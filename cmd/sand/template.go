package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// templateSnapshotter, templateDeleter, and templateDiskSizer are the narrow
// slices of provider.Provider the do* functions below actually drive — the
// same "fake the interface, not the backend" seam create.go/shell.go use
// (headlessProvisioner, vmGetter): a *providerfake.Provider satisfies all
// three, so the decision logic (name validation, collision checks, dependent
// warnings) can be unit tested without a real limactl.
type templateSnapshotter interface {
	SnapshotTemplate(ctx context.Context, source, templateInstance string, out io.Writer) (provision.SnapshotResult, error)
}

type templateDeleter interface {
	DeleteTemplate(ctx context.Context, templateInstance string, out io.Writer) error
}

type templateDiskSizer interface {
	TemplateDiskBytes(templateInstance string) int64
}

// runTemplate implements the `sand template` subcommand group: snapshot, list,
// and delete for golden VM templates (see internal/vm/template.go and
// internal/registry.Template). Mirrors runCreate/runShell's dispatch shape —
// os.Args[2:] is handed straight in from main.go, and this switches on the
// first token for the sub-subcommand.
func runTemplate(args []string) int {
	if len(args) == 0 {
		printTemplateUsage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		printTemplateUsage(os.Stdout)
		return 0
	case "snapshot":
		return runTemplateSnapshot(args[1:])
	case "list":
		return runTemplateList(args[1:])
	case "delete":
		return runTemplateDelete(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "sand template: unknown subcommand %q\n\n", args[0])
		printTemplateUsage(os.Stderr)
		return 2
	}
}

func printTemplateUsage(w io.Writer) {
	fmt.Fprint(w, `Usage: sand template <command> [flags]

Manage golden VM templates: named, reusable clone sources captured from an
existing sand-managed VM, so 'sand create --template NAME' can skip the
shared base image (and its provisioning) entirely.

Commands:
  snapshot <source-vm> <name>   capture source-vm into a new template named name
  list                          list templates in scope
  delete <name>                 delete a template

Every command accepts --profile to select a connection profile (default: the
last-used profile, else "local"). Run 'sand template <command> -h' for
command-specific flags.
`)
}

// runTemplateSnapshot implements `sand template snapshot <source> <name>`: it
// resolves the connection profile, loads the registry, and delegates the
// decision logic to doTemplateSnapshot (kept separate so it can be unit
// tested against a providerfake.Provider without any of this profile/registry
// plumbing).
func runTemplateSnapshot(args []string) int {
	fs := flag.NewFlagSet("template snapshot", flag.ContinueOnError)
	profileFlag := fs.String("profile", "", "Connection profile to act on (default: the last-used profile, else \"local\")")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: sand template snapshot <source-vm> <name> [--profile P]

Capture source-vm — an existing sand-managed VM, running or stopped — into a
new golden template named name. The source VM's power state is preserved
exactly (a running source ends running; a stopped one stays stopped).

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 2 {
		fs.Usage()
		fmt.Fprintln(os.Stderr, "sand template snapshot: need exactly two positional args: <source-vm> <name>")
		return 2
	}
	source, name := fs.Arg(0), fs.Arg(1)

	store := loadStore()
	p, scope, profile, err := bindingForProfileName(store, *profileFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sand template snapshot:", err)
		return 1
	}
	if err := p.Preflight(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	reg, loadErr := registry.Load()
	if reg == nil {
		reg = registry.NewEmpty()
	}
	if loadErr != nil {
		fmt.Fprintln(os.Stderr, "warning:", loadErr)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := doTemplateSnapshot(ctx, reg, p, scope, source, name, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "sand template snapshot:", err)
		return 1
	}

	if err := store.SetLastUsed(profile.ID); err != nil {
		fmt.Fprintln(os.Stdout, "warning: could not record last-used profile:", err)
	}
	return 0
}

// doTemplateSnapshot is runTemplateSnapshot's testable core: it validates
// name, rejects a collision with an existing template/managed VM/the base
// image, calls prov.SnapshotTemplate to do the actual stop->clone->restore,
// and records the resulting registry.Template. Config is copied from the
// source VM's own recorded config, with BaseName repointed at the template's
// own reserved instance name (vm.TemplateInstanceName(name)) — a later
// `sand create --template name` clones from THAT, never from source itself,
// so `source` staying alive/mutating afterward cannot affect the template.
func doTemplateSnapshot(ctx context.Context, reg *registry.Registry, prov templateSnapshotter, scope registry.Scope, source, name string, out io.Writer) error {
	if err := vm.ValidateTemplateName(name); err != nil {
		return err
	}
	if _, ok := reg.TemplateInScope(name, scope); ok {
		return fmt.Errorf("a template named %q already exists (delete it first, or pick a different name)", name)
	}
	if reg.IsManagedInScope(name, scope) {
		return fmt.Errorf("%q collides with an existing managed VM name", name)
	}
	if name == vm.DefaultCreateConfig().BaseName {
		return fmt.Errorf("%q collides with the base image instance name", name)
	}

	srcCfg, ok := reg.ConfigInScope(source, scope)
	if !ok {
		return fmt.Errorf("%q is not a sand-managed VM in this scope", source)
	}

	inst := vm.TemplateInstanceName(name)
	fmt.Fprintf(out, "capturing %q as template %q (stop -> clone -> restore)...\n", source, name)
	res, err := prov.SnapshotTemplate(ctx, source, inst, out)
	if err != nil {
		return fmt.Errorf("snapshot %q: %w", source, err)
	}

	tcfg := srcCfg
	tcfg.BaseName = inst
	t := registry.Template{
		Name:            name,
		Scope:           scope,
		Source:          source,
		CreatedAt:       time.Now(),
		PlaybookVersion: res.PlaybookVersion,
		ToolsetKey:      res.ToolsetKey,
		Config:          tcfg,
	}
	if err := reg.AddTemplate(t); err != nil {
		return fmt.Errorf("template captured, but recording it failed: %w", err)
	}
	fmt.Fprintf(out, "template %q captured from %q\n", name, source)
	return nil
}

// runTemplateList implements `sand template list`.
func runTemplateList(args []string) int {
	fs := flag.NewFlagSet("template list", flag.ContinueOnError)
	profileFlag := fs.String("profile", "", "Connection profile to act on (default: the last-used profile, else \"local\")")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: sand template list [--profile P]

List golden templates in scope: name, disk size, creation date, source VM,
and whether the template is stale against the playbook this build of sand
embeds.

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return 2
	}

	store := loadStore()
	p, scope, _, err := bindingForProfileName(store, *profileFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sand template list:", err)
		return 1
	}
	if err := p.Preflight(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	reg, loadErr := registry.Load()
	if reg == nil {
		reg = registry.NewEmpty()
	}
	if loadErr != nil {
		fmt.Fprintln(os.Stderr, "warning:", loadErr)
	}

	if err := doTemplateList(reg, p, scope, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "sand template list:", err)
		return 1
	}
	return 0
}

// doTemplateList is runTemplateList's testable core.
func doTemplateList(reg *registry.Registry, prov templateDiskSizer, scope registry.Scope, out io.Writer) error {
	templates := reg.TemplatesInScope(scope)
	if len(templates) == 0 {
		fmt.Fprintln(out, "no templates")
		return nil
	}

	// currentVersionFor answers "would a snapshot taken right now, with this
	// template's own tool-set, differ from what's recorded?" — the same
	// staleness question the base image asks of itself (provision.PlaybookVersion).
	// A playbook the binary cannot locate (a broken build) degrades to "unknown"
	// rather than falsely marking every template stale.
	dir, dirErr := provision.LocatePlaybook()

	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSIZE\tCREATED\tSOURCE\tSTATUS")
	for _, t := range templates {
		size := humanizeTemplateBytes(prov.TemplateDiskBytes(vm.TemplateInstanceName(t.Name)))
		status := "current"
		if dirErr != nil {
			status = "unknown"
		} else if cur, err := provision.PlaybookVersion(os.DirFS(dir), t.ToolsetKey); err != nil || cur != t.PlaybookVersion {
			status = "stale"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", t.Name, size, t.CreatedAt.Format("2006-01-02"), t.Source, status)
	}
	return tw.Flush()
}

// humanizeTemplateBytes renders a raw byte count (Provider.TemplateDiskBytes'
// return, or -1 when unmeasurable) as a human size like "8 GiB". There is no
// exported helper for this anywhere else in the repo — internal/ui has one
// but it is unexported and package-private — so this is a small, deliberate
// duplication of internal/ui/format.go's humanizeBytes rather than exporting
// across a package boundary for one CLI command.
func humanizeTemplateBytes(n int64) string {
	if n < 0 {
		return "unknown"
	}
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < len(units)-1 {
		div *= unit
		exp++
	}
	str := strconv.FormatFloat(float64(n)/float64(div), 'f', 1, 64)
	str = strings.TrimSuffix(str, ".0")
	return str + " " + units[exp]
}

// runTemplateDelete implements `sand template delete <name>`.
func runTemplateDelete(args []string) int {
	fs := flag.NewFlagSet("template delete", flag.ContinueOnError)
	profileFlag := fs.String("profile", "", "Connection profile to act on (default: the last-used profile, else \"local\")")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: sand template delete <name> [--profile P]

Delete a golden template. If any managed VMs were cloned from it, a warning
lists them (they keep working, but can no longer be recreated from this
template afterward) and the delete proceeds anyway — there is no --force
gate.

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		fmt.Fprintln(os.Stderr, "sand template delete: need exactly one positional arg: <name>")
		return 2
	}
	name := fs.Arg(0)

	store := loadStore()
	p, scope, _, err := bindingForProfileName(store, *profileFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sand template delete:", err)
		return 1
	}
	if err := p.Preflight(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	reg, loadErr := registry.Load()
	if reg == nil {
		reg = registry.NewEmpty()
	}
	if loadErr != nil {
		fmt.Fprintln(os.Stderr, "warning:", loadErr)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := doTemplateDelete(ctx, reg, p, scope, name, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "sand template delete:", err)
		return 1
	}
	return 0
}

// doTemplateDelete is runTemplateDelete's testable core: warn-and-allow, no
// --force gate, per the task spec — dependents are reported so the operator
// can make an informed call, but never block the delete.
func doTemplateDelete(ctx context.Context, reg *registry.Registry, prov templateDeleter, scope registry.Scope, name string, out io.Writer) error {
	if _, ok := reg.TemplateInScope(name, scope); !ok {
		return fmt.Errorf("no template named %q", name)
	}

	if deps := reg.DependentsOfTemplate(scope, name); len(deps) > 0 {
		fmt.Fprintf(out, "warning: %d VM(s) were cloned from template %q and will keep working, but can no longer be recreated from it once deleted: %s\n", len(deps), name, strings.Join(deps, ", "))
	}

	inst := vm.TemplateInstanceName(name)
	if err := prov.DeleteTemplate(ctx, inst, out); err != nil {
		return fmt.Errorf("delete %q: %w", name, err)
	}
	reg.RemoveTemplateScoped(scope, name)
	fmt.Fprintf(out, "template %q deleted\n", name)
	return nil
}

// resolveTemplateCreate is `sand create --template`'s decision logic,
// factored out of runCreate so it can be unit tested against a bare registry
// without constructing a provider/binding at all.
//
// An empty templateName is a no-op (ordinary `sand create`, unaffected by
// this flag): it returns ("", nil). A non-empty templateName is rejected as
// mutually exclusive with --rebuild (which targets the shared base image, not
// touched by a template clone) and with an explicitly-passed --base-name
// (the clone source for a --template create IS the template instance, so a
// second, conflicting source would be ambiguous). Otherwise it confirms the
// template exists in scope and returns its reserved Lima instance name (see
// vm.TemplateInstanceName) — what the caller must set both cfg.BaseName and
// provision.CreateOptions.TemplateSource to.
func resolveTemplateCreate(reg *registry.Registry, scope registry.Scope, templateName string, rebuild, baseNameExplicit bool) (string, error) {
	if templateName == "" {
		return "", nil
	}
	if rebuild {
		return "", fmt.Errorf("--template is mutually exclusive with --rebuild (a template create clones the template instance, not the shared base image)")
	}
	if baseNameExplicit {
		return "", fmt.Errorf("--template is mutually exclusive with an explicit --base-name (the template instance IS the clone source)")
	}
	if _, ok := reg.TemplateInScope(templateName, scope); !ok {
		return "", fmt.Errorf("no template named %q in this scope (run 'sand template list')", templateName)
	}
	return vm.TemplateInstanceName(templateName), nil
}

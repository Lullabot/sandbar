package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/lullabot/sandbar/internal/manage"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// headlessProvisioner is the narrow provisioning surface runCreate drives. An
// interface so doHeadlessCreate's bookkeeping can be unit-tested with a stub
// that "succeeds" without a real limactl/ansible run.
//
// It is the option-taking pair of methods, not the plain create/recreate the
// TUI uses, because --rebuild is an intent this layer must hand DOWN rather than
// act on: the base image may only be destroyed under the base lock, which lives
// inside the provisioner (internal/provision/baselock.go).
//
// Its method names (CreateVMWithOptions/RecreateWithOptions) predate the
// provider refactor and are kept exactly as they were — a real
// *provision.Provisioner already satisfies them natively (see the real-Lima
// e2e tests in create_e2e_test.go, which hand one in directly) — so runCreate
// bridges its provider.Provider through the small providerProvisioner adapter
// below instead of renaming this seam.
type headlessProvisioner interface {
	CreateVMWithOptions(ctx context.Context, cfg vm.CreateConfig, opts provision.CreateOptions, out io.Writer) error
	RecreateWithOptions(ctx context.Context, cfg vm.CreateConfig, opts provision.CreateOptions, out io.Writer) error
}

// providerProvisioner adapts a provider.Provider's Create/Recreate methods to
// the headlessProvisioner seam's (older, provisioner-native) method names, so
// runCreate can hand doHeadlessCreate the same centrally-constructed provider
// every other entrypoint uses without disturbing that seam's existing tests.
type providerProvisioner struct{ p provider.Provider }

func (a providerProvisioner) CreateVMWithOptions(ctx context.Context, cfg vm.CreateConfig, opts provision.CreateOptions, out io.Writer) error {
	return a.p.Create(ctx, cfg, opts, out)
}

func (a providerProvisioner) RecreateWithOptions(ctx context.Context, cfg vm.CreateConfig, opts provision.CreateOptions, out io.Writer) error {
	return a.p.Recreate(ctx, cfg, opts, out)
}

// runCreate implements the headless `sand create` subcommand: it parses a
// flag surface mirroring the original bash provisioner's (minus --ref — the
// playbook is embedded in the sand binary, so there is no ref left to pin),
// builds and validates a vm.CreateConfig, and drives the provisioner +
// managed-registry bookkeeping shared with the TUI. It never prompts; missing
// required fields are a validation error.
func runCreate(args []string) error {
	cfg := vm.DefaultCreateConfig()

	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: sand create [flags]

Headlessly provision a Claude Code development VM: no TUI, no prompts. Every
flag has a default: --git-name/--git-email fall back to the host's git config
(user.name/user.email), so on a machine with git configured `+"`sand create`"+`
needs no flags. If neither the flags nor the host git config supply an
identity, sand errors rather than fabricate a commit author. Flags mirror the
original bash provisioner's, minus --ref (the playbook is embedded in this
binary, so there is no ref to pin).

Examples:
  sand create                                                   # host git identity
  sand create --git-name "Your Name" --git-email you@example.com
  sand create --profile work                                    # create on the "work" connection profile

Flags:
`)
		fs.PrintDefaults()
	}

	cpusFlag := fs.String("cpus", fmt.Sprint(cfg.CPUs), "vCPUs")
	fs.StringVar(&cfg.Name, "name", cfg.Name, "Lima instance name")
	fs.StringVar(&cfg.BaseName, "base-name", cfg.BaseName, "Base image instance name")
	fs.StringVar(&cfg.Hostname, "hostname", cfg.Hostname, "VM hostname (default: same as --name)")
	fs.StringVar(&cfg.User, "user", cfg.User, "Primary VM user")
	fs.StringVar(&cfg.GitName, "git-name", cfg.GitName, "git user.name (default: host `git config user.name`)")
	fs.StringVar(&cfg.GitEmail, "git-email", cfg.GitEmail, "git user.email (default: host `git config user.email`)")
	fs.StringVar(&cfg.Memory, "memory", cfg.Memory, "RAM, e.g. 8GiB")
	fs.StringVar(&cfg.Disk, "disk", cfg.Disk, "Disk size, e.g. 100GiB")
	fs.StringVar(&cfg.Locale, "locale", cfg.Locale, "System locale")
	fs.StringVar(&cfg.Domain, "domain", cfg.Domain, "Domain suffix")
	fs.StringVar(&cfg.DockerProxyHost, "docker-proxy-host", cfg.DockerProxyHost, "Docker registry pull-through proxy host (optional)")
	fs.StringVar(&cfg.CloneURL, "clone-url", cfg.CloneURL, "HTTPS repo to clone into the VM (optional)")
	fs.StringVar(&cfg.CloneToken, "clone-token", cfg.CloneToken, "Token for the repo above (optional; GitHub uses it — never placed on argv inside the guest)")
	// The base-image tool-set (~500-700MB installed between Go and Java alone).
	// All three default true, so these are opt-OUT flags: an unconfigured `sand
	// create` installs everything today's base does. They configure the SHARED
	// base image, not this individual clone.
	fs.BoolVar(&cfg.WithClaude, "with-claude", cfg.WithClaude, "Install Claude Code in the base image")
	fs.BoolVar(&cfg.WithDDEV, "with-ddev", cfg.WithDDEV, "Install DDEV in the base image")
	fs.BoolVar(&cfg.WithGo, "with-go", cfg.WithGo, "Install the Go toolchain in the base image")
	fs.BoolVar(&cfg.WithJava, "with-java", cfg.WithJava, "Install a headless JDK in the base image")
	// Unlike the four above, --with-codex is opt-IN (cfg.WithCodex defaults
	// false): an unconfigured `sand create` must not start installing a tool no
	// existing base has.
	fs.BoolVar(&cfg.WithCodex, "with-codex", cfg.WithCodex, "Install OpenAI Codex in the base image")
	recreate := fs.Bool("recreate", false, "If the named instance exists and is sand-managed, delete and re-clone it")
	rebuild := fs.Bool("rebuild", false, "Destroy the base image and rebuild it from scratch before creating (a stale base is otherwise converged in place)")
	profileFlag := fs.String("profile", "", "Connection profile to create on (default: the last-used profile, else \"local\")")
	// NOTE: --ref is deliberately NOT a flag here. The original bash provisioner's
	// --ref pinned the git ref of a checked-out playbook in standalone mode;
	// sand's playbook is
	// embedded in the binary at build time (see playbook_embed.go), so there is
	// no ref left to pin at create time. This is deliberate, not a gap.

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil // usage was already printed; -h/--help is not a failure
		}
		return err // flag package already printed usage
	}

	n, err := vm.ParseCPUs(*cpusFlag)
	if err != nil {
		return err
	}
	cfg.CPUs = n

	// Resolve the backend before anything that reads or defaults host-derived
	// state: the existing base's tool-set stamp lives on whichever host limactl
	// actually runs (provision.BaseToolset needs the resolved provider's
	// host-access handle below), and Lima names the guest account after whoever
	// runs limactl — for a remote provider that is the REMOTE host's user, not
	// this machine's, so cfg.User must not be defaulted before this either.
	// Preflight runs here too, so a missing/old limactl fails before any config
	// work. --profile selects which ONE connection profile this create acts on
	// (default: the store's last-used profile, else "local"); only that
	// profile is built and preflighted — see bindingForProfileName.
	store := loadStore()
	p, scope, profile, err := bindingForProfileName(store, *profileFlag)
	if err != nil {
		return fmt.Errorf("sand create: %w", err)
	}
	if err := p.Preflight(); err != nil {
		return err
	}

	// A tool-set flag the user did NOT pass adopts what the existing base was
	// actually built with, instead of DefaultCreateConfig's all-on default. The
	// tool-set belongs to the SHARED base, so defaulting it to "everything" meant
	// a user who built a base with --with-go=false had to keep repeating that on
	// every later create — and if they forgot once, that create silently marked
	// the base stale and re-converged the Go toolchain back onto it.
	//
	// Explicit flags still win (fs.Visit reports only what was actually passed),
	// which is what makes ADDING a tool to an existing base work: --with-go on a
	// base without it is a real request, not an accidental default. With no base
	// yet, or one stamped by an older sand, there is nothing to adopt and the
	// all-on default stands.
	//
	// provision.BaseToolset takes the resolved provider's host-access handle
	// (p.HostFiles()) rather than reading a process-global: the base's stamp
	// lives on whichever host limactl actually runs (the remote host for a
	// remote provider), not necessarily this one.
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	if base, ok := provision.BaseToolset(p.HostFiles(), cfg.BaseName); ok {
		for tool, selected := range cfg.ToolPtrs() {
			if !explicit["with-"+tool] {
				*selected = base[tool]
			}
		}
	}

	// Default the VM user to the provider's host user (the remote host for remote
	// Lima, this machine for local), falling back to the local user if the host
	// could not be queried. An empty user_name would override the user role's
	// default and break the base phase's in-guest user creation.
	if cfg.User == "" {
		if u := p.HostUser(); u != "" {
			cfg.User = u
		} else {
			cfg.User = vm.HostUser()
		}
	}

	// Git identity falls back to the host's git config when the flags are
	// omitted, mirroring how the TUI form seeds those fields. If the host has no
	// identity either, Validate below errors — sand never fabricates an author.
	if cfg.GitName == "" {
		cfg.GitName = vm.HostGitConfig("user.name")
	}
	if cfg.GitEmail == "" {
		cfg.GitEmail = vm.HostGitConfig("user.email")
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("sand create: %w", err)
	}

	reg, loadErr := registry.Load()
	if reg == nil {
		reg = registry.NewEmpty()
	}
	if loadErr != nil {
		fmt.Fprintln(os.Stderr, "warning:", loadErr)
	}

	// Reconcile against the live instance list before acting, exactly like the
	// TUI does on every list load — so a VM deleted outside sand isn't wrongly
	// treated as managed (and gated recreate-able). scope confines this to the
	// resolved provider's own entries, so it can never prune (or be confused
	// with) another provider's VMs — see resolveSingle and registry.Scope.
	live, err := p.List()
	if err != nil {
		return fmt.Errorf("list existing instances: %w", err)
	}
	if _, err := manage.Reconcile(reg, live, scope); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not update managed index:", err)
	}

	// A cancellable context lets ctrl+c abort the run mid-flight, killing the
	// limactl subprocess it is currently blocked on — matching the TUI's cancel.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// p satisfies provider.Provenancer for every backend that has one today
	// (local and remote Lima — see internal/provider/limaprovenance.go); the
	// type assertion degrades to a nil Provenancer, not a panic, for a future
	// backend that does not, in which case doHeadlessCreate's RecordSuccess
	// call falls back to the registry-only behavior.
	provenancer, _ := p.(provider.Provenancer)

	if err := doHeadlessCreate(ctx, reg, providerProvisioner{p}, cfg, scope, *recreate, *rebuild, os.Stdout, provenancer); err != nil {
		return err
	}

	// Record the profile as last-used only on a successful create — by ID, so
	// a later rename of the profile does not lose the pointer (see
	// Store.SetLastUsed). Best-effort: a failure to persist it must not turn a
	// successful create into a reported failure.
	if err := store.SetLastUsed(profile.ID); err != nil {
		fmt.Fprintln(os.Stdout, "warning: could not record last-used profile:", err)
	}
	return nil
}

// doHeadlessCreate drives the create/recreate/rebuild flow and then performs
// the SAME managed-registry bookkeeping the TUI performs on a successful
// provision (recording cfg as managed — see internal/manage and
// internal/ui/model.go's provisionDoneMsg handling), so a headless-created VM
// is flagged managed and stays recreate-able exactly like one made through
// the TUI.
//
// --rebuild force-rebuilds the base image regardless of staleness detection,
// independent of --recreate (which targets the clone, not the base); both may
// be combined. --recreate is gated on the target already being a sand-managed
// VM — recreate clones from a Claude base image and would replace ANY
// instance it is pointed at, so it must never be offered for a VM sand did
// not create.
//
// IT DOES NOT DELETE THE BASE IMAGE. It used to: --rebuild force-deleted the base
// here, before the provisioner was ever called — and therefore before the base
// lock was ever taken. Another create, holding that lock, could be forty seconds
// into cloning the very disk this line removed. baselock.go's own doc comment
// names that race as one the lock exists to close, and --rebuild was the hole in
// it. The intent goes down to the provisioner instead, which destroys the base
// inside ensureBaseStopped with the lock held and no clone in flight; this
// function no longer has a lima client to delete anything with.
// provenancer is an OPTIONAL trailing argument (Go variadic — see
// manage.RecordSuccess) so every existing 8-arg call site (the unit/e2e
// tests in this package, which construct doHeadlessCreate directly with a
// stub provisioner and no real provider) keeps compiling unchanged; only
// runCreate, which has a real provider.Provider in hand, passes one. When
// present, it is threaded ONLY into the RecordSuccess call below (the
// provenance write this task's boundary scopes this file's changes to) — the
// RecreateBase call above is deliberately left registry-only here; wiring
// --recreate's own gate to provenance is out of this file's scope.
func doHeadlessCreate(ctx context.Context, reg *registry.Registry, prov headlessProvisioner, cfg vm.CreateConfig, scope registry.Scope, recreate, rebuild bool, out io.Writer, provenancer ...provider.Provenancer) error {
	opts := provision.CreateOptions{Rebuild: rebuild}

	if recreate {
		base, ok := manage.RecreateBase(reg, cfg.Name, scope)
		if !ok {
			return fmt.Errorf("%q is not a sand-managed VM — recreate refused (create it with 'sand create' first, or delete it manually and retry without --recreate)", cfg.Name)
		}
		cfg.BaseName = base
		if err := prov.RecreateWithOptions(ctx, cfg, opts, out); err != nil {
			return err
		}
	} else {
		if err := prov.CreateVMWithOptions(ctx, cfg, opts, out); err != nil {
			return err
		}
	}

	// Writes the registry cache AND (when provenancer is non-nil) the
	// authoritative provenance marker — see manage.RecordSuccess's doc
	// comment. A failure here (either write) is reported but does not fail
	// the command: the VM itself is already up either way.
	if err := manage.RecordSuccess(reg, cfg, scope, provenancer...); err != nil {
		fmt.Fprintln(out, "warning: VM ready, but recording it as managed failed:", err)
	}
	return nil
}

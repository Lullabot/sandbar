package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/lullabot/sandbar/internal/manage"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// resetter is the narrow backend surface doReset drives, so the gating and
// bookkeeping around a reset can be unit-tested without a real limactl.
// provider.Provider satisfies it natively.
type resetter interface {
	Reset(ctx context.Context, cfg vm.CreateConfig, opts provision.ResetOptions, out io.Writer) error
}

// runReset implements `sand reset NAME`: the headless spelling of the TUI's `R`,
// with the same gate (a sand-managed VM only), the same defaulting (every
// setting you do not restate comes from the VM's own recorded config), the same
// two preserve options, and the same follow-up bookkeeping.
//
// It exists because the two entrypoints had drifted into different verbs for
// the same act. The TUI could preserve a Claude login or a project tree across a
// rebuild; the CLI's only spelling was `sand create --recreate`, which could
// not, so the docs told a CLI user to go and open the TUI. `sand create
// --recreate` is now this command under an older name (see runCreate).
//
// There is deliberately NO --clone-url. A reset rebuilds the VM it is pointed
// at, project included; a different repo is a different VM, and `sand create`
// makes one. Editing the URL used to mean the preserve toggle and the clone
// disagreed about which org they were talking about — see internal/ui's
// fieldLocked for the same rule on the TUI side.
func runReset(args []string) error {
	var o resetOptions
	fs := newResetFlagSet(&o)

	if err := fs.Parse(reorderFlags(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil // usage was already printed; -h/--help is not a failure
		}
		return err // flag package already printed usage
	}
	return resetParsed(fs, &o)
}

// resetOptions is the whole flag surface of `sand reset`, bound in ONE place
// (newResetFlagSet) so the command, its help text and its tests cannot disagree
// about what exists — a test asserting "there is no --clone-url here" is only
// worth something if it inspects the same set the command parses.
type resetOptions struct {
	preserveClaude  bool
	preserveProject bool
	profile         string
	values          resetFlagValues
}

// newResetFlagSet defines `sand reset`'s flags, bound to o.
//
// The set is create's MINUS the ones a reset cannot mean: --name (the
// positional NAME is the target), --clone-url (a reset rebuilds the project the
// VM already has — see runReset), --base-name (the base comes from the VM's own
// provenance/registry record; resetting onto a different base is a create),
// --recreate (this IS the recreate), and --rebuild (that acts on the SHARED
// base image every other VM clones from, so one VM's reset must never silently
// rebuild the fleet's base — `sand create --rebuild` remains the way to ask).
func newResetFlagSet(o *resetOptions) *flag.FlagSet {
	fs := flag.NewFlagSet("reset", flag.ContinueOnError)
	fs.BoolVar(&o.preserveClaude, "preserve-claude", false, "Keep ~/.claude and ~/.claude.json (Claude Code login + history) across the rebuild")
	fs.BoolVar(&o.preserveProject, "preserve-project", false, "Keep the cloned project's per-org directory (checkout + .env) across the rebuild")
	fs.StringVar(&o.values.cpus, "cpus", "", "vCPUs (default: whatever this VM has)")
	fs.StringVar(&o.values.hostname, "hostname", "", "VM hostname (default: whatever this VM has)")
	fs.StringVar(&o.values.user, "user", "", "Primary VM user (default: whatever this VM has)")
	fs.StringVar(&o.values.gitName, "git-name", "", "git user.name written into the VM (default: whatever this VM has)")
	fs.StringVar(&o.values.gitEmail, "git-email", "", "git user.email written into the VM (default: whatever this VM has)")
	fs.StringVar(&o.values.memory, "memory", "", "RAM, e.g. 8GiB (default: whatever this VM has)")
	fs.StringVar(&o.values.disk, "disk", "", "Disk size, e.g. 100GiB (default: whatever this VM has; a clone's disk can grow but never shrink)")
	fs.StringVar(&o.values.locale, "locale", "", "System locale (default: whatever this VM has)")
	fs.StringVar(&o.values.timezone, "timezone", "", "IANA timezone for the guest (default: whatever this VM has)")
	fs.StringVar(&o.values.domain, "domain", "", "Domain suffix (default: whatever this VM has)")
	fs.StringVar(&o.values.dockerProxy, "docker-proxy-host", "", "Docker registry pull-through proxy host (default: whatever this VM has)")
	fs.StringVar(&o.values.cloneToken, "clone-token", "", "Token for this VM's recorded repo (tokens are never stored in the index; pass it again for a private repo)")
	fs.StringVar(&o.profile, "profile", "", "Connection profile NAME lives on (only needed when NAME exists under more than one enabled profile)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: sand reset NAME [flags]

Delete a sand-managed VM and clone it fresh from its base image, keeping its
name, its project, and every setting it was built with. This is the headless
spelling of the TUI's R (Reset).

Everything inside the guest is lost unless you ask for it back:

  --preserve-claude    keep ~/.claude and ~/.claude.json (the Claude Code login
                       and its history)
  --preserve-project   keep the cloned project's per-org directory (the checkout,
                       its uncommitted work, and the .env alongside it)

Both copy data out of the VM to this host and back in afterwards. Do NOT
preserve anything from a VM you believe is compromised.

Every other flag you omit is taken from the VM's own recorded settings, so
'sand reset web' means "give me this VM back". Pass one to change it:
'sand reset web --disk 200GiB' resizes on the way through.

There is no --clone-url: a reset rebuilds the project this VM already has. To
work on a different repo, create another VM with 'sand create'.

Examples:
  sand reset web                                  # clean rebuild, same settings
  sand reset web --preserve-claude                # keep the Claude login
  sand reset web --preserve-claude --preserve-project
  sand reset web --cpus 8 --memory 16GiB          # rebuild bigger

Flags:
`)
		fs.PrintDefaults()
	}
	return fs
}

// resetParsed is runReset past its flag parsing: it resolves the VM, gates on
// it being sand-managed, builds the config, runs the reset and settles the
// bookkeeping. Split from runReset so the parse and the act read separately —
// the parse is where the "no --clone-url" decision lives, and the act is where
// everything talks to a real backend.
func resetParsed(fs *flag.FlagSet, o *resetOptions) error {
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("sand reset: need exactly one VM name")
	}
	name := fs.Arg(0)

	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	store := loadStore()
	reg, loadErr := registry.Load()
	if reg == nil {
		reg = registry.NewEmpty()
	}
	if loadErr != nil {
		fmt.Fprintln(os.Stderr, "warning:", loadErr)
	}

	// WHICH profile owns NAME is resolved the same way `sand shell NAME` resolves
	// it (marker first, then the registry, then a live listing) rather than
	// create's "the profile you last used": a reset acts on a VM that already
	// exists, so the VM decides, not a default.
	target, err := resolveVMProfile(store, reg, name, o.profile)
	if err != nil {
		return fmt.Errorf("sand reset: %w", err)
	}
	p, scope, err := providerForProfile(target)
	if err != nil {
		return fmt.Errorf("sand reset: %w", err)
	}
	if err := p.Preflight(); err != nil {
		return err
	}

	// Reconcile against the live listing first, exactly as `sand create` does, so
	// a VM deleted outside sand is not still treated as managed (and resettable).
	live, err := p.List()
	if err != nil {
		return fmt.Errorf("list existing instances: %w", err)
	}
	if _, err := manage.Reconcile(reg, live, scope); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not update managed index:", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	provenancer, _ := p.(provider.Provenancer)
	manage.AdoptOnce(ctx, reg.ManagedInScope(scope), live, scope, provenancer)

	base, ok := manage.RecreateBase(reg, name, scope, provenancer)
	if !ok {
		return fmt.Errorf("sand reset: %q is not a sand-managed VM — refused (a reset clones from a sandbar base image and would replace whatever instance it was pointed at)", name)
	}

	cfg, err := resetConfigFor(reg, name, scope, base, explicit, o.values, p.HostUser())
	if err != nil {
		return fmt.Errorf("sand reset: %w", err)
	}

	if cfg.CloneURL != "" && cfg.CloneToken == "" {
		// A recorded clone URL comes back without its token (registry.Add strips
		// the secret before it ever reaches disk), and a preserved project skips
		// the clone entirely — so this only bites a private repo being re-cloned.
		// Say so rather than let the finalize playbook fail on `git clone`.
		fmt.Fprintf(os.Stderr, "sand: %s clones %s; tokens are never stored, so pass --clone-token if that repo is private.\n", name, cfg.CloneURL)
	}

	opts := provision.ResetOptions{PreserveClaude: o.preserveClaude, PreserveProject: o.preserveProject}
	if err := doReset(ctx, reg, p, cfg, scope, opts, os.Stdout, provenancer); err != nil {
		return err
	}
	// The host secrets, applied into the rebuilt guest — the step `sand create
	// --recreate` never took, which left a reset VM silently without the secrets
	// the old one had until someone started it from the TUI. See settleSecrets.
	settleSecrets(ctx, p, scope, cfg, os.Stdout)
	return nil
}

// resetFlagValues is the raw flag surface resetConfigFor applies over a VM's
// recorded config. A struct rather than fifteen parameters, and separate from
// the `explicit` set because an empty string is a legitimate value for some of
// these (clearing --docker-proxy-host) — what decides is whether the flag was
// PASSED, not whether it is empty.
type resetFlagValues struct {
	cpus, hostname, user, gitName, gitEmail string
	memory, disk, locale, timezone, domain  string
	dockerProxy, cloneToken                 string
}

// resetConfigFor builds the config a reset rebuilds NAME from: the VM's own
// recorded settings, with any flag the user actually passed applied on top.
//
// The recorded config is the SOURCE, not a fallback, and that is the whole
// contract of the verb — the same one `sand create --recreate` was fixed to
// honour and the TUI's reset form has always had: `sand reset web` means "give
// me this VM back", never "give me a default VM with this name". A VM with no
// recorded config (a marker-only instance created by another controller) still
// resets, from defaults plus this host's identity, because refusing would leave
// the one entrypoint that can rebuild it unable to.
func resetConfigFor(reg *registry.Registry, name string, scope registry.Scope, base string, explicit map[string]bool, flags resetFlagValues, hostUser string) (vm.CreateConfig, error) {
	cfg := vm.DefaultCreateConfig()
	cfg.Name = name

	rec, found := reg.ConfigInScope(name, scope)
	if found && rec.Name != "" {
		// Reuse the create path's adoption table so the two commands cannot drift
		// on what "a flag you did not pass" means. It is keyed by FLAG NAME, and
		// this flag set uses create's names for every setting it shares.
		adoptRecordedConfig(&cfg, rec, explicit)
		// The tool-set is not adopted from the flags (this command has none): it
		// is the VM's own recorded selection, replayed exactly as the TUI's reset
		// form replays it. Requesting the full default set instead would mark the
		// SHARED base stale and re-converge tools the user opted out of.
		cfg.WithClaude, cfg.WithCodex = rec.WithClaude, rec.WithCodex
		cfg.WithDDEV, cfg.WithGo, cfg.WithJava = rec.WithDDEV, rec.WithGo, rec.WithJava
	} else {
		// No record: default the identity from this host, mirroring the TUI's
		// openResetForm fallback for a pre-snapshot index entry.
		cfg.User = hostUser
		cfg.GitName = vm.HostGitConfig("user.name")
		cfg.GitEmail = vm.HostGitConfig("user.email")
		if zone, detected := vm.HostTimezone(); detected {
			cfg.Timezone = zone
		}
	}

	// The base comes from the provenance marker (or the registry), never a flag:
	// see runReset's flag-set comment.
	cfg.BaseName = base

	if explicit["cpus"] {
		n, err := vm.ParseCPUs(flags.cpus)
		if err != nil {
			return cfg, err
		}
		cfg.CPUs = n
	}
	if explicit["hostname"] {
		cfg.Hostname = flags.hostname
	}
	if explicit["user"] {
		cfg.User = flags.user
	}
	if explicit["git-name"] {
		cfg.GitName = flags.gitName
	}
	if explicit["git-email"] {
		cfg.GitEmail = flags.gitEmail
	}
	if explicit["memory"] {
		cfg.Memory = flags.memory
	}
	if explicit["disk"] {
		cfg.Disk = flags.disk
	}
	if explicit["locale"] {
		cfg.Locale = flags.locale
	}
	if explicit["timezone"] {
		// TimezoneExplicit records that a HUMAN named this zone, which is what
		// makes an unknown one fatal in the guest rather than a warning.
		cfg.Timezone, cfg.TimezoneExplicit = flags.timezone, true
	}
	if explicit["domain"] {
		cfg.Domain = flags.domain
	}
	if explicit["docker-proxy-host"] {
		cfg.DockerProxyHost = flags.dockerProxy
	}
	cfg.CloneToken = flags.cloneToken

	// Fall back to the host's git identity when neither the record nor a flag
	// supplied one — a VM recorded before those fields existed would otherwise
	// fail Validate on a reset that changed nothing.
	if cfg.GitName == "" {
		cfg.GitName = vm.HostGitConfig("user.name")
	}
	if cfg.GitEmail == "" {
		cfg.GitEmail = vm.HostGitConfig("user.email")
	}
	if cfg.User == "" {
		cfg.User = hostUser
	}
	if cfg.User == "" {
		cfg.User = vm.HostUser()
	}

	if err := cfg.Validate(); err != nil {
		if found {
			return cfg, fmt.Errorf("%s's recorded config is unusable (%w); pass the settings explicitly", name, err)
		}
		return cfg, err
	}
	return cfg, nil
}

// doReset runs the reset and then performs the SAME managed-registry and
// provenance bookkeeping a successful create does (manage.RecordSuccess), so a
// VM rebuilt headlessly stays managed, resettable, and visible to any other
// controller of the same host. Split from runReset so the gate-and-bookkeeping
// half is testable with a stub backend.
func doReset(ctx context.Context, reg *registry.Registry, p resetter, cfg vm.CreateConfig, scope registry.Scope, opts provision.ResetOptions, out io.Writer, provenancer ...provider.Provenancer) error {
	if err := p.Reset(ctx, cfg, opts, out); err != nil {
		return err
	}
	if err := manage.RecordSuccess(reg, cfg, scope, provenancer...); err != nil {
		fmt.Fprintln(out, "warning: VM ready, but recording it as managed failed:", err)
	}
	return nil
}

// reorderFlags moves flag tokens (and the values of flags that take one) ahead
// of the positional arguments, so `sand reset web --disk 200GiB` parses the
// same as `sand reset --disk 200GiB web`. flag.FlagSet stops parsing at the
// first non-flag token, which would otherwise make the natural spelling —
// verb, then VM, then options — silently drop every flag after the name.
//
// Unlike shell.go's reorderShellFlags, which hard-codes its two flags, this
// asks the FlagSet itself which names it knows and which of them take a value
// (a bool flag consumes no following token), so a flag added to the set above
// needs no change here. An unrecognised flag stays where it is and reaches
// fs.Parse, which reports it exactly as it would have.
func reorderFlags(fs *flag.FlagSet, args []string) []string {
	// A bool flag consumes no following token ("--preserve-claude web" must
	// leave "web" a positional), so the two kinds have to be told apart. This is
	// the same rule flag.FlagSet applies internally, asked of the set itself
	// rather than restated as a list that would go stale.
	takesValue := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			return
		}
		takesValue[f.Name] = true
	})

	var flagArgs, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			// The end-of-flags marker: everything from here on is positional by
			// definition, in the order it was given.
			positional = append(positional, args[i:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			positional = append(positional, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		inline := false
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name, inline = name[:eq], true
		}
		if name != "h" && name != "help" && fs.Lookup(name) == nil {
			// Unknown: leave it where it is so fs.Parse reports it exactly as it
			// would have, rather than swallowing a typo into the flag block.
			positional = append(positional, a)
			continue
		}
		flagArgs = append(flagArgs, a)
		if takesValue[name] && !inline && i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}
	return append(flagArgs, positional...)
}

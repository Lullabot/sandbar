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
	// Registered with an EMPTY default and applied after Parse, rather than
	// bound straight to cfg.Timezone like --locale above. cfg.Timezone already
	// holds this host's zone (vm.HostTimezone, via DefaultCreateConfig), and
	// binding it directly would make the flag package print that value as the
	// default — so `sand create --help` would read "(default
	// "America/Toronto")" on one machine and "(default "Europe/Berlin")" on the
	// next, and the copy of that help text in docs/using-sand/cli-reference.md
	// would be wrong for almost every reader. Same reason --git-name describes
	// its host-derived default in prose instead of showing one.
	timezoneFlag := fs.String("timezone", "", "IANA timezone for the guest, e.g. America/Toronto (default: the timezone this host is in)")
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

	// An explicitly passed --timezone wins; otherwise detect the host's, which
	// DefaultCreateConfig deliberately does not do (it is a hot, pure accessor —
	// see the comment there). TimezoneExplicit rides along so the guest knows
	// whether an unknown zone is worth failing the run over: a name the USER
	// named is, a name sand guessed is not.
	//
	// Validate rejects a malformed value before any of it reaches the playbook;
	// whether the zone actually EXISTS is a question only the guest can answer,
	// and roles/base answers it there.
	if *timezoneFlag != "" {
		cfg.Timezone, cfg.TimezoneExplicit = *timezoneFlag, true
	} else if zone, detected := vm.HostTimezone(); detected {
		cfg.Timezone = zone
	} else {
		// Say so rather than quietly handing over a UTC VM: the documented
		// promise is that the guest matches this host, and on a machine that
		// will not reveal its zone sand cannot keep it. Without this line the
		// only symptom is a VM with the wrong clock and nothing to explain it.
		fmt.Fprintf(os.Stderr, "sand: could not determine this host's timezone; using %s. Pass --timezone to set it explicitly.\n", cfg.Timezone)
	}

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
	//
	// The adoption itself is deferred until after the --recreate block below,
	// because it is keyed by cfg.BaseName and that block may still change which
	// base this VM belongs to.
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

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

	// --recreate rebuilds a VM sand ALREADY KNOWS, so every setting the user did
	// not restate on the command line comes from that VM's own recorded config
	// rather than from this flag set's defaults. Without this, `sand create
	// --recreate --name mybox` — the obvious spelling of "give me this VM back" —
	// silently returned a DIFFERENT VM: memory and disk reset to the flag
	// defaults, and the clone URL dropped entirely, so the project checkout the
	// VM existed for was simply not there. The recreate then recorded those
	// defaults, making the loss permanent for the next recreate too.
	//
	// It is the same rule the tool-set flags above already follow, applied to the
	// rest of the config: fs.Visit reports only what was actually passed, so an
	// explicit flag still wins and `--recreate --disk 200GiB` remains the way to
	// resize. This is also what makes the CLI agree with the TUI's Reset, which
	// has always pre-filled its form from the recorded config.
	//
	// The tool-set is deliberately NOT adopted from the record: those flags
	// configure the SHARED base image, and the block below adopts them from what
	// the base was actually built with — a better source than one VM's recorded
	// wish. Which is also why that block runs AFTER this one: --base-name is
	// adopted here, and the tool-set has to be read off the base this VM is
	// really cloned from.
	if *recreate {
		if rec, ok := reg.ConfigInScope(cfg.Name, scope); ok && rec.Name != "" {
			adoptRecordedConfig(&cfg, rec, explicit)
			// A recorded clone URL comes back without its token — registry.Add
			// strips the secret before it ever reaches disk — so say so rather than
			// let the finalize playbook fail on `git clone` with no credentials.
			if cfg.CloneURL != "" && cfg.CloneToken == "" && !explicit["clone-url"] {
				fmt.Fprintf(os.Stderr, "sand: reusing %s's recorded --clone-url %s; tokens are never stored, so pass --clone-token if that repo is private.\n", cfg.Name, cfg.CloneURL)
			}
			// The record was valid when it was written, but it is a file on disk
			// and this is the last point anything checks it.
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("sand create: %s's recorded config is unusable (%w); pass the settings explicitly", cfg.Name, err)
			}
		}
	}

	// Now that cfg.BaseName is final, adopt the tool-set from that base's stamp
	// (see the fs.Visit block above for why an omitted --with-* flag defers to
	// the base instead of to DefaultCreateConfig's all-on default).
	if base, ok := provision.BaseToolset(p.HostFiles(), cfg.BaseName); ok {
		for tool, selected := range cfg.ToolPtrs() {
			if !explicit["with-"+tool] {
				*selected = base[tool]
			}
		}
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

	// One-time (per process, per target) migration: stamp provenance markers
	// onto VMs this registry already recorded as managed but that predate
	// provenance — see manage.AdoptOnce. `live` is the same listing Reconcile
	// just used above, so this costs no extra round trip. A no-op after the
	// first successful run for this scope (or if the backend has no
	// Provenancer).
	manage.AdoptOnce(ctx, reg.ManagedInScope(scope), live, scope, provenancer)

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

// adoptRecordedConfig copies rec's settings into cfg for every field whose flag
// the user did not pass, leaving explicitly-passed flags untouched. It is the
// --recreate half of the "an omitted flag means 'whatever this already was'"
// rule (see its call site); explicit is the set fs.Visit reported.
//
// Each field is listed by hand rather than reflected over, because the mapping
// from field to flag name is exactly what a reader needs to check and the
// omissions are deliberate: CloneToken is never recorded (registry.Add strips
// it), and the tool-set flags adopt from the base image instead.
func adoptRecordedConfig(cfg *vm.CreateConfig, rec vm.CreateConfig, explicit map[string]bool) {
	// The base image this VM was cloned from is part of "whatever this already
	// was" too: recreating a VM built on `--base-name work-base` against the
	// DEFAULT base would clone a different image — and, if no default base
	// exists yet, silently build a whole second one.
	if !explicit["base-name"] && rec.BaseName != "" {
		cfg.BaseName = rec.BaseName
	}
	if !explicit["hostname"] && rec.Hostname != "" {
		cfg.Hostname = rec.Hostname
	}
	if !explicit["user"] && rec.User != "" {
		cfg.User = rec.User
	}
	if !explicit["git-name"] && rec.GitName != "" {
		cfg.GitName = rec.GitName
	}
	if !explicit["git-email"] && rec.GitEmail != "" {
		cfg.GitEmail = rec.GitEmail
	}
	if !explicit["cpus"] && rec.CPUs > 0 {
		cfg.CPUs = rec.CPUs
	}
	if !explicit["memory"] && rec.Memory != "" {
		cfg.Memory = rec.Memory
	}
	if !explicit["disk"] && rec.Disk != "" {
		cfg.Disk = rec.Disk
	}
	if !explicit["locale"] && rec.Locale != "" {
		cfg.Locale = rec.Locale
	}
	if !explicit["domain"] && rec.Domain != "" {
		cfg.Domain = rec.Domain
	}
	if !explicit["docker-proxy-host"] {
		cfg.DockerProxyHost = rec.DockerProxyHost
	}
	// The clone URL adopts even when the record is empty: "this VM cloned
	// nothing" is an answer, and the flag default says the same thing anyway.
	if !explicit["clone-url"] {
		cfg.CloneURL = rec.CloneURL
	}
	// TimezoneExplicit rides along with the zone it describes — it records
	// whether a HUMAN named that zone, which decides whether the guest treats an
	// unknown one as fatal, and it would be a lie carried over on its own.
	if !explicit["timezone"] && rec.Timezone != "" {
		cfg.Timezone, cfg.TimezoneExplicit = rec.Timezone, rec.TimezoneExplicit
	}
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
// runCreate, which has a real provider.Provider in hand, passes one. It is
// threaded into BOTH the RecreateBase gate below (a marker-only VM — created
// on another controller, so absent from this machine's registry — can still
// be reset here) and the RecordSuccess call (the provenance write proper);
// omitted (nil), both fall back to their pre-provenance, registry-only
// behavior exactly as before.
func doHeadlessCreate(ctx context.Context, reg *registry.Registry, prov headlessProvisioner, cfg vm.CreateConfig, scope registry.Scope, recreate, rebuild bool, out io.Writer, provenancer ...provider.Provenancer) error {
	opts := provision.CreateOptions{Rebuild: rebuild}

	if recreate {
		base, ok := manage.RecreateBase(reg, cfg.Name, scope, provenancer...)
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

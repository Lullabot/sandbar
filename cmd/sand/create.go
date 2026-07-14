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

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/manage"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// headlessProvisioner is the narrow provision.Provisioner surface runCreate
// drives. An interface so doHeadlessCreate's bookkeeping can be unit-tested with
// a stub that "succeeds" without a real limactl/ansible run.
//
// It is the option-taking pair of methods, not the plain CreateVM/Recreate the
// TUI uses, because --rebuild is an intent this layer must hand DOWN rather than
// act on: the base image may only be destroyed under the base lock, which lives
// inside the provisioner (internal/provision/baselock.go).
type headlessProvisioner interface {
	CreateVMWithOptions(ctx context.Context, cfg vm.CreateConfig, opts provision.CreateOptions, out io.Writer) error
	RecreateWithOptions(ctx context.Context, cfg vm.CreateConfig, opts provision.CreateOptions, out io.Writer) error
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
	recreate := fs.Bool("recreate", false, "If the named instance exists and is sand-managed, delete and re-clone it")
	rebuild := fs.Bool("rebuild", false, "Destroy the base image and rebuild it from scratch before creating (a stale base is otherwise converged in place)")
	// NOTE: --ref is deliberately NOT a flag here. The original bash provisioner's
	// --ref pinned the git ref of a checked-out playbook in standalone mode;
	// sand's playbook is
	// embedded in the binary at build time (see playbook_embed.go), so there is
	// no ref left to pin at create time. This is not a gap — see task 3 notes.

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

	// Lima creates a guest user matching the host username; default cfg.User to
	// it when --user is omitted, exactly as the TUI form and the original bash
	// provisioner do. Passing an empty user_name would otherwise override the
	// user role's default and break the base phase's in-guest user creation.
	if cfg.User == "" {
		cfg.User = vm.HostUser()
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

	cli := lima.New(lima.NewExecRunner())
	if err := cli.Preflight(); err != nil {
		return err
	}

	dir, err := provision.LocatePlaybook()
	if err != nil {
		return err
	}
	prov := &provision.Provisioner{Lima: cli, PlaybookDir: dir}

	reg, loadErr := registry.Load()
	if reg == nil {
		reg = registry.NewEmpty()
	}
	if loadErr != nil {
		fmt.Fprintln(os.Stderr, "warning:", loadErr)
	}

	// Reconcile against the live instance list before acting, exactly like the
	// TUI does on every list load — so a VM deleted outside sand isn't wrongly
	// treated as managed (and gated recreate-able).
	live, err := cli.List()
	if err != nil {
		return fmt.Errorf("list existing instances: %w", err)
	}
	if _, err := manage.Reconcile(reg, live); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not update managed index:", err)
	}

	// A cancellable context lets ctrl+c abort the run mid-flight, killing the
	// limactl subprocess it is currently blocked on — matching the TUI's cancel.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return doHeadlessCreate(ctx, reg, prov, cfg, *recreate, *rebuild, os.Stdout)
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
func doHeadlessCreate(ctx context.Context, reg *registry.Registry, prov headlessProvisioner, cfg vm.CreateConfig, recreate, rebuild bool, out io.Writer) error {
	opts := provision.CreateOptions{Rebuild: rebuild}

	if recreate {
		base, ok := manage.RecreateBase(reg, cfg.Name)
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

	if err := manage.RecordSuccess(reg, cfg); err != nil {
		fmt.Fprintln(out, "warning: VM ready, but recording it as managed failed:", err)
	}
	return nil
}

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// limaRunning is the status string limactl reports for a running instance
// (mirrors internal/ui's identically-named, unexported constant, and the same
// "Running" contract the provider's Status returns).
const limaRunning = "Running"

// vmGetter is the narrow backend surface shellAttachArgv needs: looking up ONE
// instance by name (resolving its status and Dir together — Dir does not come
// back from Status) and building its attach argv. An interface, satisfied by
// provider.Provider, so the decision logic can be unit tested with a stub
// instead of a real provider, which would need a real limactl (AGENTS.md, hard
// rule).
//
// Get, not List, and that is the fix for a real bug: `limactl list` with no name
// fails outright while ANY instance is mid-clone (lima#5236), so scanning the full
// listing to find one VM made `sand shell web` die instantly for the 40-60s a
// create of some OTHER VM was cloning — and from a host tmux, the new window it
// died in closed before the error could be read. See provider.Provider.Get.
type vmGetter interface {
	Get(name string) (vm.VM, error)
	AttachArgv(v vm.VM) []string
}

// registryOwnership is the narrow registry surface resolveShellProvider needs
// to determine which profile(s) own a managed VM name. Narrowed to an
// interface (satisfied by *registry.Registry) so the ambiguous-ownership
// decision logic can be unit tested with a fake that reports a name owned by
// more than one scope at once — a state the real on-disk registry (currently
// keyed by VM name alone, one entry per name) cannot reach through its own
// public API, but which the decision logic must still handle correctly.
//
// Ownership resolution is provenance-first now (see probeProvenanceOwners);
// this interface remains the LEGACY, one-release fallback consulted only
// when no candidate profile's provenance marker names NAME.
type registryOwnership interface {
	IsManagedInScope(name string, scope registry.Scope) bool
}

// runShell implements the `sand shell <name>` subcommand: it resolves the named
// VM's status and instance dir together, refuses cleanly when the VM is unknown
// or not running, and otherwise execs the attach argv built by the provider's
// AttachArgv — the one place in sand that knows tmux exists, and which the
// TUI's `S` verb builds on too, so the two entrypoints cannot drift. stdio is
// inherited because a tmux client needs the real terminal.
//
// The TUI withholds `S` from a VM that is not running (commandreg.go's enabledFor
// gate, which also hides the verb from the footer). A CLI has no footer to
// withhold, so the same rule has to be stated in words instead — see
// shellAttachArgv.
func runShell(args []string) error {
	fs := flag.NewFlagSet("shell", flag.ContinueOnError)
	profileFlag := fs.String("profile", "", "Connection profile NAME lives on (only needed when NAME exists under more than one enabled profile)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: sand shell NAME [--profile <name>]

Attach a shell to NAME's persistent tmux session in the guest.

  C-a c   new window          C-a d   detach
  C-a |   split vertically    C-a S   split horizontally

Detaching — or just closing the terminal — leaves the session and everything
running in it alive; attach again with this same command and it is all still
there. Note C-a is tmux's prefix here, so it no longer moves the cursor to the
start of the line.

A second terminal running this command shares the same windows but keeps its
own current one, so two terminals can look at two different windows of the
same VM.

The named VM must already exist and be running (see 'sand' to list instances,
or 'sand create' to make one). If NAME is managed under more than one
connection profile, --profile picks which one to attach to.
`)
	}
	// --profile may appear before or after NAME; reorder so all flags precede
	// the positional argument, which is what flag.FlagSet.Parse requires (it
	// stops parsing flags at the first non-flag token).
	if err := fs.Parse(reorderShellFlags(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil // usage was already printed; -h/--help is not a failure
		}
		return err // flag package already printed usage
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("sand shell: need exactly one VM name")
	}
	name := fs.Arg(0)

	store := loadStore()
	reg, loadErr := registry.Load()
	if reg == nil {
		reg = registry.NewEmpty()
	}
	if loadErr != nil {
		fmt.Fprintln(os.Stderr, "warning:", loadErr)
	}

	p, err := resolveShellProvider(store, reg, name, *profileFlag)
	if err != nil {
		return fmt.Errorf("sand shell: %w", err)
	}
	if err := p.Preflight(); err != nil {
		return err
	}

	argv, err := shellAttachArgv(p, name)
	if err != nil {
		return err
	}

	// The interactive attach deliberately bypasses the provider's buffered exec
	// path (which captures output for the typed lifecycle calls above) because
	// a tmux client needs the real terminal, not a pipe: hence a bare
	// exec.Command with inherited stdio rather than anything that runs through
	// the provider's own Shell/ShellOut.
	c := exec.Command(argv[0], argv[1:]...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Propagate the child's real exit code rather than collapsing every
			// failure to main.go's blanket os.Exit(1).
			//
			// A child killed by a signal (the terminal window closes while the
			// tmux client is attached, so limactl takes a SIGHUP/SIGTERM) has no
			// exit code: ExitCode() returns -1, and os.Exit(-1) is out of range
			// and surfaces as a bare 255. Report the shell convention 128+N so a
			// caller can tell "the terminal went away" from a real failure.
			code := exitErr.ExitCode()
			if code < 0 {
				if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
					code = 128 + int(ws.Signal())
				} else {
					code = 1
				}
			}
			os.Exit(code)
		}
		return fmt.Errorf("sand shell: %w", err)
	}
	return nil
}

// shellAttachArgv is the decision logic runShell delegates to: it looks up
// name in the live instance list (List, not Status, because Dir — needed to
// resolve the guest home — only comes back from List) and returns a clear,
// actionable error for an unknown instance or one that is not running, rather
// than letting a raw limactl error or a stack trace reach the user. Factored
// out from runShell so it can be tested with a stub vmLister; the exec
// hand-off above needs a real TTY and a real VM and is deliberately left
// untested here.
func shellAttachArgv(l vmGetter, name string) ([]string, error) {
	found, err := l.Get(name)
	if err != nil {
		if errors.Is(err, lima.ErrNoSuchInstance) {
			return nil, fmt.Errorf("sand shell: no VM named %q (run 'sand' to list instances)", name)
		}
		return nil, fmt.Errorf("sand shell: %w", err)
	}
	if found.Status != limaRunning {
		return nil, fmt.Errorf("sand shell: VM %q is not running (status: %s); start it first", name, found.Status)
	}

	return l.AttachArgv(found), nil
}

// reorderShellFlags moves every recognised flag token (and, for --profile,
// its value) ahead of the positional arguments in args, so `sand shell NAME
// --profile work` parses the same as `sand shell --profile work NAME` under
// flag.FlagSet, which otherwise stops parsing flags at the first non-flag
// token. Only the flags this subcommand defines (-h/--help, --profile) are
// recognised; anything else is left as positional so an unrecognised flag
// still reaches fs.Parse and produces its normal error.
func reorderShellFlags(args []string) []string {
	var flagArgs, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help" || a == "-help":
			flagArgs = append(flagArgs, a)
		case a == "--profile" || a == "-profile":
			flagArgs = append(flagArgs, a)
			if i+1 < len(args) {
				i++
				flagArgs = append(flagArgs, args[i])
			}
		case strings.HasPrefix(a, "--profile=") || strings.HasPrefix(a, "-profile="):
			flagArgs = append(flagArgs, a)
		default:
			positional = append(positional, a)
		}
	}
	return append(flagArgs, positional...)
}

// listForProfile constructs profile p's provider and returns its List() —
// used only by resolveShellProvider's unmanaged-VM fallback (see
// probeUnmanagedOwners). A package-level var, following fleet.go's
// newDefault/newRemoteLima seam pattern, so a test can stub it without a
// real limactl/SSH backend.
var listForProfile = func(p profiles.Profile) ([]vm.VM, error) {
	prov, _, err := providerForProfile(p)
	if err != nil {
		return nil, err
	}
	return prov.List()
}

// probeUnmanagedOwners is resolveShellProvider's fallback for when the
// managed-VM registry reports ZERO owners for name: before this task, `sand
// shell NAME` attached to ANY VM the (single) configured backend listed,
// managed or not (the base image `sand-base`, a hand-made limactl VM, ...).
// Now that ownership with more than one enabled profile is resolved from the
// registry, an UNMANAGED vm has no registry entry under any profile and used
// to hard-fail with "no such VM" even though it plainly exists somewhere.
// So, when the registry comes up empty, ask each enabled profile's provider
// directly whether it knows a VM by this name — local first (the common
// case), then the rest in the store's own order. A profile whose provider
// fails to construct, or whose List() errors (e.g. an unreachable remote),
// is treated as "not there": this is best-effort probing, and one bad
// profile must never turn into a hard failure when another profile actually
// has the VM, or when the honest answer is a clean "no such VM".
func probeUnmanagedOwners(enabled []profiles.Profile, name string) []profiles.Profile {
	ordered := make([]profiles.Profile, 0, len(enabled))
	var remotes []profiles.Profile
	for _, p := range enabled {
		if p.Type == profiles.TypeLocal {
			ordered = append(ordered, p)
		} else {
			remotes = append(remotes, p)
		}
	}
	ordered = append(ordered, remotes...)

	var hits []profiles.Profile
	for _, p := range ordered {
		vms, err := listForProfile(p)
		if err != nil {
			continue
		}
		for _, v := range vms {
			if v.Name == name {
				hits = append(hits, p)
				break
			}
		}
	}
	return hits
}

// provenanceOfForProfile constructs profile p's provider and reports whether
// it carries a provenance marker for name — resolveShellProvider's PRIMARY,
// authoritative ownership signal for a multi-profile lookup (see
// probeProvenanceOwners). A provider that does not implement
// provider.Provenancer (none exist today, but the interface makes no
// promise) reports (false, nil) — "no marker", never a hard error, so
// ownership resolution still has the registry and the unmanaged-VM probe to
// fall back to. A package-level var, following listForProfile's seam
// pattern, so a test can stub it without a real limactl/SSH backend.
var provenanceOfForProfile = func(p profiles.Profile, name string) (bool, error) {
	prov, _, err := providerForProfile(p)
	if err != nil {
		return false, err
	}
	pv, ok := prov.(provider.Provenancer)
	if !ok {
		return false, nil
	}
	_, found, err := pv.ProvenanceOf(context.Background(), name)
	return found, err
}

// probeProvenanceOwners asks each enabled profile's provenance marker for
// name — resolveShellProvider's first and authoritative ownership signal,
// mirroring manage.RecreateBase's marker-first resolution order. Best-effort
// like probeUnmanagedOwners: a profile whose provider fails to construct, or
// whose provenance read errors, is treated as "no marker" rather than
// aborting the whole lookup — the registry (LEGACY, one-release fallback)
// and the unmanaged-VM probe still get a chance to resolve it.
func probeProvenanceOwners(enabled []profiles.Profile, name string) []profiles.Profile {
	var hits []profiles.Profile
	for _, p := range enabled {
		if found, err := provenanceOfForProfile(p, name); err == nil && found {
			hits = append(hits, p)
		}
	}
	return hits
}

// resolveShellProvider resolves which connection profile NAME lives on and
// constructs its provider. An explicit profile name is used directly (a hard
// error if it does not name an enabled profile). With no explicit profile:
// a store with only one enabled profile always uses it (preserving `sand
// shell`'s original behaviour of attaching to any VM the one configured
// backend knows about, managed or not); with more than one enabled profile,
// ownership is resolved provenance-first: each candidate profile's marker is
// consulted via probeProvenanceOwners (one ProvenanceOf per candidate). If
// NO profile's marker names NAME, the registry's managed-VM index is
// consulted next — LEGACY, remove after one release: it exists only so a VM
// recorded by a pre-provenance sand (or a controller that has not upgraded
// yet) does not lose shell routing the moment this ships. If THAT also comes
// up empty, probeUnmanagedOwners is tried before giving up (an UNMANAGED vm,
// e.g. the base image or a hand-made instance, has no marker or registry
// entry under any profile — see its doc comment). Zero owners from all three
// is "no such VM"; more than one requires --profile to disambiguate, and
// lists the candidates by name.
func resolveShellProvider(store *profiles.Store, reg registryOwnership, name, profileFlag string) (provider.Provider, error) {
	if profileFlag != "" {
		p, ok := store.GetByName(profileFlag)
		if !ok {
			return nil, fmt.Errorf("unknown connection profile %q", profileFlag)
		}
		if !p.Enabled {
			return nil, fmt.Errorf("profile %q is disabled", p.Name)
		}
		prov, _, err := providerForProfile(p)
		return prov, err
	}

	enabled := make([]profiles.Profile, 0, len(store.List()))
	for _, p := range store.List() {
		if p.Enabled {
			enabled = append(enabled, p)
		}
	}

	var target profiles.Profile
	switch {
	case len(enabled) == 0:
		return nil, fmt.Errorf("no enabled connection profile found (not even %q)", profiles.LocalProfileID)
	case len(enabled) == 1:
		// Only one profile is enabled: use it directly, exactly as `sand shell`
		// always has, regardless of whether NAME is a sand-managed VM — there is
		// no other profile it could possibly be on.
		target = enabled[0]
	default:
		// Registry FIRST — a pure in-memory lookup. The common case (NAME is a VM
		// in this controller's managed index) resolves with ZERO network I/O, so
		// `sand shell localvm` never hangs behind an unreachable/slow OTHER profile.
		// Only on a registry miss do we pay the network: the authoritative
		// provenance marker per profile, then a List probe for an unmanaged VM.
		var owners []profiles.Profile
		for _, p := range enabled {
			if reg.IsManagedInScope(name, scopeForProfile(p)) {
				owners = append(owners, p)
			}
		}
		if len(owners) == 0 {
			// Registry cache miss: consult each candidate profile's provenance
			// marker (a network round trip per profile).
			owners = probeProvenanceOwners(enabled, name)
		}
		if len(owners) == 0 {
			// Neither the registry nor a marker found an owner; only now fall back
			// to the (network-round-tripping) List probe.
			owners = probeUnmanagedOwners(enabled, name)
		}
		switch len(owners) {
		case 0:
			return nil, fmt.Errorf("no such VM %q (run 'sand' to list instances, or pass --profile if it is on a specific connection profile)", name)
		case 1:
			target = owners[0]
		default:
			names := make([]string, len(owners))
			for i, o := range owners {
				names[i] = o.Name
			}
			return nil, fmt.Errorf("%q exists under more than one connection profile (%s) — pass --profile to pick one", name, strings.Join(names, ", "))
		}
	}

	prov, _, err := providerForProfile(target)
	return prov, err
}

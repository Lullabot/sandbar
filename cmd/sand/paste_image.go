package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/paste"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// vmLookup is the narrow surface pasteImageTarget needs from a backend:
// looking up ONE instance by name. Narrower than shell.go's vmGetter (which
// also needs AttachArgv for the interactive attach argv this command never
// builds) so a stub double need not implement a method it never calls.
// provider.Provider satisfies this structurally, same as it satisfies
// vmGetter.
type vmLookup interface {
	Get(name string) (vm.VM, error)
}

// runPasteImage implements the `sand paste-image <name>` subcommand: it reads
// the host clipboard image and stages it on the named VM's guest clipboard,
// mirroring runShell's exact argument contract (one explicit VM name +
// --profile) and Running guard, then delegates the clipboard read + guest
// write to internal/paste's PasteImage core. This command only resolves the
// target and renders the result.
func runPasteImage(args []string) error {
	fs := flag.NewFlagSet("paste-image", flag.ContinueOnError)
	profileFlag := fs.String("profile", "", "Connection profile NAME lives on (only needed when NAME exists under more than one enabled profile)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: sand paste-image NAME [--profile <name>]

Read the host clipboard image and stage it on NAME's guest clipboard at
<guest-home>/.sand/clip/latest.png, ready for Ctrl-V inside the guest.

The named VM must already exist and be running (see 'sand' to list instances,
or 'sand create' to make one). If NAME is managed under more than one
connection profile, --profile picks which one to target.

If the host clipboard holds no image, nothing is staged and the command
exits non-zero.
`)
	}
	// --profile may appear before or after NAME; reorder so all flags precede
	// the positional argument, which is what flag.FlagSet.Parse requires (it
	// stops parsing flags at the first non-flag token). Reuses shell.go's
	// reorderShellFlags: it only recognises -h/--help and --profile, the same
	// flag set this command defines.
	if err := fs.Parse(reorderShellFlags(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil // usage was already printed; -h/--help is not a failure
		}
		return err // flag package already printed usage
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("sand paste-image: need exactly one VM name")
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
		return fmt.Errorf("sand paste-image: %w", err)
	}
	if err := p.Preflight(); err != nil {
		return err
	}

	target, err := pasteImageTarget(p, name)
	if err != nil {
		return err
	}

	result, err := paste.PasteImage(context.Background(), p, target)
	if err != nil {
		return fmt.Errorf("sand paste-image: %w", err)
	}

	switch result.Status {
	case paste.Staged:
		fmt.Printf("staged image on %s — press Ctrl-V in the guest\n", result.VMName)
		return nil
	case paste.NoImage:
		// A non-zero exit (rather than a documented-zero "nothing to do") so a
		// caller — human or scripted — can tell "nothing was staged" from
		// "staged successfully" without parsing stdout.
		fmt.Fprintln(os.Stderr, "no image on clipboard")
		os.Exit(1)
	}
	return nil
}

// pasteImageTarget looks up name in the live instance list and returns a
// clear, actionable error for an unknown instance or one that is not
// running — the same guard shellAttachArgv applies for `sand shell`, since
// internal/paste's PasteImage deliberately does not check Running itself
// (see that package's doc comment). Factored out from runPasteImage so it can
// be tested with a stub vmLookup instead of a real provider.
func pasteImageTarget(l vmLookup, name string) (vm.VM, error) {
	found, err := l.Get(name)
	if err != nil {
		if errors.Is(err, lima.ErrNoSuchInstance) {
			return vm.VM{}, fmt.Errorf("sand paste-image: no VM named %q (run 'sand' to list instances)", name)
		}
		return vm.VM{}, fmt.Errorf("sand paste-image: %w", err)
	}
	if found.Status != limaRunning {
		return vm.VM{}, fmt.Errorf("sand paste-image: VM %q is not running (status: %s); start it first", name, found.Status)
	}
	return found, nil
}

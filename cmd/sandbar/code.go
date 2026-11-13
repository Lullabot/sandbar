package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/sshconfig"
	"github.com/lullabot/sandbar/internal/vm"
	"github.com/lullabot/sandbar/internal/vscode"
)

// runCode implements `sandbar code NAME [--profile NAME]`, the CLI twin of the
// board's `c` verb: it makes NAME reachable as a named ssh host and opens VS
// Code's Remote-SSH window on it. The work itself is internal/vscode.Prepare,
// shared with the TUI; this command adds only what a terminal front end owns —
// profile resolution, the y/N consent for ~/.ssh/config, and the messages.
func runCode(args []string) error {
	fs := flag.NewFlagSet("code", flag.ContinueOnError)
	profileFlag := fs.String("profile", "", "Connection profile NAME lives on (only needed when NAME exists under more than one enabled profile)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: sandbar code NAME [--profile <name>]

Open NAME in VS Code over Remote-SSH, on the VM's project checkout (or its
home directory when it was created without a repository).

sandbar writes an ssh host entry for NAME to %s
and runs: code --remote ssh-remote+<alias> <folder>
If VS Code's "code" command is not found, it prints the host alias to choose
in VS Code's "Remote-SSH: Connect to Host…" instead.

ssh reads that file only once ~/.ssh/config includes it. The first time, on a
terminal, sandbar asks before adding this line to the top of ~/.ssh/config:
  %s
It never edits a symlinked or unwritable ~/.ssh/config, or one it could not
ask about (stdin is not a terminal); it prints the line to add instead.

The named VM must already exist and be running. If NAME is managed under more
than one connection profile, --profile picks which one to open.
`, sshconfig.ManagedPath(), sshconfig.IncludeLine())
	}
	// Same argument contract as `sandbar shell`, so reorderShellFlags accepts
	// --profile on either side of NAME. It also recognises --cc, which this
	// flag set does not define: fs.Parse rejects it with the normal error.
	if err := fs.Parse(reorderShellFlags(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("sandbar code: need exactly one VM name")
	}
	name := fs.Arg(0)

	// Read-only loads: this command records nothing in either store, and a
	// command that only opens an editor should not migrate or rewrite them.
	store, err := profiles.LoadReadOnly()
	if err != nil {
		return fmt.Errorf("sandbar code: %w", err)
	}
	reg, loadErr := registry.LoadReadOnly()
	if reg == nil {
		reg = registry.NewEmpty()
	}
	if loadErr != nil {
		fmt.Fprintln(os.Stderr, "warning:", loadErr)
	}
	target, err := resolveVMProfile(store, reg, name, *profileFlag)
	if err != nil {
		return fmt.Errorf("sandbar code: %w", err)
	}
	p, scope, err := providerForProfile(target)
	if err != nil {
		return fmt.Errorf("sandbar code: %w", err)
	}
	if err := p.Preflight(); err != nil {
		return err
	}
	return doCode(context.Background(), p, reg, scope, name, os.Stdin, isTerminal(os.Stdin), os.Stdout)
}

// configLookup is the registry surface recordedCloneURL needs, satisfied by
// *registry.Registry.
type configLookup interface {
	ConfigInScope(name string, scope registry.Scope) (vm.CreateConfig, bool)
}

// doCode is runCode after resolution, taking its terminal as parameters so
// the consent branching is testable with canned input. interactive is whether
// in is a real terminal: piped input is never taken as consent to edit
// ~/.ssh/config, because a script answering "y" to every prompt did not read
// this one.
func doCode(ctx context.Context, p provider.Provider, reg configLookup, scope registry.Scope, name string, in io.Reader, interactive bool, out io.Writer) error {
	v, err := p.Get(name)
	if err != nil {
		if errors.Is(err, vm.ErrNotFound) {
			return fmt.Errorf("sandbar code: no VM named %q (run 'sandbar' to list instances)", name)
		}
		return fmt.Errorf("sandbar code: %w", err)
	}
	// Refused before anything is written: a stopped VM's entry could not be
	// resolved (Proxmox has no address for it) and VS Code would only time out.
	if v.Status != limaRunning {
		return fmt.Errorf("sandbar code: VM %q is not running (status: %s); start it first", name, v.Status)
	}

	ensureInclude(in, interactive, out)

	res, err := vscode.Prepare(ctx, p, v, recordedCloneURL(ctx, p, reg, scope, name))
	if err != nil {
		return fmt.Errorf("sandbar code: %w", err)
	}
	if res.AuthorizedKey != "" {
		fmt.Fprintf(out, "Authorized %s in the guest.\n", res.AuthorizedKey)
	}
	if res.LaunchArgv == nil {
		fmt.Fprintf(out, "VS Code's \"code\" command was not found. In VS Code, run \"Remote-SSH: Connect to Host…\" and choose %s.\n", res.Alias)
		return nil
	}
	if err := vscode.Launch(res.LaunchArgv); err != nil {
		return fmt.Errorf("sandbar code: start %s: %w", res.CodePath, err)
	}
	fmt.Fprintf(out, "Opening VS Code on %s (%s)\n", res.Alias, res.Folder)
	return nil
}

// ensureInclude gets the managed file included from ~/.ssh/config, with
// consent, or tells the user exactly what to add. It never fails the command:
// without the line the alias does not resolve yet, but the entry is still
// written, so adding the line later is all it takes.
func ensureInclude(in io.Reader, interactive bool, out io.Writer) {
	path := sshconfig.UserConfigPath()
	line := sshconfig.IncludeLine()
	manual := func(why string) {
		fmt.Fprintf(out, "%s; ssh will not find the VM's host alias until you add this line at the top of %s:\n  %s\n", why, path, line)
	}

	st, err := sshconfig.IncludeStatus()
	if err != nil {
		manual(fmt.Sprintf("Could not read %s (%v)", path, err))
		return
	}
	switch vscode.ClassifyInclude(st) {
	case vscode.IncludePresent:
		return
	case vscode.IncludeManual:
		if st.Symlink {
			manual(fmt.Sprintf("Not editing %s because it is a symlink", path))
		} else {
			manual(fmt.Sprintf("Not editing %s because it is not writable", path))
		}
		return
	}

	if !interactive {
		manual(fmt.Sprintf("Not editing %s without asking (stdin is not a terminal)", path))
		return
	}
	fmt.Fprintf(out, "Add %s to ~/.ssh/config? [y/N] ", line)
	reply, _ := bufio.NewReader(in).ReadString('\n')
	reply = strings.ToLower(strings.TrimSpace(reply))
	if reply != "y" && reply != "yes" {
		manual("Left ~/.ssh/config unchanged")
		return
	}
	if err := sshconfig.AddInclude(); err != nil {
		var me *sshconfig.ManualIncludeError
		if errors.As(err, &me) {
			manual(fmt.Sprintf("Could not edit %s (%s)", me.Path, me.Reason))
			return
		}
		manual(fmt.Sprintf("Could not edit %s (%v)", path, err))
		return
	}
	fmt.Fprintf(out, "Added the line to %s.\n", path)
}

// recordedCloneURL is the project the VM was created for, read the way reset
// reads a VM's recorded config: the provenance marker first (it lives with
// the VM, so it is right even for a VM another controller created), then the
// managed index. "" — open the home directory — when neither knows, which is
// also the honest answer for a VM created without a repository.
func recordedCloneURL(ctx context.Context, p provider.Provider, reg configLookup, scope registry.Scope, name string) string {
	if pv, ok := p.(provider.Provenancer); ok {
		if m, found, err := pv.ProvenanceOf(ctx, name); err == nil && found && m.Config.CloneURL != "" {
			return m.Config.CloneURL
		}
	}
	if cfg, ok := reg.ConfigInScope(name, scope); ok {
		return cfg.CloneURL
	}
	return ""
}

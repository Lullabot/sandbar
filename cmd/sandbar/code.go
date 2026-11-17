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

	"github.com/lullabot/sandbar/internal/manage"
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
	return doCode(context.Background(), p, reg, scope, target, name, os.Stdin, isTerminal(os.Stdin), os.Stdout)
}

// doCode is runCode after resolution, taking its terminal as parameters so
// the consent branching is testable with canned input. prof is the resolved
// profile, which names the VM's ssh host alias. interactive is
// whether in is a real terminal: piped input is never taken as consent to
// edit ~/.ssh/config, because a script answering "y" to every prompt did not
// read this one.
func doCode(ctx context.Context, p provider.Provider, reg manage.ConfigLookup, scope registry.Scope, prof profiles.Profile, name string, in io.Reader, interactive bool, out io.Writer) error {
	// Refused before anything is written: a stopped VM's entry could not be
	// resolved (Proxmox has no address for it) and VS Code would only time out.
	v, err := requireRunningVM(p, "code", name)
	if err != nil {
		return err
	}

	ensureInclude(ctx, in, interactive, out)

	lines, _, err := vscode.Open(ctx, p, prof, v, recordedCloneURL(ctx, p, reg, scope, name))
	for _, l := range lines {
		fmt.Fprintln(out, l)
	}
	if err != nil {
		return fmt.Errorf("sandbar code: %w", err)
	}
	return nil
}

// ensureInclude gets the managed file included from ~/.ssh/config, with
// consent, or tells the user exactly what to add. It never fails the command:
// without the line the alias does not resolve yet, but the entry is still
// written, so adding the line later is all it takes.
func ensureInclude(ctx context.Context, in io.Reader, interactive bool, out io.Writer) {
	path := sshconfig.UserConfigPath()
	line := sshconfig.IncludeLine()
	manual := func(why string) {
		fmt.Fprintln(out, vscode.ManualIncludeMessage(why, "\n  "))
	}

	action, why := vscode.CheckInclude(ctx)
	switch action {
	case vscode.IncludePresent:
		return
	case vscode.IncludeManual:
		manual(why)
		return
	}
	if why != "" {
		// An Include line is there but ssh does not apply it; say so, or the
		// offer to add one would look like sandbar had not seen it.
		fmt.Fprintln(out, why+".")
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
		manual(vscode.AddIncludeFailure(err))
		return
	}
	fmt.Fprintf(out, "Added the line to %s.\n", path)
}

// recordedCloneURL reads the VM's project through manage.RecordedCloneURL,
// the board's rule too, with the provider's own marker read as the first
// source. A marker that cannot be read falls through to the managed index.
func recordedCloneURL(ctx context.Context, p provider.Provider, reg manage.ConfigLookup, scope registry.Scope, name string) string {
	var marker func() (vm.CreateConfig, bool)
	if pv, ok := p.(provider.Provenancer); ok {
		marker = func() (vm.CreateConfig, bool) {
			m, found, err := pv.ProvenanceOf(ctx, name)
			return m.Config, err == nil && found
		}
	}
	return manage.RecordedCloneURL(marker, reg, scope, name)
}

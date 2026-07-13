package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/vm"
)

// limaRunning is the status string limactl reports for a running instance
// (mirrors internal/ui's identically-named, unexported constant — see
// limaBaseDeleter's Status doc in create.go for the same "Running" contract).
const limaRunning = "Running"

// vmLister is the narrow lima.Client surface shellAttachArgv needs: listing
// instances so status and Dir can be resolved together (Dir only comes back
// from List, not Status). An interface so the decision logic can be unit
// tested with a stub instead of a real lima.Client, which would need a real
// limactl (AGENTS.md, hard rule).
type vmLister interface {
	List() ([]vm.VM, error)
}

// runShell implements the `sand shell <name>` subcommand: it resolves the named
// VM's status and instance dir together, refuses cleanly when the VM is unknown
// or not running, and otherwise execs the attach argv built by lima.AttachArgv —
// the one place in sand that knows tmux exists, and which the TUI's `S` verb
// builds on too, so the two entrypoints cannot drift. stdio is inherited because
// a tmux client needs the real terminal.
//
// The TUI withholds `S` from a VM that is not running (commandreg.go's enabledFor
// gate, which also hides the verb from the footer). A CLI has no footer to
// withhold, so the same rule has to be stated in words instead — see
// shellAttachArgv.
func runShell(args []string) error {
	fs := flag.NewFlagSet("shell", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: sand shell NAME

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
or 'sand create' to make one).
`)
	}
	if err := fs.Parse(args); err != nil {
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

	cli := lima.New(lima.NewExecRunner())
	if err := cli.Preflight(); err != nil {
		return err
	}

	argv, err := shellAttachArgv(cli, name)
	if err != nil {
		return err
	}

	// The interactive attach deliberately bypasses cli.Runner (which captures
	// output for the typed lifecycle calls above) because a tmux client needs the
	// real terminal, not a pipe: hence a bare exec.Command with inherited stdio
	// rather than anything that runs through lima.Runner.
	c := exec.Command(argv[0], argv[1:]...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Propagate the child's real exit code rather than collapsing every
			// failure to main.go's blanket os.Exit(1).
			os.Exit(exitErr.ExitCode())
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
// hand-off above needs a real TTY and a real VM (task 6's job) and is
// deliberately left untested here.
func shellAttachArgv(l vmLister, name string) ([]string, error) {
	vms, err := l.List()
	if err != nil {
		return nil, fmt.Errorf("sand shell: %w", err)
	}

	var found *vm.VM
	for i := range vms {
		if vms[i].Name == name {
			found = &vms[i]
			break
		}
	}
	if found == nil {
		return nil, fmt.Errorf("sand shell: no VM named %q (run 'sand' to list instances)", name)
	}
	if found.Status != limaRunning {
		return nil, fmt.Errorf("sand shell: VM %q is not running (status: %s); start it first", name, found.Status)
	}

	return lima.AttachArgv(name, lima.GuestHome(found.Dir)), nil
}

// Package vscode is the one implementation of "open this VM in VS Code's
// Remote-SSH", shared by the board's `c` verb and `sandbar code` so the two
// front ends cannot drift. It writes the VM's host entry to sandbar's managed
// ssh config, works out which guest folder to open, and finds and starts the
// `code` command. Asking the user whether ~/.ssh/config may be edited is left
// to each front end (a terminal prompt vs a TUI overlay); ClassifyInclude
// gives them the same branching to ask from.
package vscode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"

	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/sshconfig"
	"github.com/lullabot/sandbar/internal/vm"
)

// Guest is the provider surface Prepare needs. provider.Provider satisfies
// it; naming only these methods keeps the shared operation's dependencies
// visible and lets a test supply exactly them.
type Guest interface {
	EditorSSH(ctx context.Context, v vm.VM) (sshconfig.Entry, error)
	GuestHome(v vm.VM) string
	GuestUser(v vm.VM) string
}

// Result describes what Prepare did and what the caller should do next.
type Result struct {
	// Alias is the ssh host name the VM now resolves as.
	Alias string
	// Folder is the absolute guest path to open: the project checkout when
	// the VM was created from a clone URL, else the guest home.
	Folder string
	// CodePath is the `code` command found on this workstation, "" when
	// none was, in which case the caller tells the user to pick Alias in
	// "Remote-SSH: Connect to Host…" instead.
	CodePath string
	// LaunchArgv is what Launch runs; nil when CodePath is "".
	LaunchArgv []string
	// AuthorizedKey is the workstation public key the provider authorized
	// in the guest (remote Lima), so the user can be told which key now
	// opens it; "" when none was.
	AuthorizedKey string
}

// The messages below are shared by the board and `sandbar code` so the two
// front ends tell the user the same thing; each caller only decides how to
// lay them out (a log line or a terminal line).

// AuthorizedMessage names the workstation key that now opens the guest, or ""
// when the provider authorized none.
func (r Result) AuthorizedMessage() string {
	if r.AuthorizedKey == "" {
		return ""
	}
	return fmt.Sprintf("Authorized %s in the guest.", r.AuthorizedKey)
}

// NotFoundMessage is what to do when no `code` command was found: the entry
// is written, so VS Code's own host picker can open it.
func (r Result) NotFoundMessage() string {
	return fmt.Sprintf("VS Code's \"code\" command was not found. In VS Code, run \"Remote-SSH: Connect to Host…\" and choose %s.", r.Alias)
}

// OpeningMessage reports a successful launch.
func (r Result) OpeningMessage() string {
	return fmt.Sprintf("Opening VS Code on %s (%s)", r.Alias, r.Folder)
}

// ManualIncludeMessage explains, after why, that the user must add the
// Include line to ~/.ssh/config themselves. sep separates the explanation
// from the line, so the CLI can put the line on its own row and the board's
// one-line log can keep it inline.
func ManualIncludeMessage(why, sep string) string {
	return fmt.Sprintf("%s; ssh will not find the VM's host alias until you add this line at the top of %s:%s%s",
		why, sshconfig.UserConfigPath(), sep, sshconfig.IncludeLine())
}

// The three reasons below are the `why` each front end hands
// ManualIncludeMessage, kept here for the same reason as the messages above.

// UnreadableReason explains an sshconfig.IncludeStatus error.
func UnreadableReason(err error) string {
	return fmt.Sprintf("Could not read %s (%v)", sshconfig.UserConfigPath(), err)
}

// ManualReason explains why ClassifyInclude returned IncludeManual for st.
func ManualReason(st sshconfig.IncludeState) string {
	path := sshconfig.UserConfigPath()
	if st.Symlink {
		return fmt.Sprintf("Not editing %s because it is a symlink", path)
	}
	return fmt.Sprintf("Not editing %s because it is not writable", path)
}

// AddIncludeFailure explains an sshconfig.AddInclude error, using the
// ManualIncludeError's own path and reason when it is one.
func AddIncludeFailure(err error) string {
	var me *sshconfig.ManualIncludeError
	if errors.As(err, &me) {
		return fmt.Sprintf("Could not edit %s (%s)", me.Path, me.Reason)
	}
	return fmt.Sprintf("Could not edit %s (%v)", sshconfig.UserConfigPath(), err)
}

// IncludeAction is what a front end should do about the Include line in
// ~/.ssh/config before calling Prepare.
type IncludeAction int

const (
	// IncludePresent: nothing to do.
	IncludePresent IncludeAction = iota
	// IncludeAsk: sandbar may add it, but only after the user agrees.
	IncludeAsk
	// IncludeManual: sandbar will not edit the file (a symlink, typically
	// into a dotfiles repository, or not writable); show
	// sshconfig.IncludeLine() for the user to add by hand.
	IncludeManual
)

// ClassifyInclude maps ~/.ssh/config's state to the one decision both front
// ends make. A symlink is manual even when its target is writable, because
// AddInclude's atomic rewrite would replace the link with a regular file.
func ClassifyInclude(st sshconfig.IncludeState) IncludeAction {
	switch {
	case st.Present:
		return IncludePresent
	case st.Symlink || !st.Writable:
		return IncludeManual
	default:
		return IncludeAsk
	}
}

// macAppCode is where the VS Code app bundle ships its `code` shell command.
// macOS users often never run "Shell Command: Install 'code' command in
// PATH", so PATH alone would send most of them to the manual fallback.
const macAppCode = "/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code"

// Seams for FindCode, replaced in tests so the result never depends on
// whether VS Code is installed on the machine running them.
var (
	lookPath = exec.LookPath
	goos     = runtime.GOOS
	statFile = func(p string) error { _, err := os.Stat(p); return err }
)

// FindCode returns the path of VS Code's `code` command, or "". A var so a
// front end's tests can pin the answer without installing or hiding VS Code.
var FindCode = findCode

func findCode() string {
	if p, err := lookPath("code"); err == nil {
		return p
	}
	if goos == "darwin" && statFile(macAppCode) == nil {
		return macAppCode
	}
	return ""
}

// Prepare writes v's host entry to the managed ssh config and computes the
// folder and launch command. It does I/O (the provider's EditorSSH may
// resolve a guest address or authorize a key), so the TUI must run it in a
// command, not on the update goroutine. cloneURL is the VM's recorded
// project URL, "" for none.
//
// It does not touch ~/.ssh/config: whether that may be edited is the user's
// call, asked by the front end beforehand. Without the Include the entry is
// still written, so adding the line later makes the alias work at once.
func Prepare(ctx context.Context, g Guest, v vm.VM, cloneURL string) (Result, error) {
	entry, err := g.EditorSSH(ctx, v)
	if err != nil {
		return Result{}, err
	}
	if entry.Alias == "" {
		// A zero Entry would render nothing useful and leave the user with a
		// message naming an empty host.
		return Result{}, fmt.Errorf("no ssh host entry for %q", v.Name)
	}
	if err := sshconfig.Upsert(entry); err != nil {
		return Result{}, fmt.Errorf("write %s: %w", sshconfig.ManagedPath(), err)
	}

	folder, err := guestFolder(g, v, cloneURL)
	if err != nil {
		return Result{}, err
	}
	res := Result{
		Alias:         entry.Alias,
		Folder:        folder,
		CodePath:      FindCode(),
		AuthorizedKey: entry.AuthorizedKey,
	}
	if res.CodePath != "" {
		res.LaunchArgv = []string{res.CodePath, "--remote", "ssh-remote+" + res.Alias, folder}
	}
	return res, nil
}

// guestFolder mirrors the project role's checkout layout (~/<host>/<org>/<repo>)
// so VS Code opens straight onto the code the VM exists for. The /home/<user>
// fallback matches the TUI's transfer default for a backend that cannot read
// the home from its instance files.
func guestFolder(g Guest, v vm.VM, cloneURL string) (string, error) {
	home := g.GuestHome(v)
	if home == "" {
		user := g.GuestUser(v)
		if user == "" {
			return "", fmt.Errorf("could not resolve the guest home directory of %q", v.Name)
		}
		home = "/home/" + user
	}
	if cloneURL != "" {
		if rel, ok := provision.CheckoutRelDir(cloneURL); ok {
			return home + "/" + rel, nil
		}
	}
	return home, nil
}

// Launch starts argv detached: `code` hands the request to a running (or new)
// VS Code window and exits, so there is nothing to wait for, and it must not
// hold sandbar's terminal — the TUI owns it, and the CLI returns at once. A
// new session keeps a terminal hangup from reaching the editor. A var so tests
// in every front end can replace it and never open VS Code.
var Launch = func(argv []string) error {
	if len(argv) == 0 {
		return errors.New("vscode: empty launch command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	// nil stdio connects each stream to the null device.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap it in the background rather than Process.Release: Release does not
	// wait, so each launch from a long-running TUI would leave a zombie until
	// sandbar exits. The CLI exits first, and the child carries on regardless.
	go func() { _ = cmd.Wait() }()
	return nil
}

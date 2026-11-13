// Package vscode is the one implementation of "open this VM in VS Code's
// Remote-SSH", shared by the board's `c` verb and `sandbar code` so the two
// front ends cannot drift. It names the VM's ssh host (Alias), writes its
// entry to sandbar's managed ssh config, works out which guest folder to
// open, and finds and starts the `code` command. Asking the user whether
// ~/.ssh/config may be edited is left to each front end (a terminal prompt vs
// a TUI overlay); CheckInclude gives them the same branching to ask from, and
// Open runs the rest.
package vscode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"

	"github.com/lullabot/sandbar/internal/profiles"
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
	// List is the fresh listing Prepare prunes the profile's stale entries
	// against.
	List() ([]vm.VM, error)
}

// Result describes what Prepare did and what the caller should do next.
type Result struct {
	// Alias is the ssh host name the VM now resolves as.
	Alias string
	// Folder is the absolute guest path to open: the project checkout when
	// the VM was created from a clone URL, else the guest home.
	Folder string
	// LaunchArgv is what Launch runs, starting with the `code` command found
	// on this workstation; nil when none was, in which case the user is told
	// to pick Alias in "Remote-SSH: Connect to Host…" instead.
	LaunchArgv []string
	// AuthorizedKey is the workstation public key the provider authorized
	// in the guest (remote Lima), so the user can be told which key now
	// opens it; "" when none was.
	AuthorizedKey string
	// Pruned are the aliases of this profile's other entries that Prepare
	// removed because their VMs no longer exist.
	Pruned []string
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

// PrunedMessage reports the entries Prepare removed, or "" when none.
func (r Result) PrunedMessage() string {
	if len(r.Pruned) == 0 {
		return ""
	}
	return fmt.Sprintf("Removed ssh host entries for VMs that no longer exist: %s.", strings.Join(r.Pruned, ", "))
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

// AddIncludeFailure is the `why` ManualIncludeMessage takes after
// sshconfig.AddInclude failed with err.
func AddIncludeFailure(err error) string {
	return fmt.Sprintf("Could not edit %s (%v)", sshconfig.UserConfigPath(), err)
}

// IncludeAction is what a front end should do about the Include line in
// ~/.ssh/config before calling Open.
type IncludeAction int

const (
	// IncludePresent: nothing to do.
	IncludePresent IncludeAction = iota
	// IncludeAsk: sandbar may add it, but only after the user agrees.
	IncludeAsk
	// IncludeManual: sandbar will not edit the file (a symlink, typically
	// into a dotfiles repository, not writable, or unreadable); show
	// ManualIncludeMessage so the user can add the line by hand.
	IncludeManual
)

// CheckInclude inspects ~/.ssh/config and returns the one decision both front
// ends make. why is the explanation ManualIncludeMessage takes for
// IncludeManual; for IncludeAsk it is either "" or why the question is being
// asked although an Include line is already there, for the front end to show
// before asking.
//
// Whether the Include is in effect is ssh's answer, not the file's
// (sshconfig.IncludeApplied): an Include line written inside a Host or Match
// block is present but applies only to that block, and treating it as
// present would leave every VM alias unresolvable with nothing said about
// why. The probe runs ssh, so the TUI calls this in a command. Only when ssh
// cannot answer (it is not installed, or it rejects the user's config) does
// the textual check decide, as it did before the probe existed.
//
// A symlink is manual even when its target is writable, because AddInclude's
// atomic rewrite would replace the link with a regular file.
func CheckInclude(ctx context.Context) (action IncludeAction, why string) {
	path := sshconfig.UserConfigPath()
	st, err := sshconfig.IncludeStatus()
	if err != nil {
		return IncludeManual, fmt.Sprintf("Could not read %s (%v)", path, err)
	}
	misplaced := ""
	if st.Exists {
		applied, err := sshconfig.IncludeApplied(ctx)
		switch {
		case err == nil && applied:
			return IncludePresent, ""
		case err == nil && st.Present:
			misplaced = fmt.Sprintf("The Include line in %s is not applied (it may be inside a Host or Match block)", path)
		case err != nil && st.Present:
			return IncludePresent, ""
		}
	}
	notEditing := func(reason string) string {
		if misplaced != "" {
			return fmt.Sprintf("%s, and sandbar is not editing it because it is %s", misplaced, reason)
		}
		return fmt.Sprintf("Not editing %s because it is %s", path, reason)
	}
	switch {
	case st.Symlink:
		return IncludeManual, notEditing("a symlink")
	case !st.Writable:
		return IncludeManual, notEditing("not writable")
	default:
		return IncludeAsk, misplaced
	}
}

// Alias is the ssh host name a VM on profile p is reachable by once its entry
// is written. Providers describe how to reach a guest but do not name it; the
// name is chosen here so that writing an entry (Prepare) and removing one
// (the cleanup functions below) cannot disagree. A Local VM keeps Lima's own
// name, because its entry Includes Lima's per-instance ssh.config and only
// that file's Host line matches it. Every other profile's VMs are namespaced
// by the profile's immutable ID, since two profiles may each hold a VM of the
// same name; a rename therefore changes no alias.
func Alias(p profiles.Profile, vmName string) string {
	if p.Type == profiles.TypeLocal {
		return sshconfig.LimaAlias(vmName)
	}
	return sshconfig.ProfileAlias(p.ID, vmName)
}

// vmOfAlias is Alias in reverse: the VM name alias names under p, and whether
// it is in p's namespace at all. Local's namespace is every "lima-" block in
// the managed file, which is sound because only sandbar writes that file and
// it writes "lima-" blocks only for the Local profile. A non-local profile
// without an ID (never stored that way) owns nothing, rather than every
// "sandbar--" alias.
func vmOfAlias(p profiles.Profile, alias string) (string, bool) {
	var prefix string
	switch {
	case p.Type == profiles.TypeLocal:
		prefix = sshconfig.LimaAlias("")
	case p.ID != "":
		prefix = sshconfig.ProfileAliasPrefix(p.ID)
	default:
		return "", false
	}
	name, ok := strings.CutPrefix(alias, prefix)
	return name, ok && name != ""
}

// The cleanup functions below remove entries whose VMs are gone. It matters
// most for Proxmox: its entry pins a DHCP address with host-key checking off,
// and once the VM is gone that address may be handed to an unrelated machine
// that VS Code's recent list would then connect to without a warning. Each is
// one locked rewrite of the managed file, so the TUI runs them in a command.

// RemoveProfileEntries removes every entry profile p owns: the profile is
// being deleted, and a profile ID is never reused, so nothing would ever
// rewrite or remove them later.
func RemoveProfileEntries(p profiles.Profile) ([]string, error) {
	return sshconfig.RemoveWhere(func(alias string) bool {
		_, ok := vmOfAlias(p, alias)
		return ok
	})
}

// RemoveEntries removes the entries of the named VMs on profile p, which the
// caller knows are deleted.
func RemoveEntries(p profiles.Profile, names ...string) ([]string, error) {
	gone := make(map[string]bool, len(names))
	for _, n := range names {
		gone[n] = true
	}
	return sshconfig.RemoveWhere(func(alias string) bool {
		name, ok := vmOfAlias(p, alias)
		return ok && gone[name]
	})
}

// PruneEntries removes profile p's entries for VMs not in live, a listing of
// p's VMs the caller took. An entry written after that listing for a VM
// created after it would be removed too; it costs only a rewrite on the next
// open, which is why callers prune with a listing taken moments before.
func PruneEntries(p profiles.Profile, live []vm.VM) ([]string, error) {
	present := make(map[string]bool, len(live))
	for _, v := range live {
		present[v.Name] = true
	}
	return sshconfig.RemoveWhere(func(alias string) bool {
		name, ok := vmOfAlias(p, alias)
		return ok && !present[name]
	})
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

// Prepare writes v's host entry, under Alias(prof, v.Name), to the managed
// ssh config, prunes prof's entries for VMs that no longer exist, and
// computes the folder and launch command. It does I/O (the provider's
// EditorSSH may resolve a guest address or authorize a key, and the prune
// lists the profile's VMs), so the TUI must run it in a command, not on the
// update goroutine. cloneURL is the VM's recorded project URL, "" for none.
//
// It does not touch ~/.ssh/config: whether that may be edited is the user's
// call, asked by the front end beforehand. Without the Include the entry is
// still written, so adding the line later makes the alias work at once.
func Prepare(ctx context.Context, g Guest, prof profiles.Profile, v vm.VM, cloneURL string) (Result, error) {
	alias := Alias(prof, v.Name)
	entry, err := g.EditorSSH(ctx, v)
	if err != nil {
		return Result{}, err
	}
	if entry.Include == "" && entry.HostName == "" {
		// An entry with nothing to connect to would render a bare Host line
		// and leave the user with an alias that goes nowhere.
		return Result{}, fmt.Errorf("no ssh host entry for %q", v.Name)
	}
	entry.Alias = alias
	if err := sshconfig.Upsert(entry); err != nil {
		return Result{}, fmt.Errorf("write %s: %w", sshconfig.ManagedPath(), err)
	}

	folder, err := guestFolder(g, v, entry.GuestHome, cloneURL)
	if err != nil {
		return Result{}, err
	}
	res := Result{
		Alias:         alias,
		Folder:        folder,
		AuthorizedKey: entry.AuthorizedKey,
		Pruned:        pruneStale(g, prof, v.Name),
	}
	if code := FindCode(); code != "" {
		res.LaunchArgv = []string{code, "--remote", "ssh-remote+" + alias, folder}
	}
	return res, nil
}

// pruneStale is Prepare's housekeeping: entries of prof's VMs that have gone
// since they were opened (deleted from another machine, or outside sandbar)
// are removed whenever any VM of the profile is opened. It never fails the
// open. A listing that fails, including Lima's transient one while another
// instance is mid-clone (lima.ErrListRacedInstanceDir), skips the prune; so
// does one that omits the VM being opened, which EditorSSH has just reached,
// because such a listing cannot be trusted to name the others. The entry
// just written is kept regardless.
func pruneStale(g Guest, prof profiles.Profile, opened string) []string {
	live, err := g.List()
	if err != nil {
		return nil
	}
	found := false
	for _, v := range live {
		found = found || v.Name == opened
	}
	if !found {
		return nil
	}
	pruned, err := PruneEntries(prof, live)
	if err != nil {
		return nil
	}
	return pruned
}

// Open is the whole operation both front ends run once the Include question
// is settled: Prepare, then start VS Code or, with no `code` command, tell the
// user which host to pick in VS Code instead. lines are the messages to show,
// in order, the last one being the outcome; launched reports whether VS Code
// was started, for a front end that words that line differently. On error,
// lines still holds anything already worth reporting, such as a key
// authorized before the launch failed.
func Open(ctx context.Context, g Guest, prof profiles.Profile, v vm.VM, cloneURL string) (lines []string, launched bool, err error) {
	res, err := Prepare(ctx, g, prof, v, cloneURL)
	if err != nil {
		return nil, false, err
	}
	for _, msg := range []string{res.AuthorizedMessage(), res.PrunedMessage()} {
		if msg != "" {
			lines = append(lines, msg)
		}
	}
	if len(res.LaunchArgv) == 0 {
		return append(lines, res.NotFoundMessage()), false, nil
	}
	if err := Launch(res.LaunchArgv); err != nil {
		return lines, false, fmt.Errorf("start %s: %w", res.LaunchArgv[0], err)
	}
	return append(lines, res.OpeningMessage()), true, nil
}

// guestFolder mirrors the project role's checkout layout (~/<host>/<org>/<repo>)
// so VS Code opens straight onto the code the VM exists for. home is the
// guest home the provider already reported while building the entry, if any;
// otherwise it is read from the backend. The /home/<user> fallback matches
// the TUI's transfer default for a backend that cannot read the home from its
// instance files.
func guestFolder(g Guest, v vm.VM, home, cloneURL string) (string, error) {
	if home == "" {
		home = g.GuestHome(v)
	}
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

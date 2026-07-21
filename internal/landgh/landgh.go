// Package landgh is sandbar's host-side "land" GitHub actions adapter.
//
// # What this is
//
// Every function here runs on the WORKSTATION running `sand` — the machine a
// human typed `sand` on — never on a VM's connection-profile host and never
// inside the guest. It needs only (org/repo, branch), which the checkout
// sweep (a sibling package) already extracts from the guest read-only. This
// package never touches a VM: it shells out to the user's own workstation
// `gh` and to their OS browser opener, nothing else.
//
// # Why this exists
//
// land's whole design turns on a credential split: the guest's push token is
// deliberately least-privilege and often lacks `pull_requests: write`; the
// workstation's own `gh auth` is the user's full-scoped credential. Opening a
// PR is a metadata call against a branch that is already on GitHub — no
// repository code ever reaches the host through this package. That is the
// property that lets land exist at all without reintroducing the code-on-host
// hazard the no-host-mount design avoids.
//
// # The injection-safety invariant
//
// Every gh invocation goes through Runner.Output, which is always
// exec.CommandContext(ctx, "gh", args...) — args is a []string, never a
// shell string. A branch name or org/repo containing shell metacharacters
// (";", "`", "$(...)") becomes exactly one argv element; there is no shell to
// interpret it, so it can only ever become PR text, never a command. No
// function in this package builds a "sh -c ..." string.
//
// # Injectability
//
// Client.run (a Runner), Client.open (an Opener), and Client.lookPath are all
// struct fields set by New() to the real implementations, but swappable in
// tests to a fake that records argv and returns canned output — no test in
// this package spawns a real gh or a real browser.
package landgh

import (
	"context"
	"os/exec"
)

// Runner executes the gh CLI. It is abstracted behind an interface so tests
// never spawn a real gh binary: production code uses execRunner, tests fake
// it and assert on the argv they recorded.
type Runner interface {
	// Output runs `gh args...` and returns its stdout. args are passed as an
	// argument vector (never joined into a shell string), so any single
	// element — including one containing shell metacharacters — reaches gh
	// as exactly one argument.
	Output(ctx context.Context, args ...string) ([]byte, error)
}

// Opener opens target (a URL) in the user's default browser. Production is
// osOpen, which picks xdg-open/open/start by runtime.GOOS; tests fake it to
// record the target without launching anything.
type Opener func(ctx context.Context, target string) error

// Client is the host gh actions adapter: PR-state lookup, one-shot draft PR
// creation, gh-availability detection, and gh-free browser URL helpers. All
// of it runs workstation-local via os/exec — see the package doc.
type Client struct {
	run      Runner
	open     Opener
	lookPath func(file string) (string, error)

	// ghBinary is the executable Availability should look for on PATH: "gh"
	// normally, or "op" when the 1Password plugin is what carries the token
	// (opplugin.go). Without this the probe would hunt for a bare gh that a
	// plugin user may not even have, and report "not installed" for a setup
	// that works. Empty means "gh", so a zero-value Client (every test that
	// builds one by hand) behaves exactly as it did before.
	ghBinary string
}

// New returns a Client backed by the real gh binary on PATH and the host
// OS's browser opener.
func New() *Client {
	// Resolved once per Client: whether this user's gh token lives behind the
	// 1Password shell plugin cannot change mid-session without them editing
	// their shell config. See opplugin.go.
	cmd := resolveGhCommand()
	return &Client{
		run:      execRunner{cmd: cmd},
		open:     osOpen,
		lookPath: exec.LookPath,
		ghBinary: cmd[0],
	}
}

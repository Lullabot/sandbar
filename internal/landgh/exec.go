package landgh

import (
	"context"
	"os/exec"
	"runtime"
)

// execLookPath is exec.LookPath, named so opplugin.go can inject a fake.
var execLookPath = exec.LookPath

// execRunner is the real Runner: it runs the gh binary on PATH — or, when the
// 1Password gh shell plugin holds this user's token, `op plugin run -- gh`
// (see opplugin.go). cmd carries whichever prefix was resolved when the Client
// was built; either way this is still a plain argv exec with no shell.
//
// The child gets NO stdin (exec leaves it as /dev/null) and its stdout is
// captured, never inherited, so it cannot draw on — or block against — the
// terminal a full-screen TUI is holding. That matters most on the op path,
// where a credential prompt would otherwise have somewhere to appear.
type execRunner struct{ cmd []string }

func (r execRunner) Output(ctx context.Context, args ...string) ([]byte, error) {
	argv := r.cmd
	if len(argv) == 0 {
		argv = []string{"gh"}
	}
	full := append(append([]string{}, argv[1:]...), args...)
	return exec.CommandContext(ctx, argv[0], full...).Output()
}

// openerCommand picks the OS-appropriate browser-open command for goos. It is
// a pure function (no exec) so GOOS selection is tested directly without
// spawning anything.
func openerCommand(goos, target string) (name string, args []string) {
	switch goos {
	case "darwin":
		return "open", []string{target}
	case "windows":
		// The empty argument is the window-title placeholder `start` expects
		// before the URL; without it, `start` treats a quoted URL as the title.
		return "cmd", []string{"/c", "start", "", target}
	default:
		return "xdg-open", []string{target}
	}
}

// osOpen is the real Opener: it runs the OS-appropriate browser-open command
// for runtime.GOOS. Like execRunner, it is a thin wrapper over openerCommand,
// which carries the tested logic.
func osOpen(ctx context.Context, target string) error {
	name, args := openerCommand(runtime.GOOS, target)
	return exec.CommandContext(ctx, name, args...).Run()
}

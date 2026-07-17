package landgh

import (
	"context"
	"os/exec"
	"runtime"
)

// execRunner is the real Runner: it shells out to the gh binary on PATH.
// This is the thin, deliberately untested-beyond-fakes wrapper the package
// doc describes — every behavioral test fakes Runner instead.
type execRunner struct{}

func (execRunner) Output(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "gh", args...).Output()
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

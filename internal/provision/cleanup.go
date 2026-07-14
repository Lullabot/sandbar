package provision

import (
	"io"
	"os"
	"path/filepath"
)

// cleanup.go removes an instance THIS RUN created when the run does not finish
// creating it — a ^C during the clone, a failed `limactl start`, a cancelled base
// build. Without it the user is left with a half-written instance directory, and
// that is not merely untidy: a directory holding a disk and a cidata.iso but no
// lima.yaml makes `limactl list` FATAL —
//
//	fatal: unable to load instance web: open ~/.lima/web/lima.yaml: no such file
//
// — so every later list fails, the board cannot render, and sand is wedged by a
// VM that was never created. The user's only way out is to know to `rm -rf` a
// directory they have never heard of. The run that made the mess cleans it up.
//
// WHAT IS NOT CLEANED UP: a VM whose PLAYBOOK failed or was cancelled. That one
// booted, its lima.yaml is valid, `limactl list` is happy, and its log is
// retained — it is inspectable, and inspecting it is the point of a retained
// failed run. Only an instance that never finished being CREATED is removed:
// only that one is unusable, and only that one wedges the tool.

// cleanupInstance removes an instance sand created but did not finish creating.
// Best-effort by design: it runs on a path that is ALREADY failing, so it reports
// what it did and never replaces the error that brought us here.
//
// It asks limactl first and falls back to removing the instance directory
// outright — because the case that hurts most is exactly the one limactl cannot
// handle: a half-written directory it refuses to LOAD, and one it will not load
// is one it will never delete, while its mere presence is what makes every later
// `limactl list` fatal.
//
// Client.Delete runs on context.Background (lima.Client.run), so this still works
// on the path that brings us here most often: a context the user just cancelled.
func (p *Provisioner) cleanupInstance(name string, out io.Writer) {
	dir := instanceDir(name)
	if dir == "" {
		return
	}
	if _, err := os.Stat(dir); err != nil {
		return // nothing was written; nothing to clean up
	}

	step(out, "Cleaning up the partially created VM %q…", name)

	if err := p.Lima.Delete(name, true); err == nil {
		if _, err := os.Stat(dir); err != nil {
			return // limactl took it
		}
	}

	if err := os.RemoveAll(dir); err != nil {
		step(out, "Could not remove %s: %v — remove it by hand, or `limactl list` will keep failing.", dir, err)
		return
	}
	step(out, "Removed %s.", dir)
}

// instanceDir is a Lima instance's own directory under the Lima home. "" when the
// name is empty or the Lima home cannot be determined, which the caller reads as
// "nothing to clean up".
func instanceDir(name string) string {
	home := limaHome()
	if name == "" || home == "" {
		return ""
	}
	return filepath.Join(home, name)
}

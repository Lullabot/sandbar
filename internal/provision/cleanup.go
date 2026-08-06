package provision

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lullabot/sandbar/internal/lima"
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
//
// THE EVIDENCE OUTLIVES THE DIRECTORY. Removing the directory used to remove the
// only account of why the create failed with it. Lima's own fatal line points at
// a file inside it —
//
//	FATA[0031] exiting, status={Running:false Degraded:false ...}
//	  (hint: see "~/.lima/web/ha.stderr.log")
//
// — and by the time the user reads that hint the file is gone, so the message
// survives and the evidence does not. preserveFailureLogs copies those logs to a
// temporary directory first and prints where they went, which is the difference
// between reading a log and reproducing a failure.

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
	hf := p.hostFiles()
	dir := instanceDir(hf, name)
	if dir == "" {
		return
	}
	if _, err := hf.Stat(dir); err != nil {
		return // nothing was written; nothing to clean up
	}

	step(out, "Cleaning up the partially created VM %q…", name)

	// BEFORE anything removes the directory — including the limactl delete just
	// below, which takes it with the instance when it succeeds.
	preserveFailureLogs(hf, dir, name, out)

	if err := p.Lima.Delete(name, true); err == nil {
		if _, err := hf.Stat(dir); err != nil {
			return // limactl took it
		}
	}

	if err := hf.RemoveAll(dir); err != nil {
		step(out, "Could not remove %s: %v — remove it by hand, or `limactl list` will keep failing.", dir, err)
		return
	}
	step(out, "Removed %s.", dir)
}

// failureLogNames are the files Lima writes into an instance directory that say
// why it would not come up: the hostagent's own streams, and the guest's two
// consoles (serialv.log is the virtio console; serial.log the firmware one, which
// is where a VM that never reaches a kernel — bad firmware, no OVMF — reports
// it). Everything else in there is disk images, a config, and sockets: not
// evidence, and in the disks' case far too large to copy.
//
// A name absent from the directory is skipped, so this list is deliberately wider
// than any single Lima version writes.
var failureLogNames = []string{
	"ha.stderr.log",
	"ha.stdout.log",
	"serialv.log",
	"serial.log",
}

// maxPreservedLogBytes caps what is kept PER FILE. A VM stuck in a boot loop can
// spin megabytes into serial.log, and none of it diagnoses better than its tail —
// which is also the end nearest the failure. Truncation is announced in the copy
// itself (see tailBytes) rather than silently.
const maxPreservedLogBytes = 1 << 20 // 1 MiB

// preserveFailureLogs copies an about-to-be-deleted instance's logs into a fresh
// temporary directory and reports where they landed.
//
// The copy is written to THIS machine with os.*, not through hf, while the
// originals are READ through hf: for remote Lima the logs live on the remote host
// but the person reading the error message is sitting at this one, and a path
// they cannot open is barely better than no path at all. (For local Lima the two
// hosts are the same machine and the distinction does not arise.)
//
// Best-effort throughout, and for the same reason cleanupInstance is: this runs
// on a path that is already failing, and a build must not fail differently
// because its post-mortem could not be saved. A log that cannot be read is
// skipped — the overwhelmingly common case is a cleanup so early that Lima never
// wrote one — and only a directory that actually received something is announced.
func preserveFailureLogs(hf lima.HostFiles, dir, name string, out io.Writer) {
	var dest string
	var saved []string
	for _, log := range failureLogNames {
		data, err := hf.ReadFile(filepath.Join(dir, log))
		if err != nil || len(data) == 0 {
			continue
		}
		if dest == "" {
			// Created lazily: nothing readable means no empty directory left behind.
			d, err := os.MkdirTemp("", "sand-failed-"+tempNameSuffix(name)+"-*")
			if err != nil {
				step(out, "Note: could not keep %s's logs (%v); they are gone with the instance directory.", name, err)
				return
			}
			dest = d
		}
		if err := os.WriteFile(filepath.Join(dest, log), tailBytes(data, maxPreservedLogBytes), 0o600); err != nil {
			continue
		}
		saved = append(saved, log)
	}
	if len(saved) == 0 {
		return
	}
	step(out, "Kept %s's logs (%s) in %s — the instance directory itself is about to go.", name, strings.Join(saved, ", "), dest)
}

// tempNameSuffix makes an instance name safe to embed in an os.MkdirTemp pattern,
// which rejects a path separator outright — so an unusual name would otherwise
// cost the preserved logs entirely rather than merely their label.
func tempNameSuffix(name string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '-' || r == '.' || r == '_' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return r
		}
		return '-'
	}, name)
	if safe == "" {
		return "vm"
	}
	return safe
}

// tailBytes returns the last max bytes of data, with a header saying so and the
// leading partial line dropped. Under the cap the data is returned untouched, so
// the ordinary case is a byte-for-byte copy of the original log.
func tailBytes(data []byte, max int) []byte {
	if len(data) <= max {
		return data
	}
	tail := data[len(data)-max:]
	if i := bytes.IndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	}
	header := fmt.Sprintf("[sand: truncated — this is the last ~%d of %d bytes]\n", len(tail), len(data))
	return append([]byte(header), tail...)
}

// instanceDir is a Lima instance's own directory under the Lima home. "" when the
// name is empty or the Lima home cannot be determined, which the caller reads as
// "nothing to clean up".
func instanceDir(hf lima.HostFiles, name string) string {
	home := hf.LimaHome()
	if name == "" || home == "" {
		return ""
	}
	return filepath.Join(home, name)
}

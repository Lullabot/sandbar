package provision

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"
)

// template.go builds a golden VM template — a standalone Lima instance, reserved
// under vm.TemplateInstanceName's namespace, that a later create/reset clones
// from exactly like a base image. Capturing one is POWER-STATE PRESERVING: the
// source VM the template is snapshotted FROM must end the operation in exactly
// the power state it started in, because it is a VM the user is actively
// working in, not a disposable build artifact — an "oh, and by the way it also
// stopped/started your VM" side effect on a snapshot would be a surprising,
// unrelated disruption to whatever the user was doing with it.
//
// The mechanics mirror prepareBaseAndClone (see provision.go): serialize under
// the base/template lock (baselock.go), consistent-clone via limactl (which
// needs a stopped, idle source disk), and stamp the result exactly like a base
// image is stamped (baseversion.go) so a later create/reset can detect drift.
// Unlike the base-build path, a template's SOURCE is a live, user-owned VM —
// nothing here may destroy or mutate it beyond the strictly necessary,
// temporary stop, and that stop must always be undone.

// SnapshotResult carries what a successful SnapshotTemplate captured, so a
// caller (the `sand template snapshot` CLI, task 4) can build the
// registry.Template record without recomputing either field: PlaybookVersion
// and ToolsetKey are read from — or fall back to matching — the same fields
// registry.Template stores them under.
type SnapshotResult struct {
	// PlaybookVersion is the version stamp recorded on the template instance
	// (provision.PlaybookVersion's content-hash scheme), or "" when it could
	// not be determined — the template is then unstamped and reads as stale on
	// every later comparison, the same posture an unstamped base image takes.
	PlaybookVersion string
	// ToolsetKey is the canonical tool-set string embedded in PlaybookVersion
	// (vm.CreateConfig.ToolsetKey's own rendering), or "" when it could not be
	// determined.
	ToolsetKey string
}

// SnapshotTemplate captures source (a managed VM) into templateInstance (the
// reserved Lima instance name a golden template is stored under — see
// vm.TemplateInstanceName), via `limactl clone`, and stamps the result with the
// playbook version it was captured from.
//
// Power-state preservation is the load-bearing contract here: source's status is
// read BEFORE anything else, it is stopped only if it was actually running (a
// consistent clone needs an idle source disk — the same requirement a base
// image's clone has), and it is restored to that recorded state on every exit
// path, including a clone failure — via a single deferred restore, so there is
// no way to add a new failure path below that forgets to undo the stop.
//
// A clone failure also cleans up the half-written template instance
// (cleanupInstance, shared with the create path's own failed-clone cleanup) and
// returns before any stamp is written — a partially cloned template must never
// be mistaken for a complete, usable one.
func (p *Provisioner) SnapshotTemplate(ctx context.Context, source, templateInstance string, out io.Writer) (SnapshotResult, error) {
	hf := p.hostFiles()

	// Serialize against base/template mutation on this host, the same discipline
	// prepareBaseAndClone applies to a create's clone — a template instance is
	// created by the same limactl-clone primitive and must not race a concurrent
	// base rebuild or another snapshot/delete of the same template name. A lock
	// failure is non-fatal here (mirroring lockBase's own contract: it already
	// reported the reason via out for every failure except a cancelled ctx), so
	// this proceeds unserialized rather than refusing to snapshot at all.
	release, err := lockBase(ctx, hf, templateInstance, out)
	if err == nil {
		defer release()
	}

	// Record the source's power state BEFORE touching anything. wasRunning is the
	// single fact the deferred restore below acts on.
	prior, _ := p.Lima.Status(source)
	wasRunning := prior == "Running"

	if wasRunning {
		step(out, "Stopping %q to snapshot it consistently…", source)
		if err := p.Lima.StopStreaming(ctx, source, out); err != nil {
			return SnapshotResult{}, fmt.Errorf("stop %q to snapshot it: %w", source, err)
		}
	}

	// Restore the source's prior power state no matter what happens below —
	// including a clone failure or a stamp-write failure. A snapshot must never
	// leave a formerly-running VM stopped out from under its user.
	defer func() {
		if wasRunning {
			if err := p.Lima.StartStreaming(ctx, source, out); err != nil {
				step(out, "Note: could not restart %q after snapshotting it (%v); start it manually.", source, err)
			}
		}
	}()

	step(out, "Cloning %q into template %q…", source, templateInstance)
	if err := p.Lima.CloneStreaming(ctx, source, templateInstance, out); err != nil {
		// A half-written template instance dir makes every later `limactl list`
		// fatal, exactly like a half-written create — clean it up the same way.
		p.cleanupInstance(templateInstance, out)
		return SnapshotResult{}, fmt.Errorf("clone %q into template %q: %w", source, templateInstance, err)
	}

	// Stamp the template with the playbook version it was captured from. Prefer
	// a version already stamped against the source's own instance name (present
	// when the source is itself a template/base-derived instance that carries
	// one); a plain managed VM carries no stamp of its own (only a base image is
	// ever stamped), so the common case falls back to the CURRENT playbook
	// version — the closest available signal for "what this snapshot contains".
	ver := readBaseVersionFn(hf, source)
	if ver == "" {
		dir, dirErr := p.playbookDir()
		if dirErr != nil {
			step(out, "Note: could not locate the playbook to stamp template %q (%v); it will show as stale.", templateInstance, dirErr)
		} else if v, vErr := playbookVersionFn(dir, ""); vErr != nil {
			step(out, "Note: could not determine the current playbook version for template %q (%v); it will show as stale.", templateInstance, vErr)
		} else {
			ver = v
		}
	}
	toolset := toolsetFromStamp(ver)
	if ver != "" {
		if err := writeBaseVersionFn(hf, templateInstance, ver, time.Now()); err != nil {
			step(out, "Note: could not record template %q's playbook version (%v); it will show as stale.", templateInstance, err)
		}
	}

	return SnapshotResult{PlaybookVersion: ver, ToolsetKey: toolset}, nil
}

// DeleteTemplate removes a template's Lima instance (`limactl delete --force`),
// under the same base/template lock SnapshotTemplate takes — so a delete cannot
// race a concurrent snapshot into (or another delete of) the same template name.
func (p *Provisioner) DeleteTemplate(ctx context.Context, templateInstance string, out io.Writer) error {
	release, err := lockBase(ctx, p.hostFiles(), templateInstance, out)
	if err == nil {
		defer release()
	}
	if err := p.Lima.Delete(templateInstance, true); err != nil {
		return fmt.Errorf("delete template %q: %w", templateInstance, err)
	}
	return nil
}

// TemplateDiskBytes returns the allocated on-disk size of a template's qcow2
// image, or -1 when it cannot be measured (mirrors internal/ui/diskusage.go's
// diskUsedBytes, the same join of the instance directory's `disk` file, but
// resolved through the provisioner's own host-access handle rather than a
// caller-supplied one — a template's instance directory is always found under
// this provisioner's own Lima home).
func (p *Provisioner) TemplateDiskBytes(templateInstance string) int64 {
	hf := p.hostFiles()
	dir := filepath.Join(hf.LimaHome(), templateInstance)
	return hf.DiskAllocBytes(filepath.Join(dir, "disk"))
}

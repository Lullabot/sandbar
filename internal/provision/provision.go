package provision

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/vm"
)

// inGuestScript is the bash body run inside the guest to provision one phase. It
// reproduces the original bash provisioner's run_provision: the phase vars are
// streamed in over
// stdin into tmpfs (never argv, so a finalize token never appears in a process
// listing or on the persistent disk) and removed via an EXIT trap; the playbook
// is re-synced from the still-mounted host copy first so a run always reflects
// the current working tree, then run with ansible-playbook --connection=local.
//
// The vars file gets an explicit 0600 mode (via `install -m 600`) rather than a
// restrictive global umask: a global umask would also make mode-less files the
// playbook creates — notably the apt keyrings — root-only, which breaks apt
// signature verification by the _apt sandbox user.
const inGuestScript = `set -eu -o pipefail
vars=/dev/shm/sand-vars.yml
trap 'rm -f "$vars"' EXIT
install -m 600 /dev/null "$vars"
cat > "$vars"
rsync -a --delete /mnt/playbook/ /root/playbook/
cd /root/playbook
ansible-playbook -i localhost, --connection=local site.yml --extra-vars @"$vars"
`

// scopedInGuestScript returns inGuestScript unchanged when tags is empty
// (the normal full-role-set path used by every phase today), or the same
// script with an Ansible `--tags <tags>` appended to the ansible-playbook
// invocation when tags is non-empty — e.g. RenderSecrets passes "secrets" so
// a sync applies only the secrets role's tasks. tags is always an internal,
// hardcoded constant (never derived from user input), so appending it to the
// script text carries no injection risk.
func scopedInGuestScript(tags string) string {
	if tags == "" {
		return inGuestScript
	}
	return strings.TrimSuffix(inGuestScript, "\n") + " --tags " + tags + "\n"
}

// Provisioner drives the base-build / clone / finalize sequence through a
// lima.Client, streaming playbook output to the caller-supplied writer.
type Provisioner struct {
	Lima        *lima.Client
	PlaybookDir string
}

// step writes a phase banner to the streamed output so the user sees which
// lifecycle stage is running even before limactl/ansible prints anything — the
// slow base build and boots are otherwise long stretches of silence.
func step(out io.Writer, format string, args ...any) {
	fmt.Fprintf(out, "\n==> "+format+"\n", args...)
}

// runProvision streams one phase's extra-vars into the guest over stdin and runs
// the FULL playbook there (every role site.yml gates in for that phase).
// Mirrors the original bash provisioner's run_provision.
func (p *Provisioner) runProvision(ctx context.Context, name, phase, hostname string, cfg vm.CreateConfig, out io.Writer) error {
	return p.runProvisionTagged(ctx, name, phase, hostname, cfg, "", out)
}

// runProvisionTagged is runProvision's general form: when tags is non-empty
// it restricts the guest's ansible-playbook run to that Ansible `--tags`
// value (see scopedInGuestScript), so a caller like RenderSecrets can apply
// a single role's tasks against an already-running VM without running a full
// phase. The stdin/tmpfs vars-passing and rsync-then-run machinery are
// identical either way — this is the ONE place that shells into the guest,
// so create/finalize/Reset and RenderSecrets never duplicate it.
func (p *Provisioner) runProvisionTagged(ctx context.Context, name, phase, hostname string, cfg vm.CreateConfig, tags string, out io.Writer) error {
	vars, err := BuildExtraVars(cfg, phase, hostname)
	if err != nil {
		return fmt.Errorf("build extra-vars (%s): %w", phase, err)
	}
	step(out, "Provisioning %q (%s phase, Ansible)…", name, phase)
	// Vars go over STDIN, never argv (secret hygiene).
	if err := p.Lima.Shell(ctx, name, bytes.NewReader(vars), out, "sudo", "bash", "-c", scopedInGuestScript(tags)); err != nil {
		return fmt.Errorf("provisioning (%s) failed for %q: %w", phase, name, err)
	}
	return nil
}

// secretsRoleTag is the Ansible tag applied to the secrets role in site.yml
// (see the `tags: ["secrets"]` entry on that role), used by RenderSecrets to
// scope its run to just that role's tasks.
const secretsRoleTag = "secrets"

// RenderSecrets re-renders the host secrets store's CURRENT contents into an
// ALREADY-RUNNING VM by applying only the secrets role (`ansible-playbook
// --tags secrets`) — never a full finalize pass, so a sync is fast and has
// no side effects on any other role. It does NOT start, stop, or restart the
// VM or instruct a shell restart; that decision belongs to the caller (and
// per task 5, sync deliberately never forces one).
//
// This is the single "load store -> map to secrets_* vars -> run the
// secrets role over stdin" entry point: `sand secret sync` (cmd/sand,
// task 5) and the TUI's refresh action (task 6) both call it rather than
// duplicating the render logic. Callers only need name (the running Lima
// instance to target) and enough of cfg to satisfy BuildExtraVars' non-base
// fields — notably cfg.User, which the secrets role's getent lookup
// requires; the other identity/clone fields BuildExtraVars also embeds are
// harmless no-ops here since --tags secrets skips every task outside the
// secrets role. cfg.Name is always overridden to name, so the secrets store
// loaded is guaranteed to be the one for the VM actually being targeted,
// even if the caller's cfg came from a different source (e.g. a stale
// registry entry).
func (p *Provisioner) RenderSecrets(ctx context.Context, name string, cfg vm.CreateConfig, out io.Writer) error {
	cfg.Name = name
	if err := p.runProvisionTagged(ctx, name, "finalize", cfg.EffectiveHostname(), cfg, secretsRoleTag, out); err != nil {
		return fmt.Errorf("render secrets into %q: %w", name, err)
	}
	return nil
}

// BuildBase renders the base overlay, creates the base instance, runs the heavy
// base-phase playbook over a shell, and stops the instance (kept as the clone
// source). Mirrors the original bash provisioner's build_base.
func (p *Provisioner) BuildBase(ctx context.Context, cfg vm.CreateConfig, out io.Writer) error {
	overlay, err := RenderBaseOverlay(cfg, p.PlaybookDir)
	if err != nil {
		return fmt.Errorf("render base overlay: %w", err)
	}

	f, err := os.CreateTemp("", "sand-base-*.yaml")
	if err != nil {
		return fmt.Errorf("create overlay temp file: %w", err)
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return fmt.Errorf("chmod overlay temp file: %w", err)
	}
	if _, err := f.Write(overlay); err != nil {
		f.Close()
		return fmt.Errorf("write overlay temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close overlay temp file: %w", err)
	}

	step(out, "Building base image %q — downloads Debian and boots once; the first run takes several minutes…", cfg.BaseName)
	if err := p.Lima.CreateStreaming(ctx, cfg.BaseName, f.Name(), out); err != nil {
		return fmt.Errorf("create base image %q: %w", cfg.BaseName, err)
	}
	if err := p.runProvision(ctx, cfg.BaseName, "base", cfg.BaseName, cfg, out); err != nil {
		return err
	}
	// Keep the base stopped: it is never used directly, only cloned — and a clone
	// needs a quiescent source disk.
	step(out, "Stopping base image %q (quiescing it for cloning)…", cfg.BaseName)
	if err := p.Lima.StopStreaming(ctx, cfg.BaseName, out); err != nil {
		return fmt.Errorf("stop base image %q: %w", cfg.BaseName, err)
	}

	// Stamp the base with the playbook version it was built from so a later create
	// can detect drift and rebuild instead of silently cloning stale content. A
	// missing stamp (version unknown, or write failed) just forces a rebuild next
	// time, so neither branch here is fatal to the build.
	if v, err := playbookVersionFn(p.PlaybookDir); err != nil {
		step(out, "Note: could not record the base image's playbook version (%v); it will rebuild on the next create.", err)
	} else if err := writeBaseVersionFn(cfg.BaseName, v); err != nil {
		step(out, "Note: could not record the base image's playbook version (%v); it will rebuild on the next create.", err)
	}
	return nil
}

// CreateVM ensures a stopped base image exists, clones it into the target VM,
// starts it, runs the light finalize pass (hostname, git identity, optional repo
// clone), then bounces the VM so the first shell starts cleanly. Mirrors the
// original bash provisioner's launch sequence.
func (p *Provisioner) CreateVM(ctx context.Context, cfg vm.CreateConfig, out io.Writer) error {
	// Refuse an existing target rather than colliding on clone — the original
	// bash provisioner has the same guard. A definite status (no error,
	// non-empty) means the instance is already there. Recreate calls createVM
	// directly (it just deleted the target), so it bypasses this check instead
	// of racing a just-removed VM.
	if status, err := p.Lima.Status(cfg.Name); err == nil && status != "" {
		return fmt.Errorf("instance %q already exists — delete it first, or choose a different name", cfg.Name)
	}
	return p.createVM(ctx, cfg, out)
}

// createVM clones and finalizes the target, building the base image first if
// absent. It does NOT check whether the target already exists; callers that need
// that guard use CreateVM. Recreate uses createVM directly because it has just
// force-deleted the target.
func (p *Provisioner) createVM(ctx context.Context, cfg vm.CreateConfig, out io.Writer) error {
	if err := p.ensureBaseStopped(ctx, cfg, out); err != nil {
		return err
	}

	step(out, "Cloning %q from base image %q…", cfg.Name, cfg.BaseName)
	if err := p.Lima.CloneStreaming(ctx, cfg.BaseName, cfg.Name, out); err != nil {
		return fmt.Errorf("clone %q -> %q: %w", cfg.BaseName, cfg.Name, err)
	}
	// Size the clone before its first start: the base is built at a small disk
	// floor, so this grows the disk (and applies cpus/memory) for this VM.
	if err := p.Lima.Configure(cfg.Name, cfg.CPUs, cfg.Memory, cfg.Disk); err != nil {
		return fmt.Errorf("configure clone %q: %w", cfg.Name, err)
	}
	step(out, "Starting %q…", cfg.Name)
	if err := p.Lima.StartStreaming(ctx, cfg.Name, out); err != nil {
		return fmt.Errorf("start %q: %w", cfg.Name, err)
	}
	if err := p.runProvision(ctx, cfg.Name, "finalize", cfg.EffectiveHostname(), cfg, out); err != nil {
		return err
	}

	// Bounce the VM so the first interactive shell starts cleanly: the finalize
	// apt upgrade may have pulled a new kernel/libraries, and the hostname change
	// takes full effect on a fresh boot.
	step(out, "Restarting %q for a clean first boot…", cfg.Name)
	if err := p.Lima.StopStreaming(ctx, cfg.Name, out); err != nil {
		return fmt.Errorf("stop %q: %w", cfg.Name, err)
	}
	if err := p.Lima.StartStreaming(ctx, cfg.Name, out); err != nil {
		return fmt.Errorf("restart %q: %w", cfg.Name, err)
	}
	return nil
}

// ensureBaseStopped makes sure the base image exists and is stopped — a clone
// needs a quiescent source disk. An empty/error status means the instance is
// absent, so the heavy base build runs; an existing but running base is stopped.
func (p *Provisioner) ensureBaseStopped(ctx context.Context, cfg vm.CreateConfig, out io.Writer) error {
	status, err := p.Lima.Status(cfg.BaseName)
	exists := err == nil && status != ""

	// A base built from an older playbook would clone stale content into every new
	// VM, so rebuild it when the current playbook differs from what it was stamped
	// with. The check is host-side (git + a stamp file), adding no limactl calls to
	// the common up-to-date path.
	if exists && p.baseStale(cfg.BaseName, out) {
		if err := p.Lima.Delete(cfg.BaseName, true); err != nil {
			return fmt.Errorf("delete stale base image %q: %w", cfg.BaseName, err)
		}
		exists = false
	}

	switch {
	case !exists:
		if err := p.BuildBase(ctx, cfg, out); err != nil {
			return err
		}
	case status != "Stopped":
		step(out, "Stopping base image %q (quiescing it for cloning)…", cfg.BaseName)
		if err := p.Lima.StopStreaming(ctx, cfg.BaseName, out); err != nil {
			return fmt.Errorf("stop base image %q: %w", cfg.BaseName, err)
		}
	}
	return nil
}

// baseStale reports whether an existing base image was built from a different
// playbook version than the one that would be mounted now, and streams the
// reason when it returns true. A missing/unreadable stamp counts as stale, so a
// base built before version stamping is rebuilt once. A version-lookup error
// (e.g. the playbook dir is not a git checkout) is treated as NOT stale — better
// to reuse the base than to rebuild it on every create.
func (p *Provisioner) baseStale(baseName string, out io.Writer) bool {
	want, err := playbookVersionFn(p.PlaybookDir)
	if err != nil {
		step(out, "Note: could not determine the current playbook version (%v); reusing the existing base image %q.", err, baseName)
		return false
	}
	have := readBaseVersionFn(baseName)
	if have == want {
		return false
	}
	if have == "" {
		step(out, "Base image %q has no recorded playbook version; rebuilding it from the current playbook (%s)…", baseName, shortVersion(want))
	} else {
		step(out, "Base image %q was built from playbook %s but the current playbook is %s; rebuilding it…", baseName, shortVersion(have), shortVersion(want))
	}
	return true
}

// ResetOptions selects which of a VM's local state survives a reset. When both
// are false a reset is a clean recreate from the base image.
type ResetOptions struct {
	PreserveClaude  bool // keep ~/.claude and ~/.claude.json (login + history)
	PreserveProject bool // keep the per-org checkout + restored .env
}

// Reset recreates a managed VM from a (possibly edited) config, optionally
// preserving the Claude login and/or the per-org project tree across the
// destroy/recreate by staging them on the host and restoring them in the right
// order relative to the finalize playbook.
//
// Ordering is load-bearing: the Claude restore runs BEFORE finalize so the
// playbook re-applies ~/.claude/settings.json on top of the restored
// credentials/history; the project restore runs AFTER finalize and the finalize
// pass omits project_clone_url (CloneURL cleared) so the project role's clone
// step does not clobber the restored tree.
//
// Once stage-out has begun, no later error removes the staging dir — the error
// is wrapped with its path so the user can recover their data manually.
func (p *Provisioner) Reset(ctx context.Context, cfg vm.CreateConfig, opts ResetOptions, out io.Writer) error {
	// stageDir is only set once staging actually runs; an empty value means there
	// is nothing on the host to preserve or clean up.
	var stageDir string
	var home string
	var orgRel string
	var ok bool

	// 1. Stage out the selected state while the source VM is still alive.
	if opts.PreserveClaude || opts.PreserveProject {
		// Ensure the source VM is running so tar can read from it.
		if status, _ := p.Lima.Status(cfg.Name); status != "Running" {
			step(out, "Starting %q to stage its data…", cfg.Name)
			if err := p.Lima.StartStreaming(ctx, cfg.Name, out); err != nil {
				return fmt.Errorf("start %q for staging: %w", cfg.Name, err)
			}
		}
		var err error
		if home, err = guestHome(ctx, p.Lima, cfg.Name, cfg.User); err != nil {
			return fmt.Errorf("resolve home for %q: %w", cfg.Name, err)
		}
		if stageDir, err = newStageDir(); err != nil {
			return err
		}
		// From here on, errors must keep stageDir and surface its path.
		if opts.PreserveClaude {
			if err := StageOut(ctx, p.Lima, cfg.Name, home, []string{".claude", ".claude.json"}, filepath.Join(stageDir, "claude.tgz")); err != nil {
				return fmt.Errorf("reset failed after staging; your data is preserved at %s: %w", stageDir, err)
			}
		}
		if opts.PreserveProject {
			if orgRel, ok = cloneOrgRelDir(cfg.CloneURL); ok {
				if err := StageOut(ctx, p.Lima, cfg.Name, home, []string{orgRel}, filepath.Join(stageDir, "project.tgz")); err != nil {
					return fmt.Errorf("reset failed after staging; your data is preserved at %s: %w", stageDir, err)
				}
			}
		}
	}

	// staged reports whether host data exists; staged errors are wrapped with the
	// recovery path so the user can retrieve it.
	staged := stageDir != ""
	wrap := func(err error) error {
		if err == nil {
			return nil
		}
		if staged {
			return fmt.Errorf("reset failed after staging; your data is preserved at %s: %w", stageDir, err)
		}
		return err
	}

	// 2. Delete the existing VM.
	if err := p.Lima.Delete(cfg.Name, true); err != nil {
		return wrap(fmt.Errorf("delete %q: %w", cfg.Name, err))
	}

	// 3. Recreate sized from the base image.
	if err := p.ensureBaseStopped(ctx, cfg, out); err != nil {
		return wrap(err)
	}
	step(out, "Cloning %q from base image %q…", cfg.Name, cfg.BaseName)
	if err := p.Lima.CloneStreaming(ctx, cfg.BaseName, cfg.Name, out); err != nil {
		return wrap(fmt.Errorf("clone %q -> %q: %w", cfg.BaseName, cfg.Name, err))
	}
	if err := p.Lima.Configure(cfg.Name, cfg.CPUs, cfg.Memory, cfg.Disk); err != nil {
		return wrap(fmt.Errorf("configure clone %q: %w", cfg.Name, err))
	}
	step(out, "Starting %q…", cfg.Name)
	if err := p.Lima.StartStreaming(ctx, cfg.Name, out); err != nil {
		return wrap(fmt.Errorf("start %q: %w", cfg.Name, err))
	}

	// 4. Restore Claude BEFORE finalize so the playbook layers settings on top.
	if opts.PreserveClaude {
		if err := StageIn(ctx, p.Lima, cfg.Name, home, cfg.User, []string{".claude", ".claude.json"}, filepath.Join(stageDir, "claude.tgz")); err != nil {
			return wrap(fmt.Errorf("restore Claude into %q: %w", cfg.Name, err))
		}
	}

	// 5. Finalize, skipping the project clone only when a tree was actually
	// staged for restore (ok). If PreserveProject was requested but the URL had
	// no org component (nothing staged), fall back to the role's normal clone.
	finCfg := cfg
	if opts.PreserveProject && ok {
		finCfg.CloneURL = "" // omit project_clone_url so the role skips its clone
	}
	if err := p.runProvision(ctx, cfg.Name, "finalize", cfg.EffectiveHostname(), finCfg, out); err != nil {
		return wrap(err)
	}

	// 6. Restore the project tree AFTER finalize, then re-approve its .env.
	if opts.PreserveProject && ok {
		if err := StageIn(ctx, p.Lima, cfg.Name, home, cfg.User, []string{orgRel}, filepath.Join(stageDir, "project.tgz")); err != nil {
			return wrap(fmt.Errorf("restore project into %q: %w", cfg.Name, err))
		}
		if err := p.Lima.Shell(ctx, cfg.Name, nil, out, "sudo", "-iu", cfg.User, "direnv", "allow", home+"/"+orgRel); err != nil {
			return wrap(fmt.Errorf("direnv allow in %q: %w", cfg.Name, err))
		}
	}

	// 7. Bounce so the first interactive shell starts cleanly (mirror createVM).
	step(out, "Restarting %q for a clean first boot…", cfg.Name)
	if err := p.Lima.StopStreaming(ctx, cfg.Name, out); err != nil {
		return wrap(fmt.Errorf("stop %q: %w", cfg.Name, err))
	}
	if err := p.Lima.StartStreaming(ctx, cfg.Name, out); err != nil {
		return wrap(fmt.Errorf("restart %q: %w", cfg.Name, err))
	}

	// 8. Full success: drop the host staging dir.
	if staged {
		removeStageDir(stageDir)
	}
	return nil
}

// Recreate deletes the named instance (force) and re-clones it from the base
// image — a fast way to reset one VM. Mirrors the original bash provisioner's
// --recreate path.
func (p *Provisioner) Recreate(ctx context.Context, cfg vm.CreateConfig, out io.Writer) error {
	if err := p.Lima.Delete(cfg.Name, true); err != nil {
		return fmt.Errorf("delete %q: %w", cfg.Name, err)
	}
	// Skip CreateVM's exists-guard: we just deleted the target, and re-querying it
	// could spuriously refuse the recreate if the delete hasn't fully settled.
	return p.createVM(ctx, cfg, out)
}

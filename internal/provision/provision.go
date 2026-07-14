package provision

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

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
//
// The rsync copies only the playbook fileset, never the whole mount. In repo
// mode /mnt/playbook is the git checkout, so an unfiltered sync would drag the
// entire repository (.git, Go sources, agent tooling) into every VM — and
// readlink() on a symlink under the read-only host mount fails with EPERM, so a
// checkout containing symlinks (.claude/skills/) failed the sync outright. The
// filter list is the rsync spelling of the go:embed directives in
// playbook_embed.go: keep the two in step, and the guest gets the same tree
// whether it came from a checkout or the embedded copy. --delete-excluded makes
// /root/playbook match that set exactly, clearing junk a pre-filter base baked in.
//
// The SAND_ANSIBLE_TASK_TOTAL line exists because Ansible's default stdout
// callback prints no task count ANYWHERE in its output, so the TUI — which
// renders a progress bar on a building VM's tile — has no denominator to fill it
// with. --list-tasks supplies one, and it is exact rather than an estimate:
// Ansible announces a TASK banner even for a task it goes on to skip (a
// `when:`-gated role still announces every one of its tasks, then prints
// "skipping:"), so the static list and the live banner count agree. The +1 is
// "Gathering Facts", which every play runs and --list-tasks omits. Verified
// against a real base-phase run: 71 listed + 1 = the 72 banners it printed.
// Parsed host-side by internal/ui/ansible.go.
//
// It is best-effort by construction: if the listing fails or returns nothing
// countable, the total is 0, the tile falls back to an indeterminate bar, and
// the provision itself is untouched. A progress bar must never be able to break
// a build.
const inGuestScript = `set -eu -o pipefail
vars=/dev/shm/sand-vars.yml
trap 'rm -f "$vars"' EXIT
install -m 600 /dev/null "$vars"
cat > "$vars"
rsync -a --delete --delete-excluded \
  --include=/site.yml --include=/ansible.cfg --include=/inventory \
  --include='/roles/***' --include='/group_vars/***' --exclude='*' \
  /mnt/playbook/ /root/playbook/
cd /root/playbook
listed=$(ansible-playbook -i localhost, --connection=local site.yml --extra-vars @"$vars" --list-tasks 2>/dev/null | grep -cE '^ {6}[^ ]' || true)
` + taskTotalGuard + `
ansible-playbook -i localhost, --connection=local site.yml --extra-vars @"$vars"
`

// taskTotalGuard turns the raw count from `grep -c` into the denominator the tile's
// build bar uses, and it is a const of its own so a test can execute it under a real
// /bin/sh with a stubbed $listed (see TestTaskTotalGuard) — the bug it carries is a
// shell-semantics bug and cannot be caught by reading Go.
//
// ZERO MUST BE REJECTED, not incremented. `grep -c` prints "0" and exits 1 when it
// matches nothing; the `|| true` swallows the exit status, so $listed is the STRING
// "0" — non-empty and perfectly numeric. It therefore sailed past the
// ”|*[!0-9]* guard into total=$((0 + 1)) and the guest announced a total of ONE
// task. The first `TASK [...]` banner then read 1/1 and the tile showed a filled bar
// at 100% for the whole multi-minute build: a progress bar that is pinned full and
// lying, which is strictly worse than the indeterminate 0% bar the fallback exists
// to give. A guest whose --list-tasks pass fails, or whose ansible indents its
// output differently, hits this every time.
//
// The +1 is the play's implicit gather_facts task, which --list-tasks does not list
// but which the run does execute.
const taskTotalGuard = `case "${listed:-}" in
  ''|0|*[!0-9]*) total=0 ;;
  *) total=$((listed + 1)) ;;
esac
echo "SAND_ANSIBLE_TASK_TOTAL=$total"`

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
// the playbook there. Mirrors the original bash provisioner's run_provision.
func (p *Provisioner) runProvision(ctx context.Context, name, phase, hostname string, cfg vm.CreateConfig, out io.Writer) error {
	vars, err := BuildExtraVars(cfg, phase, hostname)
	if err != nil {
		return fmt.Errorf("build extra-vars (%s): %w", phase, err)
	}
	step(out, "Provisioning %q (%s phase, Ansible)…", name, phase)
	// Vars go over STDIN, never argv (secret hygiene).
	if err := p.Lima.Shell(ctx, name, bytes.NewReader(vars), out, "sudo", "bash", "-c", inGuestScript); err != nil {
		return fmt.Errorf("provisioning (%s) failed for %q: %w", phase, name, err)
	}
	return nil
}

// BuildBase renders the base overlay, creates the base instance, runs the heavy
// base-phase playbook over a shell, and stops the instance (kept as the clone
// source). Mirrors the original bash provisioner's build_base.
func (p *Provisioner) BuildBase(ctx context.Context, cfg vm.CreateConfig, out io.Writer) error {
	return p.buildBase(ctx, cfg, out, newPhaseTimer(out))
}

// buildBase is BuildBase's implementation, taking a timer so a caller already
// running a per-phase timed sequence (createVM, Reset) can share one timer and
// summary across the whole run instead of BuildBase starting its own.
func (p *Provisioner) buildBase(ctx context.Context, cfg vm.CreateConfig, out io.Writer, timer *phaseTimer) error {
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
	if err := timer.time("base image creation", func() error {
		return p.Lima.CreateStreaming(ctx, cfg.BaseName, f.Name(), out)
	}); err != nil {
		return fmt.Errorf("create base image %q: %w", cfg.BaseName, err)
	}
	if err := timer.time("base playbook", func() error {
		return p.runProvision(ctx, cfg.BaseName, "base", cfg.BaseName, cfg, out)
	}); err != nil {
		return err
	}
	// Keep the base stopped: it is never used directly, only cloned — and a clone
	// needs an idle source disk.
	step(out, "Stopping base image %q (making it idle for cloning)…", cfg.BaseName)
	if err := timer.time("base stop", func() error {
		return p.Lima.StopStreaming(ctx, cfg.BaseName, out)
	}); err != nil {
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
	timer := newPhaseTimer(out)
	if err := p.prepareBaseAndClone(ctx, cfg, out, timer); err != nil {
		return err
	}
	step(out, "Starting %q…", cfg.Name)
	if err := timer.time("clone start", func() error {
		// Size the clone before its first start: the base is built at a small
		// disk floor, so this grows the disk (and applies cpus/memory) for this
		// VM.
		if err := p.Lima.Configure(cfg.Name, cfg.CPUs, cfg.Memory, cfg.Disk); err != nil {
			return fmt.Errorf("configure clone %q: %w", cfg.Name, err)
		}
		if err := p.Lima.StartStreaming(ctx, cfg.Name, out); err != nil {
			return fmt.Errorf("start %q: %w", cfg.Name, err)
		}
		return nil
	}); err != nil {
		return err
	}
	if err := timer.time("finalize playbook", func() error {
		return p.runProvision(ctx, cfg.Name, "finalize", cfg.EffectiveHostname(), cfg, out)
	}); err != nil {
		return err
	}

	// Bounce the VM so the first interactive shell starts cleanly: the finalize
	// apt upgrade may have pulled a new kernel/libraries, and the hostname change
	// takes full effect on a fresh boot.
	step(out, "Restarting %q for a clean first boot…", cfg.Name)
	if err := timer.time("bounce", func() error {
		if err := p.Lima.StopStreaming(ctx, cfg.Name, out); err != nil {
			return fmt.Errorf("stop %q: %w", cfg.Name, err)
		}
		if err := p.Lima.StartStreaming(ctx, cfg.Name, out); err != nil {
			return fmt.Errorf("restart %q: %w", cfg.Name, err)
		}
		return nil
	}); err != nil {
		return err
	}
	timer.summary()
	return nil
}

// prepareBaseAndClone is the base image's WHOLE critical section: prepare it, then
// clone from it, with the base lock (baselock.go) held across BOTH.
//
// The clone has to be inside the lock, and it was not. The lock was taken and
// released inside ensureBaseStopped, so it covered the decision to build or rebuild
// the base and then let go — while the 40-60s clone that READS that base ran
// unprotected. baselock.go's own doc lists "the stale-base path can DELETE the base
// while another create is cloning from it" among the races it closes, and it did not
// close that one: playbookVersionFn is the playbook checkout's git HEAD plus a
// "-dirty" suffix, so it changes at RUNTIME. Edit a playbook file while a create is
// cloning, start a second create, and that create takes the free lock, decides the
// base is stale, and force-deletes the instance the first create is reading its disk
// from.
//
// The cost is that two concurrent creates serialize their clones. That is the right
// trade: a clone is seconds to a minute, while the Ansible run that follows it —
// which is the expensive part, and the part concurrency was for — still overlaps
// freely. The alternative (a shared/exclusive lock, readers cloning in parallel while
// a writer rebuilds) buys back that minute at the price of lock-upgrade semantics
// nobody should have to reason about while a VM's disk is being deleted underneath
// them.
func (p *Provisioner) prepareBaseAndClone(ctx context.Context, cfg vm.CreateConfig, out io.Writer, timer *phaseTimer) error {
	release, err := lockBase(ctx, cfg.BaseName, out)
	if err != nil {
		return err // only a cancelled context gets here
	}
	defer release()

	if err := p.ensureBaseStopped(ctx, cfg, out, timer); err != nil {
		return err
	}

	step(out, "Cloning %q from base image %q…", cfg.Name, cfg.BaseName)
	if err := timer.time("clone", func() error {
		return p.Lima.CloneStreaming(ctx, cfg.BaseName, cfg.Name, out)
	}); err != nil {
		return fmt.Errorf("clone %q -> %q: %w", cfg.BaseName, cfg.Name, err)
	}
	return nil
}

// ensureBaseStopped makes sure the base image exists and is stopped — a clone
// needs an idle source disk. An empty/error status means the instance is
// absent, so the heavy base build runs; an existing but running base is stopped.
func (p *Provisioner) ensureBaseStopped(ctx context.Context, cfg vm.CreateConfig, out io.Writer, timer *phaseTimer) error {
	// The caller holds the base lock (prepareBaseAndClone). Everything here is one
	// atomic decision — read the base's status, then act on that reading — and it must
	// stay inside that lock. See baselock.go.
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
		if err := p.buildBase(ctx, cfg, out, timer); err != nil {
			return err
		}
	case status != "Stopped":
		step(out, "Stopping base image %q (making it idle for cloning)…", cfg.BaseName)
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
// are false a reset rebuilds the VM cleanly from the base image.
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
			if orgRel, ok = OrgRelDir(cfg.CloneURL); ok {
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

	// 3. Recreate sized from the base image. Reset does not surface a per-phase
	// timing summary (only "sand create" does; see phaseTimer's doc comment) —
	// prepareBaseAndClone still needs a timer to share its signature with
	// createVM, so give it one whose readings are simply discarded here.
	if err := p.prepareBaseAndClone(ctx, cfg, out, newPhaseTimer(out)); err != nil {
		return wrap(err)
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

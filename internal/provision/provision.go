package provision

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

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

// profileGuestScript is the opt-in variant of inGuestScript, selected when the
// host has SAND_PROFILE set (see sandProfileEnabled). ansible.cfg enables the
// profile_tasks callback unconditionally, but that callback lives in the
// ansible.posix collection, not ansible-core (strikethroo plan 13, task 02),
// and the default Lima dependency script installs only ansible-core. This
// variant installs ansible.posix on demand, right before the playbook run, so
// the callback loads and prints its timing; the default path (inGuestScript)
// never does this and stays collection-free.
const profileGuestScript = `set -eu -o pipefail
vars=/dev/shm/sand-vars.yml
trap 'rm -f "$vars"' EXIT
install -m 600 /dev/null "$vars"
cat > "$vars"
rsync -a --delete --delete-excluded \
  --include=/site.yml --include=/ansible.cfg --include=/inventory \
  --include='/roles/***' --include='/group_vars/***' --exclude='*' \
  /mnt/playbook/ /root/playbook/
cd /root/playbook
ansible-galaxy collection install ansible.posix
listed=$(ansible-playbook -i localhost, --connection=local site.yml --extra-vars @"$vars" --list-tasks 2>/dev/null | grep -cE '^ {6}[^ ]' || true)
` + taskTotalGuard + `
ansible-playbook -i localhost, --connection=local site.yml --extra-vars @"$vars"
`

// sandProfileEnabled reports whether the host opted into profiling via
// SAND_PROFILE (any non-empty value other than "0"). It gates the only place
// that ever installs an Ansible collection in the Lima flow: the default path
// stays collection-free.
func sandProfileEnabled() bool {
	v := os.Getenv("SAND_PROFILE")
	return v != "" && v != "0"
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
// the playbook there. Mirrors the original bash provisioner's run_provision.
//
// aptUpgrade is threaded straight through to BuildExtraVars: true only for a
// base self-refresh run (see reapplyBase), never for a cold base build and
// never for finalize.
func (p *Provisioner) runProvision(ctx context.Context, name, phase, hostname string, cfg vm.CreateConfig, aptUpgrade bool, out io.Writer) error {
	vars, err := BuildExtraVars(cfg, phase, hostname, aptUpgrade)
	if err != nil {
		return fmt.Errorf("build extra-vars (%s): %w", phase, err)
	}
	step(out, "Provisioning %q (%s phase, Ansible)…", name, phase)
	script := inGuestScript
	if sandProfileEnabled() {
		script = profileGuestScript
	}
	// Vars go over STDIN, never argv (secret hygiene).
	if err := p.Lima.Shell(ctx, name, bytes.NewReader(vars), out, "sudo", "bash", "-c", script); err != nil {
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
		// buildBase only ever runs when the base is ABSENT or is being rebuilt from
		// scratch (the caller force-deleted it first), so the instance here is one
		// this run created — and a ^C during the Debian download leaves it
		// half-written, which makes every later `limactl list` fatal. Ours to clean
		// up. See cleanup.go.
		p.cleanupInstance(cfg.BaseName, out)
		return fmt.Errorf("create base image %q: %w", cfg.BaseName, err)
	}
	// A failure PAST this point leaves a base that booted and has a valid
	// lima.yaml: it is stale, not broken, and the next create converges or rebuilds
	// it. It is deliberately left alone — an unstamped base is already treated as
	// stale (baseversion.go), so nothing is lost by keeping it, and the apt cache it
	// has already downloaded is worth keeping.
	// Best-effort: reuse whatever a previous build harvested, so this build's
	// apt installs are CPU-bound rather than network-bound. See aptcache.go for
	// why this is a `limactl copy` round trip rather than a host mount.
	_ = timer.time("apt cache seed", func() error { return p.seedAptCache(ctx, cfg.BaseName, out) })
	if err := timer.time("base playbook", func() error {
		return p.runProvision(ctx, cfg.BaseName, "base", cfg.BaseName, cfg, false, out)
	}); err != nil {
		return err
	}
	// Best-effort: harvest whatever apt fetched this run so the NEXT rebuild can
	// reuse it. Only reached once the base playbook has actually succeeded
	// (the `return err` above exits on failure), so a failed run never harvests
	// a half-populated cache.
	_ = timer.time("apt cache harvest", func() error { return p.harvestAptCache(ctx, cfg.BaseName, out) })
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
	if v, err := playbookVersionFn(p.PlaybookDir, cfg.ToolsetKey()); err != nil {
		step(out, "Note: could not record the base image's playbook version (%v); it will rebuild on the next create.", err)
	} else if err := writeBaseVersionFn(cfg.BaseName, v, time.Now()); err != nil {
		step(out, "Note: could not record the base image's playbook version (%v); it will rebuild on the next create.", err)
	}
	return nil
}

// CreateOptions carries per-run INTENT — what this create is asking for — as
// distinct from vm.CreateConfig, which describes the VM being made.
//
// The distinction is load-bearing, not stylistic: a successful create records its
// vm.CreateConfig verbatim in the managed-VM registry (registry.Add), and that
// recorded config is what a later reset rebuilds the VM from. A Rebuild flag
// living on it would be persisted with the VM and would silently force a
// from-scratch base rebuild on every future reset of it.
type CreateOptions struct {
	// Rebuild destroys the base image and builds it again from the raw Debian
	// image, whatever its version stamp says. It is the escape hatch for a base
	// that the idempotent re-apply cannot fix — a hand-broken guest, or an image
	// left half-converged by something that died mid-run.
	//
	// It is an INTENT, passed down, and the destroy it asks for happens inside
	// ensureBaseStopped: under the base lock, with no clone in flight. A caller
	// that deletes the base itself before calling the provisioner deletes it
	// outside that lock — where another create may be mid-clone from the very disk
	// being removed. That is the race baselock.go exists to close, and `sand
	// create --rebuild` used to be the hole in it.
	Rebuild bool
}

// CreateVM ensures a stopped base image exists, clones it into the target VM,
// starts it, runs the light finalize pass (hostname, git identity, optional repo
// clone), then bounces the VM so the first shell starts cleanly. Mirrors the
// original bash provisioner's launch sequence.
func (p *Provisioner) CreateVM(ctx context.Context, cfg vm.CreateConfig, out io.Writer) error {
	return p.CreateVMWithOptions(ctx, cfg, CreateOptions{}, out)
}

// CreateVMWithOptions is CreateVM with the per-run intent the TUI has no way to
// express (CreateVM's signature is the TUI's provisionFunc — see
// internal/ui/progress.go — and every job it starts is a plain create).
func (p *Provisioner) CreateVMWithOptions(ctx context.Context, cfg vm.CreateConfig, opts CreateOptions, out io.Writer) error {
	// Refuse an existing target rather than colliding on clone — the original
	// bash provisioner has the same guard. A definite status (no error,
	// non-empty) means the instance is already there. Recreate calls createVM
	// directly (it just deleted the target), so it bypasses this check instead
	// of racing a just-removed VM.
	if status, err := p.Lima.Status(cfg.Name); err == nil && status != "" {
		return fmt.Errorf("instance %q already exists — delete it first, or choose a different name", cfg.Name)
	}
	return p.createVM(ctx, cfg, opts, out)
}

// createVM clones and finalizes the target, building the base image first if
// absent. It does NOT check whether the target already exists; callers that need
// that guard use CreateVM. Recreate uses createVM directly because it has just
// force-deleted the target.
func (p *Provisioner) createVM(ctx context.Context, cfg vm.CreateConfig, opts CreateOptions, out io.Writer) error {
	timer := newPhaseTimer(out)
	if err := p.prepareBaseAndClone(ctx, cfg, opts, out, timer); err != nil {
		// The clone is where this VM's directory first appears, so a ^C or a failure
		// in there can leave one half-written — and a half-written instance dir
		// (disk + cidata, no lima.yaml) makes every later `limactl list` FATAL, which
		// wedges the whole board, not just this VM. Clean up what we started. See
		// cleanup.go.
		p.cleanupInstance(cfg.Name, out)
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
		// Still "never finished being created": the clone exists but has never
		// booted, so there is nothing in it to inspect and nothing to keep.
		p.cleanupInstance(cfg.Name, out)
		return err
	}
	// PAST THIS POINT THE VM IS NOT CLEANED UP. It booted, its lima.yaml is valid,
	// and `limactl list` is happy with it — so a failed or cancelled PLAYBOOK leaves
	// a VM the user can shell into and a retained log they can read, which is the
	// point of keeping a failed run. Deleting it here would throw away the evidence.
	if err := timer.time("finalize playbook", func() error {
		return p.runProvision(ctx, cfg.Name, "finalize", cfg.EffectiveHostname(), cfg, false, out)
	}); err != nil {
		return err
	}

	// The bounce used to be unconditional. Its two stated reasons are both gone:
	//   - the finalize apt upgrade (removed in task 8), and
	//   - docker-group membership, which is granted in the BASE phase
	//     (roles/dev-tools, gated `when: provision_phase != 'finalize'` in
	//     site.yml) and therefore baked into the image — a clone has the group
	//     in /etc/group before it ever boots, and every `limactl shell` is a
	//     fresh login with a fresh initgroups(). Finalize never touches groups.
	//   - the hostname change was also checked (not just assumed): verified live
	//     against a running VM that `hostnamectl set-hostname` (what
	//     roles/base's "Set hostname" task uses) takes effect immediately, with
	//     no reboot — a fresh `limactl shell` right after the change already
	//     reports the new name via `hostname`, `cat /etc/hostname`, and
	//     `hostnamectl`, and /etc/hosts is rewritten by the same playbook run
	//     (a template task, not reboot-gated either). Nothing caches the old
	//     name across a fresh process.
	// What is left is a genuine reboot request from the guest itself.
	if p.needsReboot(ctx, cfg.Name) {
		step(out, "Restarting %q to apply a pending reboot…", cfg.Name)
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
	}
	timer.summary()
	return nil
}

// needsReboot reports whether the guest itself is asking for a restart.
// /var/run/reboot-required is written by Debian's kernel/libc upgrade
// machinery (update-notifier-common's hook); a genuine miss (the file does
// not exist) and a communication failure both read as "no reboot needed" —
// the caller has no more information than "the marker was not confirmed
// present", and treating an unreachable guest as reboot-required would just
// trade one unconditional bounce for another.
func (p *Provisioner) needsReboot(ctx context.Context, name string) bool {
	var buf bytes.Buffer
	err := p.Lima.Shell(ctx, name, nil, &buf, "test", "-e", "/var/run/reboot-required")
	return err == nil
}

// hasLiveTmux reports whether the guest's persistent `main` tmux session
// (internal/lima/attach.go) is up. `=main` is an exact-match target, matching
// guestAttachExpr's own care to never match a same-prefixed session.
func (p *Provisioner) hasLiveTmux(ctx context.Context, name string) bool {
	var buf bytes.Buffer
	err := p.Lima.Shell(ctx, name, nil, &buf, "tmux", "has-session", "-t", "=main")
	return err == nil
}

// prepareBaseAndClone is the base image's WHOLE critical section: prepare it, then
// clone from it, with the base lock (baselock.go) held across BOTH.
//
// The clone has to be inside the lock, and it was not. The lock was taken and
// released inside ensureBaseStopped, so it covered the decision to build or rebuild
// the base and then let go — while the 40-60s clone that READS that base ran
// unprotected. baselock.go's own doc lists "the stale-base path can DELETE the base
// while another create is cloning from it" among the races it closes, and it did not
// close that one: playbookVersionFn is a content hash of the playbook fileset, so
// it changes at RUNTIME whenever a playbook file is edited. Edit a playbook file
// while a create is cloning, start a second create, and that create takes the free
// lock, decides the base is stale, and force-deletes the instance the first create
// is reading its disk from.
//
// Everything that can destroy or mutate the base image is therefore reached through
// HERE and nowhere else — the from-scratch build, the in-place re-apply, and
// --rebuild's destroy (CreateOptions.Rebuild). A caller that "helpfully" deletes the
// base before calling the provisioner puts that destroy back outside the lock, which
// is precisely the bug this closed.
//
// The cost is that two concurrent creates serialize their clones. That is the right
// trade: a clone is seconds to a minute, while the Ansible run that follows it —
// which is the expensive part, and the part concurrency was for — still overlaps
// freely. The alternative (a shared/exclusive lock, readers cloning in parallel while
// a writer rebuilds) buys back that minute at the price of lock-upgrade semantics
// nobody should have to reason about while a VM's disk is being deleted underneath
// them.
func (p *Provisioner) prepareBaseAndClone(ctx context.Context, cfg vm.CreateConfig, opts CreateOptions, out io.Writer, timer *phaseTimer) error {
	release, err := lockBase(ctx, cfg.BaseName, out)
	if err != nil {
		return err // only a cancelled context gets here
	}
	// Released on EVERY exit, including a cancelled build's. A build that dies
	// holding this lock wedges every other create on the machine behind a job that
	// is already over.
	defer release()

	if err := p.ensureBaseStopped(ctx, cfg, opts, out, timer); err != nil {
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

// ensureBaseStopped makes sure the base image exists, is current, and is stopped —
// a clone needs an idle source disk carrying the playbook this create was asked
// for. It has four outcomes: build the base from scratch (it is absent, or
// --rebuild asked for a fresh one), converge it in place (it exists but its
// playbook version is stale), refresh it in place (it exists, its version is
// current, but it has gone past baseMaxAge without an apt upgrade), or leave it
// alone (it is current and fresh), stopping it if something left it running.
//
// EVERY READ HERE HAPPENS AFTER THE LOCK. The caller (prepareBaseAndClone) holds
// the exclusive base lock around this whole function and the clone that follows
// it, and the status, the version stamp, AND the build timestamp are read INSIDE
// it, never cached outside and carried in. That is not a formality: a create that
// blocked for minutes behind someone else's rebuild or refresh must see what that
// work left behind. A staleness or age verdict formed before the wait is a
// verdict about a base that no longer exists in that state — act on it and this
// create redoes work that finished while it queued.
func (p *Provisioner) ensureBaseStopped(ctx context.Context, cfg vm.CreateConfig, opts CreateOptions, out io.Writer, timer *phaseTimer) error {
	status, err := p.Lima.Status(cfg.BaseName)
	exists := err == nil && status != ""

	// --rebuild: destroy the base and build it again from the raw Debian image.
	// THIS is where that destroy belongs — under the lock, with no clone in flight
	// (see CreateOptions.Rebuild).
	if exists && opts.Rebuild {
		step(out, "Rebuilding base image %q from scratch…", cfg.BaseName)
		if err := p.Lima.Delete(cfg.BaseName, true); err != nil {
			return fmt.Errorf("delete base image %q for rebuild: %w", cfg.BaseName, err)
		}
		exists = false
	}

	// A base built from an older playbook would clone stale content into every new
	// VM, so bring it up to date when the current playbook differs from what it was
	// stamped with. The check is host-side (a content hash and a stamp file), adding
	// no limactl calls to the common up-to-date path.
	if exists {
		// Report retained tools regardless of which branch below runs. The base is
		// shared and a converge-in-place cannot uninstall (see mergeToolsetVersion),
		// so a de-selected tool stays on the base — and therefore in this clone —
		// whether or not anything else about the base is stale. Warning only on the
		// converge branch would mean the plain `--with-go=false` case, where nothing
		// else changed and nothing is re-applied, said nothing at all.
		if lost := shrunkTools(readBaseVersionFn(cfg.BaseName), cfg.ToolsetKey()); len(lost) > 0 {
			step(out, "Note: %s were de-selected but remain installed on the base image.\n    Ansible cannot uninstall them. Rebuild the base to remove them\n    (sand create --rebuild, or the \"Rebuild base image\" toggle in the form).", strings.Join(lost, ", "))
		}
		if want, stale := p.baseStale(cfg, out); stale {
			// Ansible is idempotent, so a playbook edit only needs its DELTA applied:
			// converge the existing base in place instead of paying for a Debian
			// re-download and a from-scratch run of every task in the play.
			//
			// But ONLY a base that was created from the same overlay this create would
			// use — same playbook mount, same bootstrap script. A base created from a
			// different one either runs a playbook that is not ours (and gets stamped
			// as if it were) or runs ours without the prerequisites it is allowed to
			// assume. baseoverlay.go is entirely about why. When it cannot be
			// converged, rebuild it: that re-creates it under the current overlay,
			// which is the behaviour that existed before the re-apply did.
			convergeable, why := p.baseConvergeable(cfg)
			if convergeable {
				return p.reapplyBase(ctx, cfg, want, status, out, timer, false)
			}
			step(out, "Base image %q cannot be updated in place (%s); rebuilding it from scratch…", cfg.BaseName, why)
			if err := p.Lima.Delete(cfg.BaseName, true); err != nil {
				return fmt.Errorf("delete stale base image %q: %w", cfg.BaseName, err)
			}
			exists = false
		} else if want != "" && p.baseNeedsRefresh(cfg, out) {
			// The base's CONTENT is current (want came back non-empty: the version
			// comparison actually succeeded and matched), but its PACKAGES have gone
			// stale: apt upgrades have landed upstream since it was last built or
			// refreshed. Bring it up to date with ONE in-place apt upgrade here, so
			// every clone taken from it afterwards skips the upgrade entirely — instead
			// of every clone re-paying for it in the finalize phase, which is what this
			// task removes.
			//
			// want=="" means baseStale could not even determine the current playbook
			// version (a genuine lookup error) and is already reusing the base as-is;
			// piling an age-driven mutation on top of a base we cannot positively
			// confirm is current would be acting on a guess, not a fact.
			convergeable, why := p.baseConvergeable(cfg)
			if convergeable {
				return p.reapplyBase(ctx, cfg, want, status, out, timer, true)
			}
			// Un-convergeable (baseoverlay.go: different playbook mount or bootstrap
			// script) — the same fallback the version-staleness branch above takes.
			// Rebuilding from scratch both fixes the overlay mismatch and leaves the
			// base freshly stamped, so this is not a wedge.
			step(out, "Base image %q cannot be refreshed in place (%s); rebuilding it from scratch…", cfg.BaseName, why)
			if err := p.Lima.Delete(cfg.BaseName, true); err != nil {
				return fmt.Errorf("delete base image %q for refresh: %w", cfg.BaseName, err)
			}
			exists = false
		}
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

// reapplyBase converges an existing base image IN PLACE: start it, re-run the
// base-phase playbook against it, re-stamp it, and stop it again, ready to be
// cloned. It is the whole point of the playbook-development inner loop — an edit
// to one role costs the delta Ansible actually has to apply, not a Debian download
// and a from-scratch run of every task in the play. ensureBaseStopped's 30-day
// self-refresh reuses this SAME machinery (start -> converge -> stamp -> stop)
// rather than duplicating it, with aptUpgrade=true as the only difference.
//
// It runs under the base lock, held by prepareBaseAndClone across this AND the
// clone that follows: the base is RUNNING for the duration of the playbook, and a
// clone taken from a running base copies a disk that is being written to.
//
// version is the stamp to record on success — read (under the lock) BEFORE the
// run, not recomputed after it. The playbook directory is a live working tree; if
// it is edited while the run is in flight, a recomputed stamp would swear the base
// carries content it never saw. A stamp that is older than the base is harmless
// (the next create converges the difference); a stamp that is NEWER than the base
// is a lie that no later create can detect.
//
// aptUpgrade is passed straight to runProvision/BuildExtraVars: true only for the
// 30-day self-refresh (ensureBaseStopped's baseNeedsRefresh), which emits
// base_apt_upgrade so roles/base/tasks/main.yml runs its "Upgrade all apt
// packages" task; false for a playbook-version re-apply, which must not pay for
// an upgrade it did not ask for.
//
// THE STAMP IS WRITTEN ONLY ON A FULL, SUCCESSFUL RUN. On any failure — a broken
// task, a cancelled context, a killed limactl — the old stamp stays exactly where
// it is, so the base remains unambiguously stale/aged and the next create retries
// it. A fresh stamp on a half-converged base is the worst outcome available here:
// it is undetectable, and every clone taken from that base afterwards inherits it.
func (p *Provisioner) reapplyBase(ctx context.Context, cfg vm.CreateConfig, version, status string, out io.Writer, timer *phaseTimer, aptUpgrade bool) error {
	step(out, "Re-applying the playbook to base image %q (Ansible is idempotent: only the changes are applied)…", cfg.BaseName)

	// The base is normally Stopped (that is how it is kept, for cloning). Anything
	// else means a previous run left it up — including a re-apply that was
	// interrupted between its playbook and its stop — and starting a running
	// instance is a no-op worth skipping rather than relying on.
	if status != "Running" {
		if err := timer.time("base start", func() error {
			return p.Lima.StartStreaming(ctx, cfg.BaseName, out)
		}); err != nil {
			return fmt.Errorf("start base image %q for re-apply: %w", cfg.BaseName, err)
		}
	}

	// Best-effort, same as buildBase: reuse whatever a previous build harvested.
	_ = timer.time("apt cache seed", func() error { return p.seedAptCache(ctx, cfg.BaseName, out) })

	// The same in-guest script as the build: the phase vars over stdin (never
	// argv), the same rsync of the playbook fileset out of /mnt/playbook, and its
	// own SAND_ANSIBLE_TASK_TOTAL marker — this is a THIRD Ansible run down the
	// same pipe, and without its own denominator the TUI's progress bar would keep
	// counting its task banners against the previous run's total
	// (internal/ui/ansible.go).
	if err := timer.time("base playbook", func() error {
		return p.runProvision(ctx, cfg.BaseName, "base", cfg.BaseName, cfg, aptUpgrade, out)
	}); err != nil {
		return err // NO STAMP: leave the base unambiguously stale (see above).
	}

	// Best-effort, same as buildBase: harvest for the next rebuild. Only reached
	// once the base playbook above has actually succeeded.
	_ = timer.time("apt cache harvest", func() error { return p.harvestAptCache(ctx, cfg.BaseName, out) })

	// Converged. Stamp it before the stop: the stamp describes the base's CONTENT,
	// which is now correct whatever the stop does next. A write failure is not fatal
	// to the create — an unstamped base simply reads as stale and is converged again
	// next time, the same posture buildBase takes.
	//
	// The stamp's timestamp is the apt-age clock (baseNeedsRefresh), NOT "when did
	// we last touch the base". A content-only re-apply upgrades no packages, so it
	// carries the prior build time forward: stamping "now" here would restart the
	// 30-day clock on every playbook edit, and a base edited more often than that
	// would never be upgraded at all — which, with finalize's apt upgrade gone, means
	// no VM would ever get a security update. Only the aptUpgrade run resets it.
	builtAt := time.Now()
	if !aptUpgrade {
		if prior, ok := readBaseBuiltAtFn(cfg.BaseName); ok {
			builtAt = prior
		}
	}
	if err := writeBaseVersionFn(cfg.BaseName, version, builtAt); err != nil {
		step(out, "Note: could not record the base image's playbook version (%v); it will be re-applied again on the next create.", err)
	}

	step(out, "Stopping base image %q (making it idle for cloning)…", cfg.BaseName)
	if err := timer.time("base stop", func() error {
		return p.Lima.StopStreaming(ctx, cfg.BaseName, out)
	}); err != nil {
		return fmt.Errorf("stop base image %q: %w", cfg.BaseName, err)
	}
	return nil
}

// baseStale reports whether an existing base image was built from a different
// playbook version than the one that would be mounted now — and, when it was, the
// version the base has to be brought up to (which its caller records only once the
// base actually carries it). It streams the reason whenever it says yes.
//
// A missing/unreadable stamp counts as stale, so a base built before version
// stamping is converged once. So does a stamp written by the old git-HEAD scheme
// (no "v2:" prefix): an upgrading user converges onto the content-hash scheme once
// rather than trusting a stamp a different versioning scheme vouched for. A
// version-lookup error (a genuine filesystem problem — the content-hash scheme
// cannot fail merely for "not a git checkout" the way the old scheme did) is
// treated as NOT stale: better to reuse the base than to churn it on every create.
func (p *Provisioner) baseStale(cfg vm.CreateConfig, out io.Writer) (version string, stale bool) {
	baseName := cfg.BaseName
	want, err := playbookVersionFn(p.PlaybookDir, cfg.ToolsetKey())
	if err != nil {
		step(out, "Note: could not determine the current playbook version (%v); reusing the existing base image %q.", err, baseName)
		return "", false
	}
	have := readBaseVersionFn(baseName)

	// Compare against what the base will actually CONTAIN once converged — the
	// current playbook hash plus the union of its existing and requested tools —
	// not against the requested selection alone. A converge-in-place only adds, so
	// a bare de-selection changes nothing on the base and must not read as stale;
	// treating it as stale is what made alternating selections re-converge the one
	// shared base forever (mergeToolsetVersion explains the ping-pong in full).
	want = mergeToolsetVersion(want, have)
	if have == want {
		return want, false
	}
	switch {
	case have == "":
		step(out, "Base image %q has no recorded playbook version; bringing it up to date with the current playbook (%s)…", baseName, shortVersion(want))
	case !strings.HasPrefix(have, playbookVersionPrefix):
		step(out, "Base image %q was built by an older version scheme; bringing it up to date with the current playbook (%s)…", baseName, shortVersion(want))
	default:
		step(out, "Base image %q was built from playbook %s but the current playbook is %s; bringing it up to date…", baseName, shortVersion(have), shortVersion(want))
	}
	return want, true
}

// baseMaxAge is how long a base image may run without a full `apt upgrade`
// before a create refreshes it once, in place, under the base lock, instead of
// every future clone paying for the upgrade itself in the finalize phase.
const baseMaxAge = 30 * 24 * time.Hour

// baseNeedsRefresh reports whether an already-current (non-stale) base has gone
// past baseMaxAge since it was last built or refreshed, and streams the reason
// when it has. It is called only after baseStale has said "not stale" for cfg —
// staleness (a playbook edit) and age (apt drift) are independent dimensions,
// and a base can be current in content while still needing an apt upgrade.
//
// A missing or unparseable BuiltAt — a base built before this task, or a
// corrupt stamp — is read as "cannot prove this base is fresh" and refreshes it
// once, the same "never guess" posture baseStale takes for an
// unparseable/missing version. After that one refresh the stamp carries a real
// timestamp and this reads normally from then on.
func (p *Provisioner) baseNeedsRefresh(cfg vm.CreateConfig, out io.Writer) bool {
	baseName := cfg.BaseName
	builtAt, ok := readBaseBuiltAtFn(baseName)
	if !ok {
		step(out, "Base image %q has no recorded build time; refreshing it once now (other creates will queue behind this)…", baseName)
		return true
	}
	age := time.Since(builtAt)
	if age <= baseMaxAge {
		return false
	}
	step(out, "Base image %q is older than %d days; refreshing it once now (other creates will queue behind this)…", baseName, int(baseMaxAge/(24*time.Hour)))
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

	// 3. Recreate sized from the base image. A reset re-clones ONE VM; it never
	// asks for a base rebuild (CreateOptions zero value), it just takes the base as
	// the base lock finds it. Reset does not surface a per-phase timing summary
	// (only "sand create" does; see phaseTimer's doc comment) — prepareBaseAndClone
	// still needs a timer to share its signature with createVM, so give it one whose
	// readings are simply discarded here.
	if err := p.prepareBaseAndClone(ctx, cfg, CreateOptions{}, out, newPhaseTimer(out)); err != nil {
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
	if err := p.runProvision(ctx, cfg.Name, "finalize", cfg.EffectiveHostname(), finCfg, false, out); err != nil {
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

	// 7. Bounce only when the guest actually asked for one (mirror createVM's
	// conditional bounce; see its comment for why the old unconditional reasons
	// are gone).
	//
	// Unlike createVM's clone, this VM is the one this VERY call just finalized —
	// nothing has had a chance to attach to it yet, so hasLiveTmux should read
	// false in the overwhelmingly common case. It is checked anyway, on
	// principle: `sand reset` also runs against a VM whose name/identity a
	// caller could reuse for an already-attached session in edge cases we do not
	// fully control from here (e.g. a script driving Reset directly rather than
	// through the CLI/TUI paths that always operate on a freshly (re)created
	// name). A stop+start of a RUNNING VM destroys the guest's persistent `main`
	// tmux session and everything running in it — the precise disaster
	// internal/lima/attach.go's lingering session exists to prevent — so a live
	// session is never bounced through silently.
	//
	// We warn and proceed rather than refuse: refusing would leave Reset's own
	// preceding work (delete + reclone + restore + finalize, all already
	// committed) in a state the caller cannot easily walk back from, for the
	// sake of a bounce that is itself already a rare case (reboot-required is
	// the exception, not the rule). Silence was the actual defect; a loud
	// warning that still finishes the reset is the least surprising fix.
	//
	// Counter-consideration: "never bounce" is not automatically safe either. A
	// long-lived tmux server freezes its supplementary groups and environment
	// at the moment each pane is forked. If a future re-apply against a
	// RUNNING VM ever changed group membership (it does not today — see
	// createVM's comment), existing panes would not observe that change
	// without a restart. That is a real cost of skipping the bounce, not a
	// reason to make it unconditional again; it means any code that starts
	// changing groups on a live VM must reckon with tmux explicitly, the same
	// way this one does.
	if p.needsReboot(ctx, cfg.Name) {
		if p.hasLiveTmux(ctx, cfg.Name) {
			step(out, "WARNING: %q has a live tmux session; restarting it now to apply a pending reboot will destroy that session and everything running in it.", cfg.Name)
		}
		step(out, "Restarting %q to apply a pending reboot…", cfg.Name)
		if err := p.Lima.StopStreaming(ctx, cfg.Name, out); err != nil {
			return wrap(fmt.Errorf("stop %q: %w", cfg.Name, err))
		}
		if err := p.Lima.StartStreaming(ctx, cfg.Name, out); err != nil {
			return wrap(fmt.Errorf("restart %q: %w", cfg.Name, err))
		}
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
	return p.RecreateWithOptions(ctx, cfg, CreateOptions{}, out)
}

// RecreateWithOptions is Recreate with the per-run intent (CreateOptions). The
// two flags are independent — --recreate targets the CLONE, --rebuild the BASE —
// and `sand create --recreate --rebuild` asks for both, so the recreate path has
// to carry the base intent down as well.
//
// The delete here is of the TARGET VM, not the base image, so it needs no base
// lock: nothing clones from a target.
func (p *Provisioner) RecreateWithOptions(ctx context.Context, cfg vm.CreateConfig, opts CreateOptions, out io.Writer) error {
	if err := p.Lima.Delete(cfg.Name, true); err != nil {
		return fmt.Errorf("delete %q: %w", cfg.Name, err)
	}
	// Skip CreateVM's exists-guard: we just deleted the target, and re-querying it
	// could spuriously refuse the recreate if the delete hasn't fully settled.
	return p.createVM(ctx, cfg, opts, out)
}

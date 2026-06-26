package provision

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/deviantintegral/claude-code-ansible/tui/internal/lima"
	"github.com/deviantintegral/claude-code-ansible/tui/internal/vm"
)

// inGuestScript is the bash body run inside the guest to provision one phase. It
// reproduces new-vm.sh's run_provision: the phase vars are streamed in over
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
vars=/dev/shm/claude-vm-vars.yml
trap 'rm -f "$vars"' EXIT
install -m 600 /dev/null "$vars"
cat > "$vars"
rsync -a --delete /mnt/playbook/ /root/playbook/
cd /root/playbook
ansible-playbook -i localhost, --connection=local site.yml --extra-vars @"$vars"
`

// Provisioner drives the base-build / clone / finalize sequence through a
// lima.Client, streaming playbook output to the caller-supplied writer.
type Provisioner struct {
	Lima        *lima.Client
	PlaybookDir string
}

// runProvision streams one phase's extra-vars into the guest over stdin and runs
// the playbook there. Mirrors new-vm.sh's run_provision.
func (p *Provisioner) runProvision(ctx context.Context, name, phase, hostname string, cfg vm.CreateConfig, out io.Writer) error {
	vars, err := BuildExtraVars(cfg, phase, hostname)
	if err != nil {
		return fmt.Errorf("build extra-vars (%s): %w", phase, err)
	}
	// Vars go over STDIN, never argv (secret hygiene).
	if err := p.Lima.Shell(ctx, name, bytes.NewReader(vars), out, "sudo", "bash", "-c", inGuestScript); err != nil {
		return fmt.Errorf("provisioning (%s) failed for %q: %w", phase, name, err)
	}
	return nil
}

// BuildBase renders the base overlay, creates the base instance, runs the heavy
// base-phase playbook over a shell, and stops the instance (kept as the clone
// source). Mirrors new-vm.sh's build_base.
func (p *Provisioner) BuildBase(ctx context.Context, cfg vm.CreateConfig, out io.Writer) error {
	overlay, err := RenderBaseOverlay(cfg, p.PlaybookDir)
	if err != nil {
		return fmt.Errorf("render base overlay: %w", err)
	}

	f, err := os.CreateTemp("", "claude-vm-base-*.yaml")
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

	if err := p.Lima.Create(cfg.BaseName, f.Name()); err != nil {
		return fmt.Errorf("create base image %q: %w", cfg.BaseName, err)
	}
	if err := p.runProvision(ctx, cfg.BaseName, "base", cfg.BaseName, cfg, out); err != nil {
		return err
	}
	// Keep the base stopped: it is never used directly, only cloned — and a clone
	// needs a quiescent source disk.
	if err := p.Lima.Stop(cfg.BaseName); err != nil {
		return fmt.Errorf("stop base image %q: %w", cfg.BaseName, err)
	}
	return nil
}

// CreateVM ensures a stopped base image exists, clones it into the target VM,
// starts it, runs the light finalize pass (hostname, git identity, optional repo
// clone), then bounces the VM so the first shell starts cleanly. Mirrors
// new-vm.sh's launch sequence.
func (p *Provisioner) CreateVM(ctx context.Context, cfg vm.CreateConfig, out io.Writer) error {
	// Refuse an existing target rather than colliding on clone — new-vm.sh has the
	// same guard. A definite status (no error, non-empty) means the instance is
	// already there. Recreate calls createVM directly (it just deleted the
	// target), so it bypasses this check instead of racing a just-removed VM.
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
	// Ensure the base image exists and is stopped (a clone needs a quiescent
	// source disk). An empty/error status means the instance is absent.
	status, err := p.Lima.Status(cfg.BaseName)
	switch {
	case err != nil || status == "":
		if err := p.BuildBase(ctx, cfg, out); err != nil {
			return err
		}
	case status != "Stopped":
		if err := p.Lima.Stop(cfg.BaseName); err != nil {
			return fmt.Errorf("stop base image %q: %w", cfg.BaseName, err)
		}
	}

	if err := p.Lima.Clone(cfg.BaseName, cfg.Name); err != nil {
		return fmt.Errorf("clone %q -> %q: %w", cfg.BaseName, cfg.Name, err)
	}
	if err := p.Lima.Start(cfg.Name); err != nil {
		return fmt.Errorf("start %q: %w", cfg.Name, err)
	}
	if err := p.runProvision(ctx, cfg.Name, "finalize", cfg.EffectiveHostname(), cfg, out); err != nil {
		return err
	}

	// Bounce the VM so the first interactive shell starts cleanly: the finalize
	// apt upgrade may have pulled a new kernel/libraries, and the hostname change
	// takes full effect on a fresh boot.
	if err := p.Lima.Stop(cfg.Name); err != nil {
		return fmt.Errorf("stop %q: %w", cfg.Name, err)
	}
	if err := p.Lima.Start(cfg.Name); err != nil {
		return fmt.Errorf("restart %q: %w", cfg.Name, err)
	}
	return nil
}

// Recreate deletes the named instance (force) and re-clones it from the base
// image — a fast way to reset one VM. Mirrors new-vm.sh's --recreate path.
func (p *Provisioner) Recreate(ctx context.Context, cfg vm.CreateConfig, out io.Writer) error {
	if err := p.Lima.Delete(cfg.Name, true); err != nil {
		return fmt.Errorf("delete %q: %w", cfg.Name, err)
	}
	// Skip CreateVM's exists-guard: we just deleted the target, and re-querying it
	// could spuriously refuse the recreate if the delete hasn't fully settled.
	return p.createVM(ctx, cfg, out)
}

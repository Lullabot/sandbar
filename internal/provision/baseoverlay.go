package provision

// baseoverlay.go answers one question, and the in-place re-apply (reapplyBase in
// provision.go) is not safe without the answer: CAN this base image be converged
// in place at all, or must it be rebuilt from scratch?
//
// A re-apply starts the EXISTING base and re-runs the base playbook inside it. What
// that playbook is, and what it can assume about the guest it lands in, are both
// decided by the base's OVERLAY — the lima.yaml Lima wrote when the instance was
// created (RenderBaseOverlay). A later `limactl start` re-applies exactly that file.
// Nothing a re-apply does can change it. Two fields of it are therefore load-bearing:
//
//  1. THE PLAYBOOK MOUNT. The guest rsyncs its playbook out of /mnt/playbook, i.e.
//     out of whatever host directory the base was CREATED with — not the one this
//     create is converging from. Re-applying to a base that mounts a different
//     directory would run someone else's playbook and then stamp the base with OUR
//     version: a base that claims content it does not have, cloned into every VM
//     from then on, and never detected again, because the stamp matches. Two
//     checkouts (a git worktree beside the main tree) do this, and so does a
//     released binary — outside a checkout, LocatePlaybook extracts the embedded
//     playbook to a FRESH temp dir on every run (playbook.go), so the base mounts
//     the temp dir of the run that BUILT it, which holds the old playbook and which
//     /tmp cleanup may have deleted outright. Upgrading the binary is exactly what
//     makes the base stale, so this is the common case, not an edge of it.
//
//  2. THE BOOTSTRAP SCRIPT. Lima's `mode: dependency` script is what installs the
//     tools the playbook is allowed to assume — ansible-core and rsync to run at
//     all, and (since the consolidated apt pass) curl and gnupg, which the base
//     role's keyring tasks shell out to and which its comments name as "guaranteed
//     present by the Lima dependency script". That guarantee is a property of the
//     OVERLAY, so a base created under an older one does not carry it. Re-applying
//     the current playbook into it runs tasks whose prerequisites were never
//     installed: the run fails, the base is (correctly) left unstamped and stale,
//     and the next create re-applies it and fails again. A wedge, not a rebuild.
//
// So: converge in place only when the base was created from the same playbook mount
// AND the same bootstrap this create would use. Otherwise fall back to the
// from-scratch rebuild, which re-creates the base under the current overlay — the
// behaviour that existed before the re-apply did. The check costs one file read on
// the stale path and nothing at all on the common, up-to-date path.

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/vm"
)

// playbookMountPoint is where a base image mounts the host playbook directory.
// RenderBaseOverlay writes it and the in-guest script rsyncs out of it; the two are
// pinned together by TestBaseOverlayReaderUnderstandsTheOverlayWeWrite.
const playbookMountPoint = "/mnt/playbook"

// baseOverlay is the part of an instance's lima.yaml that decides whether the
// instance can be converged in place: which playbook it mounts, and the bootstrap
// script Lima runs in it on every boot.
type baseOverlay struct {
	PlaybookDir string // host dir mounted at /mnt/playbook
	Bootstrap   string // the `mode: dependency` provision script
}

// baseOverlayFn is indirected through a package var so tests can drive the base's
// overlay without materialising a Lima instance on disk.
var baseOverlayFn = readBaseOverlay

// readBaseOverlay reads an existing base image's overlay from that instance's own
// lima.yaml — the ground truth of what `limactl start` will mount and run, rather
// than what this process happens to believe about it.
//
// An unreadable or unparseable instance file is not an error to report: it means we
// cannot PROVE what the base would do, and the caller must then treat it as
// unconvergeable (rebuild) rather than guess.
func readBaseOverlay(hf lima.HostFiles, baseName string) (baseOverlay, bool) {
	b, err := hf.ReadFile(filepath.Join(hf.LimaHome(), baseName, "lima.yaml"))
	if err != nil {
		return baseOverlay{}, false
	}
	return parseBaseOverlay(b)
}

// parseBaseOverlay extracts the playbook mount and the bootstrap script from a Lima
// instance file — or from the overlay that produced one, since Lima keeps the mount
// and provision blocks it is handed verbatim. Both sides of the comparison are
// parsed by this one function, so they cannot disagree over YAML spelling.
func parseBaseOverlay(limaYAML []byte) (baseOverlay, bool) {
	var doc struct {
		Mounts []struct {
			Location   string `yaml:"location"`
			MountPoint string `yaml:"mountPoint"`
		} `yaml:"mounts"`
		Provision []struct {
			Mode   string `yaml:"mode"`
			Script string `yaml:"script"`
		} `yaml:"provision"`
	}
	if err := yaml.Unmarshal(limaYAML, &doc); err != nil {
		return baseOverlay{}, false
	}

	var o baseOverlay
	for _, m := range doc.Mounts {
		if m.MountPoint == playbookMountPoint && m.Location != "" {
			o.PlaybookDir = m.Location
			break
		}
	}
	for _, pr := range doc.Provision {
		if pr.Mode == "dependency" && pr.Script != "" {
			o.Bootstrap = pr.Script
			break
		}
	}
	if o.PlaybookDir == "" || o.Bootstrap == "" {
		return baseOverlay{}, false // not a base image sand made, or not one we understand
	}
	return o, true
}

// baseConvergeable reports whether an existing base image can be converged in place
// by re-running the playbook inside it — and, when it cannot, why not, in words a
// user watching the stream can act on.
//
// It compares the base's own overlay against the one this create would build a base
// from. Anything it cannot prove is a "no": being wrong that way costs a rebuild,
// while being wrong the other way runs the wrong playbook, or runs the right one in
// a guest that was never given what it needs.
func (p *Provisioner) baseConvergeable(cfg vm.CreateConfig) (bool, string) {
	have, ok := baseOverlayFn(p.hostFiles(), cfg.BaseName)
	if !ok {
		return false, "its Lima instance file cannot be read"
	}

	dir, err := p.playbookDir()
	if err != nil {
		return false, fmt.Sprintf("the playbook could not be located (%v)", err)
	}
	wantYAML, err := RenderBaseOverlay(cfg, dir)
	if err != nil {
		return false, fmt.Sprintf("the current base overlay could not be rendered (%v)", err)
	}
	want, ok := parseBaseOverlay(wantYAML)
	if !ok {
		return false, "the current base overlay could not be parsed"
	}

	if !sameDir(have.PlaybookDir, want.PlaybookDir) {
		return false, fmt.Sprintf("it mounts a different playbook directory (%s)", have.PlaybookDir)
	}
	if have.Bootstrap != want.Bootstrap {
		return false, "it was created with a different bootstrap script, so the playbook's prerequisites are not guaranteed in it"
	}
	return true, ""
}

// sameDir reports whether two host paths name the same directory. It compares the
// cleaned paths first — which is also the only comparison available when a path does
// not exist — and falls back to identity on disk, so a directory reached through a
// symlink (macOS resolves the per-process temp dir through one) still matches
// itself.
func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

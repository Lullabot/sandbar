package provision

import (
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"
	"gopkg.in/yaml.v3"
)

func testOverlayImage() BaseImageSpec {
	return BaseImageSpec{Location: "/verified/base.qcow2", Arch: "x86_64", Digest: "sha256:" + strings.Repeat("a", 64)}
}

func TestRenderBaseOverlayRejectsMalformedDigest(t *testing.T) {
	image := testOverlayImage()
	image.Digest = "sha256:abc"
	if _, err := RenderBaseOverlay(vm.DefaultCreateConfig(), "/playbook", image); err == nil {
		t.Fatal("short digest accepted")
	}
}

func TestRenderBaseOverlay(t *testing.T) {
	cfg := vm.CreateConfig{CPUs: 4, Memory: "8GiB", Disk: "100GiB"}
	const playbookDir = "/home/andrew/src/sandbar"

	image := BaseImageSpec{Location: `/cache/sand "base".qcow2`, Arch: "x86_64", Digest: "sha256:" + strings.Repeat("a", 64)}
	data, err := RenderBaseOverlay(cfg, playbookDir, image)
	if err != nil {
		t.Fatalf("RenderBaseOverlay: %v", err)
	}
	got := string(data)

	wantSubstrings := []string{
		"images:",
		"sha256:",
		"mountType: reverse-sshfs",
		"cpus: 4",
		`memory: "8GiB"`,
		// The base overlay always pins disk to the floor, not cfg.Disk (100GiB
		// above); clones are grown to the requested size after cloning.
		`disk: "20GiB"`,
		`- location: "` + playbookDir + `"`,
		"mountPoint: /mnt/playbook",
		"writable: false",
		"mode: dependency",
		"command -v ansible-playbook >/dev/null 2>&1",
		"command -v rsync >/dev/null 2>&1",
		"command -v curl >/dev/null 2>&1",
		"command -v gpg >/dev/null 2>&1",
		"python3 -c 'import passlib' >/dev/null 2>&1",
		"install -y --no-install-recommends ansible-core rsync curl gnupg ca-certificates python3-passlib",
	}
	// Lima's own container stack is redundant (the playbook installs Docker) and
	// expensive: its setup runs in cloud-final on EVERY boot, costing ~19s of a
	// clone's ~58s start, and it plants ~575MB of nerdctl-full in the base that
	// every clone copies. Dropping either line silently hands both back.
	wantSubstrings = append(wantSubstrings, "containerd:", "system: false", "user: false")
	for _, s := range wantSubstrings {
		if !strings.Contains(got, s) {
			t.Errorf("overlay missing %q\n--- overlay ---\n%s", s, got)
		}
	}

	// The render must be valid YAML with the expected shape.
	var doc struct {
		Base      []string `yaml:"base"`
		MountType string   `yaml:"mountType"`
		Images    []struct {
			Location string `yaml:"location"`
			Arch     string `yaml:"arch"`
			Digest   string `yaml:"digest"`
		} `yaml:"images"`
		CPUs   int    `yaml:"cpus"`
		Memory string `yaml:"memory"`
		Disk   string `yaml:"disk"`
		Mounts []struct {
			Location   string `yaml:"location"`
			MountPoint string `yaml:"mountPoint"`
			Writable   bool   `yaml:"writable"`
		} `yaml:"mounts"`
		Provision []struct {
			Mode   string `yaml:"mode"`
			Script string `yaml:"script"`
		} `yaml:"provision"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("overlay is not valid YAML: %v\n%s", err, got)
	}
	if len(doc.Base) != 0 {
		t.Errorf("base = %v, want no inherited stock image", doc.Base)
	}
	if doc.MountType != "reverse-sshfs" {
		t.Errorf("mountType = %q, want reverse-sshfs for cloud kernel", doc.MountType)
	}
	if len(doc.Images) != 1 || doc.Images[0].Location != image.Location || doc.Images[0].Arch != image.Arch || doc.Images[0].Digest != image.Digest {
		t.Errorf("images = %+v, want exactly %+v", doc.Images, image)
	}
	if doc.CPUs != 4 || doc.Memory != "8GiB" || doc.Disk != "20GiB" {
		t.Errorf("cpus/memory/disk = %d/%q/%q", doc.CPUs, doc.Memory, doc.Disk)
	}
	if len(doc.Mounts) != 1 {
		t.Fatalf("got %d mounts, want 1", len(doc.Mounts))
	}
	m := doc.Mounts[0]
	if m.Location != playbookDir || m.MountPoint != "/mnt/playbook" || m.Writable {
		t.Errorf("mount = %+v, want read-only %s at /mnt/playbook", m, playbookDir)
	}
	if len(doc.Provision) != 1 || doc.Provision[0].Mode != "dependency" {
		t.Fatalf("provision = %+v, want one dependency entry", doc.Provision)
	}
	if !strings.Contains(doc.Provision[0].Script, "install -y --no-install-recommends ansible-core rsync curl gnupg ca-certificates python3-passlib") {
		t.Errorf("dependency script missing ansible-core+rsync+curl+gnupg+ca-certificates+passlib install:\n%s", doc.Provision[0].Script)
	}
	// passlib is not optional and not a recommendation we can inherit: the user
	// role hashes the account password with password_hash('sha512'), which fails
	// at play time with "Unable to encrypt nor hash, passlib must be installed"
	// unless python3-passlib is on the box. --no-install-recommends means nothing
	// else drags it in, so a live base build broke on exactly this. Pin it.
	if !strings.Contains(doc.Provision[0].Script, "python3-passlib") {
		t.Errorf("dependency script omits python3-passlib, so the user role's "+
			"password_hash('sha512') filter will fail at play time:\n%s", doc.Provision[0].Script)
	}
	// The bundled `ansible` package (200MB installed) must never be installed on
	// the default path; only the lean ansible-core (8MB) is acceptable here.
	if strings.Contains(doc.Provision[0].Script, "install -y ansible ") ||
		strings.HasSuffix(strings.TrimSpace(doc.Provision[0].Script), "install -y ansible") {
		t.Errorf("dependency script installs the fat ansible bundle instead of ansible-core:\n%s", doc.Provision[0].Script)
	}
	// --no-install-recommends is load-bearing, not hygiene. Debian's ansible-core
	// Recommends: ansible, so without the flag apt installs the fat bundle anyway
	// and the whole point of bootstrapping ansible-core is silently lost. A live
	// base build caught exactly that; this pins it so it cannot come back.
	if !strings.Contains(doc.Provision[0].Script, "--no-install-recommends") {
		t.Errorf("dependency script omits --no-install-recommends, so ansible-core's "+
			"Recommends: ansible will drag the 200MB bundle back in:\n%s", doc.Provision[0].Script)
	}
}

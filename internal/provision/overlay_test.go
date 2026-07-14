package provision

import (
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"
	"gopkg.in/yaml.v3"
)

func TestRenderBaseOverlay(t *testing.T) {
	cfg := vm.CreateConfig{CPUs: 4, Memory: "8GiB", Disk: "100GiB"}
	const playbookDir = "/home/andrew/src/sandbar"

	data, err := RenderBaseOverlay(cfg, playbookDir)
	if err != nil {
		t.Fatalf("RenderBaseOverlay: %v", err)
	}
	got := string(data)

	wantSubstrings := []string{
		"- template:_images/debian-13",
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
		"apt-get install -y ansible-core rsync curl gnupg ca-certificates",
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(got, s) {
			t.Errorf("overlay missing %q\n--- overlay ---\n%s", s, got)
		}
	}

	// The render must be valid YAML with the expected shape.
	var doc struct {
		Base   []string `yaml:"base"`
		CPUs   int      `yaml:"cpus"`
		Memory string   `yaml:"memory"`
		Disk   string   `yaml:"disk"`
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
	if len(doc.Base) != 1 || doc.Base[0] != "template:_images/debian-13" {
		t.Errorf("base = %v, want [template:_images/debian-13]", doc.Base)
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
	if !strings.Contains(doc.Provision[0].Script, "apt-get install -y ansible-core rsync curl gnupg ca-certificates") {
		t.Errorf("dependency script missing ansible-core+rsync+curl+gnupg+ca-certificates install:\n%s", doc.Provision[0].Script)
	}
	// The bundled `ansible` package (200MB installed) must never be installed on
	// the default path; only the lean ansible-core (8MB) is acceptable here.
	if strings.Contains(doc.Provision[0].Script, "install -y ansible ") ||
		strings.HasSuffix(strings.TrimSpace(doc.Provision[0].Script), "install -y ansible") {
		t.Errorf("dependency script installs the fat ansible bundle instead of ansible-core:\n%s", doc.Provision[0].Script)
	}
}

package lima

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestConfigureArgvStripsWritableMounts is the fast, portable half of the
// clone-strip guard: it pins the exact `limactl edit --set` expression
// Configure builds, using the fake Runner (no real limactl needed). It is a
// STRING assertion, so on its own it cannot prove the expression is valid yq
// syntax or that it does what it claims — TestConfigureStripsWritableMountAgainstRealLimactl
// below is the real guard for that.
func TestConfigureArgvStripsWritableMounts(t *testing.T) {
	f := &fakeRunner{outputs: map[string][]byte{}}
	c := New(f)
	if err := c.Configure("vm1", 4, "8GiB", "100GiB"); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("got %d calls, want 1: %v", len(f.calls), f.calls)
	}
	want := []string{"edit", "--set",
		`.cpus=4 | .memory="8GiB" | .disk="100GiB" | .mounts |= map(select(.writable != true))`,
		"vm1"}
	got := f.calls[0]
	if len(got) != len(want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv = %v, want %v", got, want)
		}
	}
}

// resolvedCloneFixtureTemplate is a stand-in for a REAL clone's on-disk lima.yaml: the
// shape `limactl clone` produces once a base image has actually been started
// (which resolves the overlay's `base: [template:...]` down to a concrete
// `images:` list — `limactl edit` refuses to touch a file that still carries
// an unresolved `base:` entry, so a fixture built straight from
// provision.RenderBaseOverlay's own output cannot be fed to a real `edit`
// without first booting a VM; that full round trip is covered separately by
// the limae2e-gated tests). The two mounts mirror RenderBaseOverlay exactly:
// a read-only playbook mount and a writable apt-cache mount, which is what
// `limactl clone` would have copied byte-for-byte from the base's lima.yaml.
const resolvedCloneFixtureTemplate = `images:
- location: "https://example.invalid/debian-13.qcow2"
  arch: "x86_64"
  digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000"
cpus: 2
memory: "2GiB"
disk: "20GiB"
mounts:
- location: %q
  mountPoint: /mnt/playbook
  writable: false
- location: %q
  mountPoint: /mnt/apt-cache
  writable: true
`

// TestConfigureStripsWritableMountAgainstRealLimactl is the REAL guard: it
// hands Configure's exact expression to the actual limactl binary — the same
// yq engine `limactl edit --set` uses in production — against a fixture
// shaped like a real clone's lima.yaml, and asserts the writable apt-cache
// mount is gone afterward while the read-only playbook mount survives.
//
// This is the test the task's mutation proof exercises: delete the
// `| .mounts |= map(select(.writable != true))` clause from Configure and this
// test goes red, because the writable mount is still there afterward.
//
// It skips (does not fail) when limactl is not on PATH, so `go test ./...`
// stays green on a machine without Lima installed — exactly like every other
// test in this package, which is built to run without a real limactl binary.
// This one opts back IN to the real binary, on purpose, because a fake Runner
// can only prove what string was built, never that Lima accepts and applies
// it.
func TestConfigureStripsWritableMountAgainstRealLimactl(t *testing.T) {
	if _, err := exec.LookPath("limactl"); err != nil {
		t.Skip("limactl not on PATH; skipping the real-limactl guard (see TestConfigureArgvStripsWritableMounts for the portable half)")
	}

	limaHome := t.TempDir()
	t.Setenv("LIMA_HOME", limaHome)
	const name = "sand-strip-test"
	instDir := filepath.Join(limaHome, name)
	if err := os.MkdirAll(instDir, 0o700); err != nil {
		t.Fatalf("mkdir instance dir: %v", err)
	}

	// limactl warns (but does not fail) on a mount location that does not exist
	// on the host, so give it real directories rather than risk that warning
	// masking a real problem.
	playbookHostDir := t.TempDir()
	aptCacheHostDir := t.TempDir()
	fixture := fmt.Sprintf(resolvedCloneFixtureTemplate, playbookHostDir, aptCacheHostDir)
	if err := os.WriteFile(filepath.Join(instDir, "lima.yaml"), []byte(fixture), 0o644); err != nil {
		t.Fatalf("write fixture lima.yaml: %v", err)
	}

	c := New(NewExecRunner())
	if err := c.Configure(name, 4, "8GiB", "100GiB"); err != nil {
		t.Fatalf("Configure against real limactl: %v", err)
	}

	after, err := os.ReadFile(filepath.Join(instDir, "lima.yaml"))
	if err != nil {
		t.Fatalf("read back lima.yaml: %v", err)
	}
	var doc struct {
		CPUs   int    `yaml:"cpus"`
		Memory string `yaml:"memory"`
		Disk   string `yaml:"disk"`
		Mounts []struct {
			Location   string `yaml:"location"`
			MountPoint string `yaml:"mountPoint"`
			Writable   bool   `yaml:"writable"`
		} `yaml:"mounts"`
	}
	if err := yaml.Unmarshal(after, &doc); err != nil {
		t.Fatalf("parse edited lima.yaml: %v\n%s", err, after)
	}

	if doc.CPUs != 4 || doc.Memory != "8GiB" || doc.Disk != "100GiB" {
		t.Errorf("cpus/memory/disk = %d/%q/%q, want 4/8GiB/100GiB", doc.CPUs, doc.Memory, doc.Disk)
	}

	for _, m := range doc.Mounts {
		if m.Writable {
			t.Errorf("clone's lima.yaml still carries a WRITABLE mount after Configure: %+v\nfull file:\n%s", m, after)
		}
	}
	if len(doc.Mounts) != 1 || doc.Mounts[0].MountPoint != "/mnt/playbook" || doc.Mounts[0].Location != playbookHostDir || doc.Mounts[0].Writable {
		t.Errorf("mounts = %+v, want exactly the read-only playbook mount at %s", doc.Mounts, playbookHostDir)
	}
}

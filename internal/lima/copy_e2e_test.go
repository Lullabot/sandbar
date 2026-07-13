//go:build limae2e

// This test boots a REAL Lima VM and so is gated behind the `limae2e` build tag
// (and the LIMA_E2E env var) — it never runs in the normal `go test ./...`. It
// exercises the one thing the fake Runner cannot prove: that lima.Client.Copy
// actually moves bytes over `limactl copy` in both directions, and that the
// destination-is-a-directory contract places a recursive source inside the dest.
//
// Run (needs limactl + KVM/nested virt; downloads the Debian 13 image once):
//
//	go test -tags limae2e -timeout 30m -run TestE2E ./internal/lima/
//
// (set LIMA_E2E=1 in the environment). Mirrors the cleanup discipline of
// provision/lima_e2e_test.go so a failed run never leaks a VM.
package lima

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"
)

// copyE2EOverlay is a minimal base overlay: the shipped Debian 13 image sized to
// the disk floor. It omits the playbook mount + ansible provision so the VM boots
// fast — this test validates copy mechanics, not the Ansible run.
const copyE2EOverlay = `base:
- template:_images/debian-13
cpus: 2
memory: "2GiB"
disk: "` + vm.BaseDiskFloor + `"
`

func TestE2ECopyRoundTrip(t *testing.T) {
	if os.Getenv("LIMA_E2E") == "" {
		t.Skip("set LIMA_E2E=1 (and -tags limae2e) to run the real-Lima copy e2e test")
	}

	cli := New(NewExecRunner())
	const name = "claude-copy-e2e"

	// Clean slate and unconditional teardown.
	_ = cli.Delete(name, true)
	t.Cleanup(func() { _ = cli.Delete(name, true) })

	overlay := filepath.Join(t.TempDir(), "base.yaml")
	if err := os.WriteFile(overlay, []byte(copyE2EOverlay), 0o600); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	if err := cli.Create(name, overlay); err != nil {
		t.Fatalf("create: %v", err)
	}

	ctx := context.Background()

	// Guest scratch directory the transfers target.
	const guestDir = "/tmp/e2e-in"
	var b bytes.Buffer
	if err := cli.Shell(ctx, name, nil, &b, "mkdir", "-p", guestDir); err != nil {
		t.Fatalf("mkdir guest %s: %v\n%s", guestDir, err, b.String())
	}

	// --- Single-file round-trip: upload a host file, download it back, compare.
	const payload = "round-trip payload"
	hostSrc := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(hostSrc, []byte(payload), 0o644); err != nil {
		t.Fatalf("write host src: %v", err)
	}
	if err := cli.Copy(ctx, io.Discard, false, hostSrc, GuestPath(name, guestDir)); err != nil {
		t.Fatalf("upload: %v", err)
	}
	hostDstDir := t.TempDir()
	if err := cli.Copy(ctx, io.Discard, false, GuestPath(name, guestDir+"/hello.txt"), hostDstDir); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(hostDstDir, "hello.txt"))
	if err != nil {
		t.Fatalf("read round-tripped file: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("round-trip content = %q, want %q", got, payload)
	}

	// --- Recursive directory copy: a host dir with one file, uploaded into the
	// guest dir. The destination-is-a-directory contract places `srcdir` INSIDE
	// guestDir, so the file lands at <guestDir>/srcdir/nested.txt.
	const nested = "nested payload"
	srcDir := filepath.Join(t.TempDir(), "srcdir")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir host srcdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "nested.txt"), []byte(nested), 0o644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}
	if err := cli.Copy(ctx, io.Discard, true, srcDir, GuestPath(name, guestDir)); err != nil {
		t.Fatalf("recursive upload: %v", err)
	}
	var catBuf bytes.Buffer
	if err := cli.Shell(ctx, name, nil, &catBuf, "cat", guestDir+"/srcdir/nested.txt"); err != nil {
		t.Fatalf("cat nested guest file: %v\n%s", err, catBuf.String())
	}
	if catBuf.String() != nested {
		t.Fatalf("recursive placement content = %q, want %q (dst-is-a-dir: srcdir nested under dest)", catBuf.String(), nested)
	}

	// And the directory is not SPLATTED: its contents must not appear loose in the
	// destination. That is what the rsync backend does — it drops srcdir and copies
	// what was inside it — and it is why the backend is pinned to scp. Asserting
	// only on the file above is not enough: a splat that ALSO left a correct copy
	// behind would pass, and more to the point, this is the assertion that names the
	// failure. Without it the test says "the file is missing" when the truth is "the
	// file is in the wrong place".
	var splat bytes.Buffer
	err = cli.Shell(ctx, name, nil, &splat, "test", "!", "-e", guestDir+"/nested.txt")
	if err != nil {
		t.Fatalf("a recursive copy must place srcdir INSIDE %s, not splat its contents there: "+
			"%s/nested.txt exists (backend fell back to rsync?)\n%s", guestDir, guestDir, splat.String())
	}
}

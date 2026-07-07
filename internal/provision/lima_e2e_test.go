//go:build limae2e

// These tests boot REAL Lima VMs and so are gated behind the `limae2e` build tag
// (and the LIMA_E2E env var) — they never run in the normal `go test ./...`. They
// cover the reset feature's load-bearing Lima mechanics that the in-process fakes
// can only assert as argv strings:
//
//   - lima.Client.Configure -> `limactl edit --set` actually grows a stopped
//     clone's qcow2 and the Debian image's growpart extends the guest root FS.
//   - provision.StageOut/StageIn -> a `sudo tar` round-trip over `limactl shell`
//     preserves file contents and the 0600 mode and re-chowns to the guest user.
//
// Run (needs limactl + nested virt / KVM; downloads the Debian 13 image once):
//
//	go test -tags limae2e -timeout 30m -run TestE2E ./internal/provision/
//
// (set LIMA_E2E=1 in the environment). Mirrors the 20GiB floor and the
// clone -> Configure -> Start ordering of provision.createVM.
package provision

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/vm"
)

// e2eOverlay is a minimal base overlay: the shipped Debian 13 image sized to the
// disk floor. It deliberately omits the playbook mount + ansible dependency
// provision (RenderBaseOverlay's heavier bits) so the test boots fast — it is
// validating Lima sizing/staging, not the Ansible run.
const e2eOverlay = `base:
- template:_images/debian-13
cpus: 2
memory: "2GiB"
disk: "` + vm.BaseDiskFloor + `"
`

// guestOut runs argv in the guest and returns its trimmed stdout, failing the
// test on error.
func guestOut(t *testing.T, cli *lima.Client, name string, argv ...string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := cli.Shell(context.Background(), name, nil, &buf, argv...); err != nil {
		t.Fatalf("shell %s %v: %v\n%s", name, argv, err, buf.String())
	}
	return strings.TrimSpace(buf.String())
}

func TestE2E_ConfigureGrowsDiskAndStageRoundTrip(t *testing.T) {
	if os.Getenv("LIMA_E2E") == "" {
		t.Skip("set LIMA_E2E=1 (and -tags limae2e) to run the real-Lima e2e tests")
	}

	cli := lima.New(lima.NewExecRunner())
	const base, clone = "claude-e2e-base", "claude-e2e-clone"

	// Start from a clean slate and always tear the instances down.
	_ = cli.Delete(clone, true)
	_ = cli.Delete(base, true)
	t.Cleanup(func() {
		_ = cli.Delete(clone, true)
		_ = cli.Delete(base, true)
	})

	overlay := filepath.Join(t.TempDir(), "base.yaml")
	if err := os.WriteFile(overlay, []byte(e2eOverlay), 0o600); err != nil {
		t.Fatalf("write overlay: %v", err)
	}

	// Build the base at the floor, then stop it so the clone source is quiescent.
	if err := cli.Create(base, overlay); err != nil {
		t.Fatalf("create base: %v", err)
	}
	if err := cli.Stop(base); err != nil {
		t.Fatalf("stop base: %v", err)
	}

	// Clone, then grow the STOPPED clone to 30GiB via the production Configure path.
	if err := cli.Clone(base, clone); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if err := cli.Configure(clone, 2, "2GiB", "30GiB"); err != nil {
		t.Fatalf("configure (edit --set): %v", err)
	}
	if err := cli.Start(clone); err != nil {
		t.Fatalf("start clone: %v", err)
	}

	// growpart must have extended the root FS well past the 20GiB floor.
	sizeGB, err := strconv.Atoi(guestOut(t, cli, clone, "sh", "-c", "df -BG --output=size / | tail -1 | tr -dc 0-9"))
	if err != nil {
		t.Fatalf("parse root FS size: %v", err)
	}
	if sizeGB < 25 {
		t.Fatalf("clone root FS = %dGB, want it grown past the 20GiB floor toward 30GiB", sizeGB)
	}
	if nproc := guestOut(t, cli, clone, "nproc"); nproc != "2" {
		t.Errorf("clone nproc = %q, want 2 (cpus applied by Configure)", nproc)
	}

	// --- Staging round-trip: prove StageOut/StageIn preserve content + 0600 + owner.
	user := guestOut(t, cli, clone, "id", "-un")
	home, err := guestHome(context.Background(), cli, clone, user)
	if err != nil {
		t.Fatalf("guestHome: %v", err)
	}

	// Seed a Claude login with a secret-mode credential and a settings JSON.
	guestOut(t, cli, clone, "sh", "-c",
		"set -e; rm -rf ~/.claude ~/.claude.json; mkdir -p ~/.claude; "+
			"printf SECRET-TOKEN > ~/.claude/.credentials.json; chmod 600 ~/.claude/.credentials.json; "+
			`printf '{"oauth":"keepme"}' > ~/.claude.json`)

	archive := filepath.Join(t.TempDir(), "claude.tgz")
	if err := StageOut(context.Background(), cli, clone, home, []string{".claude", ".claude.json"}, archive); err != nil {
		t.Fatalf("StageOut: %v", err)
	}
	if fi, err := os.Stat(archive); err != nil || fi.Size() == 0 {
		t.Fatalf("StageOut produced no archive: %v", err)
	}

	// Wipe in-guest, then restore from the host archive.
	guestOut(t, cli, clone, "sh", "-c", "rm -rf ~/.claude ~/.claude.json")
	if err := StageIn(context.Background(), cli, clone, home, user, []string{".claude", ".claude.json"}, archive); err != nil {
		t.Fatalf("StageIn: %v", err)
	}

	if got := guestOut(t, cli, clone, "cat", home+"/.claude/.credentials.json"); got != "SECRET-TOKEN" {
		t.Errorf("restored credential = %q, want SECRET-TOKEN", got)
	}
	if got := guestOut(t, cli, clone, "stat", "-c", "%a", home+"/.claude/.credentials.json"); got != "600" {
		t.Errorf("restored credential mode = %q, want 600", got)
	}
	if got := guestOut(t, cli, clone, "stat", "-c", "%U", home+"/.claude/.credentials.json"); got != user {
		t.Errorf("restored credential owner = %q, want %q (re-chowned to the guest user)", got, user)
	}
	if got := guestOut(t, cli, clone, "cat", home+"/.claude.json"); !strings.Contains(got, "keepme") {
		t.Errorf("restored ~/.claude.json = %q, want it to contain keepme", got)
	}
}

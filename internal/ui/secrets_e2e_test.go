//go:build limae2e

// This test boots a REAL Lima VM and so is gated behind the `limae2e` build
// tag (and the LIMA_E2E env var) — it never runs in the normal `go test
// ./...`. It is the secrets subsystem's FIRST end-to-end test.
//
// Every existing secrets test (TestSecretsEditorSaveValidPersists and
// friends, secrets_test.go) drives the real ctrl+s key path and asserts on
// real store state — but only the HOST store. They pass while the feature is
// broken, because "ctrl+s never reaches the guest" is a claim about the
// GUEST, and nothing in-process can observe a guest. This test closes that
// gap: it drives the real 'e' -> type -> ctrl+s key path on a VM that is
// RUNNING and stays running, then reads the saved value back from INSIDE the
// guest via `limactl shell`, without ever restarting the VM.
//
// Run (needs limactl + nested virt / KVM; downloads the Debian 13 image once):
//
//	go test -tags limae2e -timeout 30m -run TestE2E ./internal/ui/
//
// (set LIMA_E2E=1 in the environment). Mirrors the minimal-overlay and
// cleanup discipline of internal/provision/lima_e2e_test.go and
// internal/lima/copy_e2e_test.go.
package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/vm"

	tea "charm.land/bubbletea/v2"
)

// secretsE2EOverlay is a minimal base overlay: the shipped Debian 13 image
// sized to the disk floor. It deliberately omits the playbook mount + ansible
// dependency provision (RenderBaseOverlay's heavier bits) so the VM boots
// fast — this test validates the secrets-apply-on-save path, not the Ansible
// run.
const secretsE2EOverlay = `base:
- template:_images/debian-13
cpus: 2
memory: "2GiB"
disk: "` + vm.BaseDiskFloor + `"
`

// e2eGuestOut runs argv in the guest and returns its trimmed stdout, failing
// the test on error. A local twin of the identical helper in
// internal/provision/lima_e2e_test.go (unexported there, and that package
// does not import into this one).
func e2eGuestOut(t *testing.T, cli *lima.Client, name string, argv ...string) string {
	t.Helper()
	out, err := cli.ShellOut(context.Background(), name, argv...)
	if err != nil {
		t.Fatalf("shell %s %v: %v\n%s", name, argv, err, string(out))
	}
	return strings.TrimSpace(string(out))
}

// e2eRunCmd drains cmd to a concrete actionDoneMsg, unwrapping one level of
// tea.BatchMsg — updateSecrets' running-VM save path batches the real apply
// command with the spinner tick via beginAction, so the raw cmd() result is a
// BatchMsg, not the actionDoneMsg itself.
func e2eRunCmd(t *testing.T, cmd tea.Cmd) actionDoneMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected save on a running VM to dispatch a non-nil command")
	}
	msg := cmd()
	cmds := []tea.Cmd{func() tea.Msg { return msg }}
	if batch, ok := msg.(tea.BatchMsg); ok {
		cmds = batch
	}
	for _, c := range cmds {
		if c == nil {
			continue
		}
		if done, ok := c().(actionDoneMsg); ok {
			return done
		}
	}
	t.Fatalf("no actionDoneMsg produced by the save command, got %#v", msg)
	return actionDoneMsg{}
}

// guestGH reads GH_TOKEN back out of the guest's sourced secrets.env — the
// exact file/mechanism ApplySecrets writes and ~/.profile + ~/.bashrc source
// (internal/provision/secrets.go) — never touching anything cached
// host-side.
func guestGH(t *testing.T, cli *lima.Client, name string) string {
	t.Helper()
	return e2eGuestOut(t, cli, name, "sh", "-c",
		`. "$HOME/.config/sandbar/secrets.env" 2>/dev/null; printf '%s' "$GH_TOKEN"`)
}

// TestE2ESecretsSaveOnRunningVMReachesGuestWithoutRestart is the deliverable
// this task exists for: proof, at the boundary the user actually cares
// about, that ctrl+s in the secrets editor changes a RUNNING VM — not just
// the host-side JSON store. Before the fix, updateSecrets' save path
// returned a nil tea.Cmd, so the guest's ~/.config/sandbar/secrets.env was
// never touched; this test would then observe the guest still holding the
// OLD token (the "dead token live in the guest" bug from the task
// description) even though the save reported success.
func TestE2ESecretsSaveOnRunningVMReachesGuestWithoutRestart(t *testing.T) {
	if os.Getenv("LIMA_E2E") == "" {
		t.Skip("set LIMA_E2E=1 (and -tags limae2e) to run the real-Lima secrets e2e test")
	}

	// Isolate the host-side secrets/registry stores to a temp dir: New() below
	// loads the real secrets.Load()/registry.Load() paths, and without this the
	// test would read and write the developer's actual
	// ~/.local/share/sandbar/secrets.json.
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cli := lima.New(lima.NewExecRunner())
	const name = "claude-secrets-e2e"

	// Clean slate and unconditional teardown.
	_ = cli.Delete(name, true)
	t.Cleanup(func() { _ = cli.Delete(name, true) })

	overlay := filepath.Join(t.TempDir(), "base.yaml")
	if err := os.WriteFile(overlay, []byte(secretsE2EOverlay), 0o600); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	if err := cli.Create(name, overlay); err != nil {
		t.Fatalf("create: %v", err)
	}

	user := e2eGuestOut(t, cli, name, "id", "-un")

	// Seed an OLD value the way a prior sand-initiated save/start already
	// would have, directly through the production ApplySecrets (setup, not
	// the code under test), so the guest starts from a known value that a
	// rotate-and-save must overwrite while the VM stays running throughout —
	// mirroring the "rotating an expired GH_TOKEN leaves the dead token live
	// in the guest" scenario from the task description.
	oldScopes := map[string]map[string]string{"": {"GH_TOKEN": "old-token-e2e"}}
	if err := provision.ApplySecrets(context.Background(), cli, name, user, oldScopes, os.Stderr); err != nil {
		t.Fatalf("seed old secret: %v", err)
	}
	if got := guestGH(t, cli, name); got != "old-token-e2e" {
		t.Fatalf("precondition: guest GH_TOKEN = %q, want the seeded old-token-e2e", got)
	}

	// Build a real model against the real cli — exactly the shape the TUI
	// runs with — then drive the real key path: open the editor with 'e',
	// type the new value, and ctrl+s. m.vms carries the Running status the
	// save path's guest-apply gate reads (m.lookupVM); nothing else about the
	// dispatch is faked.
	prov := &provision.Provisioner{Lima: cli}
	m, ok := New(provider.NewLocalLima(cli, prov)).(model)
	if !ok {
		t.Fatalf("New did not return a model")
	}
	m = resized(m, 100, 30)
	m.vms = []vm.VM{{Name: name, Status: "Running"}}
	m = openSecretsViaKey(t, m, name, "Running")
	m = typeInto(m, "GH_TOKEN=new-token-e2e")

	after, cmd := m.Update(ctrlKey('s'))
	m, ok = after.(model)
	if !ok {
		t.Fatal("ctrl+s did not return a model")
	}
	if m.secretsErr != nil {
		t.Fatalf("save should not have produced a parse/store error: %v", m.secretsErr)
	}
	if m.view != viewBoard {
		t.Fatalf("a valid save should return to the board, got %v", m.view)
	}

	done := e2eRunCmd(t, cmd)
	if done.err != nil {
		t.Fatalf("the guest apply must succeed while the VM is up, got err = %v", done.err)
	}

	// The proof: read the value back from INSIDE the guest, without ever
	// restarting the VM.
	if got := guestGH(t, cli, name); got != "new-token-e2e" {
		t.Fatalf("guest GH_TOKEN = %q after ctrl+s, want %q — the guest was not updated by the save", got, "new-token-e2e")
	}
}

package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/sshconfig"
	"github.com/lullabot/sandbar/internal/vm"
)

// TestLocalProviderEditorSSHIncludesLimaConfig proves local Lima hands back an
// Include of Lima's own per-instance ssh.config rather than a copy of its
// options: Lima picks a fresh forwarded port on every start
// and rewrites that file, so only an Include stays correct across restarts.
func TestLocalProviderEditorSSHIncludesLimaConfig(t *testing.T) {
	p := &limaProvider{}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "ssh.config")
	if err := os.WriteFile(cfg, []byte("Host lima-web\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := p.EditorSSH(context.Background(), vm.VM{Name: "web", Dir: dir})
	if err != nil {
		t.Fatalf("EditorSSH: %v", err)
	}
	want := sshconfig.Entry{Include: cfg}
	if got != want {
		t.Fatalf("EditorSSH = %+v, want %+v", got, want)
	}
}

// TestLocalProviderEditorSSHMissingConfig proves a VM whose ssh.config Lima has
// not written (a half-created instance, or an empty Dir) is an error naming the
// file, not an Include of a path that does not exist: ssh silently skips a
// missing Include, so the user would get only "could not resolve hostname".
func TestLocalProviderEditorSSHMissingConfig(t *testing.T) {
	p := &limaProvider{}
	dir := t.TempDir()

	_, err := p.EditorSSH(context.Background(), vm.VM{Name: "web", Dir: dir})
	if err == nil {
		t.Fatal("EditorSSH succeeded with no ssh.config on disk")
	}
	if !strings.Contains(err.Error(), filepath.Join(dir, "ssh.config")) {
		t.Fatalf("error %q does not name the missing file", err)
	}

	if _, err := p.EditorSSH(context.Background(), vm.VM{Name: "web"}); err == nil {
		t.Fatal("EditorSSH succeeded for a VM with no instance directory")
	}
}

// TestProxmoxEditorSSHEntry proves the editor entry reaches the guest the way
// sandbar's own ssh does — resolved address, cloud-init user, the profile's key
// and the throwaway host-key posture.
func TestProxmoxEditorSSHEntry(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", clusterResources)
	m.data("/nodes/pve1/qemu/100/status/current", `{"vmid":100,"name":"web","status":"running"}`)
	m.data("/nodes/pve1/qemu/100/config", fmt.Sprintf(`{"net0":"virtio=%s,bridge=vmbr0"}`, testMAC))
	m.data("/nodes/pve1/qemu/100/agent/network-get-interfaces", agentInterfaces)
	p := newProxmoxForTest(t, m)
	if _, err := p.List(); err != nil {
		t.Fatalf("List: %v", err)
	}
	// A stale cached address, as left by a restart made outside sandbar: the
	// entry outlives this process, so it must carry what the guest agent
	// reports now (192.168.1.50), not what was cached.
	p.setGuestIP("web", "192.168.1.99")

	got, err := p.EditorSSH(context.Background(), vm.VM{Name: "web"})
	if err != nil {
		t.Fatalf("EditorSSH: %v", err)
	}
	want := sshconfig.Entry{
		HostName:          "192.168.1.50",
		User:              "dev",
		IdentityFile:      "/keys/id_ed25519",
		IdentitiesOnly:    true,
		EphemeralHostKeys: true,
	}
	if got != want {
		t.Fatalf("EditorSSH = %+v, want %+v", got, want)
	}
	// The fresh answer replaced the stale one in the cache, so the next attach
	// connects where the editor does.
	if ip, _ := p.cachedGuestIP("web"); ip != "192.168.1.50" {
		t.Fatalf("cached address = %q after EditorSSH, want the fresh one", ip)
	}
	got.Alias = "sandbar-home-pve-web"
	if _, err := got.Render(); err != nil {
		t.Fatalf("the entry must render: %v", err)
	}
}

// TestProxmoxEditorSSHUnresolvable proves an unresolvable guest is an error,
// never an entry with an empty or guessed HostName: an entry naming the VM
// itself could resolve to an unrelated machine on the LAN.
func TestProxmoxEditorSSHUnresolvable(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", `[]`)
	p := newProxmoxForTest(t, m)

	got, err := p.EditorSSH(context.Background(), vm.VM{Name: "ghost"})
	if err == nil {
		t.Fatalf("EditorSSH = %+v for an unresolvable guest; want an error", got)
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("error %q does not name the VM", err)
	}
}

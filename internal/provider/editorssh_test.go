package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/sshconfig"
	"github.com/lullabot/sandbar/internal/vm"
)

// TestLocalProviderEditorSSHIncludesLimaConfig proves local Lima hands back an
// Include of Lima's own per-instance ssh.config under Lima's own alias rather
// than a copy of its options: Lima picks a fresh forwarded port on every start
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
	want := sshconfig.Entry{Alias: "lima-web", Include: cfg}
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

// TestRemoteProviderEditorSSHIsNotTheLocalOne guards against the embedded local
// implementation leaking through: it would Include v.Dir/ssh.config, a path on
// the REMOTE host, which on the workstation names nothing — or something else.
func TestRemoteProviderEditorSSHIsNotTheLocalOne(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ssh.config"), []byte("Host lima-web\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := NewRemoteLima(TargetConfig{Provider: RemoteLimaProviderID, Host: "example.com", User: "dev", ProfileName: "mini"})
	if err != nil {
		t.Fatalf("NewRemoteLima: %v", err)
	}
	if got := p.(*remoteLimaProvider).profileName; got != "mini" {
		t.Fatalf("remote provider profileName = %q, want %q", got, "mini")
	}

	got, err := p.EditorSSH(context.Background(), vm.VM{Name: "web", Dir: dir})
	if err == nil {
		t.Fatalf("EditorSSH = %+v, want an error until remote Lima is supported", got)
	}
	if got.Include != "" {
		t.Fatalf("remote EditorSSH returned a workstation Include %q", got.Include)
	}
}

// TestProxmoxEditorSSHEntry proves the editor entry reaches the guest the way
// sandbar's own ssh does — resolved address, cloud-init user, the profile's key
// and the throwaway host-key posture — under an alias namespaced by the
// profile, since two profiles can each hold a VM called "web".
func TestProxmoxEditorSSHEntry(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", clusterResources)
	p := newProxmoxForTest(t, m, func(c *TargetConfig) { c.ProfileName = "Home PVE" })
	if _, err := p.List(); err != nil {
		t.Fatalf("List: %v", err)
	}
	p.setGuestIP("web", "192.168.1.50")

	got, err := p.EditorSSH(context.Background(), vm.VM{Name: "web"})
	if err != nil {
		t.Fatalf("EditorSSH: %v", err)
	}
	want := sshconfig.Entry{
		Alias:             "sandbar-home-pve-web",
		HostName:          "192.168.1.50",
		User:              "dev",
		IdentityFile:      "/keys/id_ed25519",
		IdentitiesOnly:    true,
		EphemeralHostKeys: true,
	}
	if got != want {
		t.Fatalf("EditorSSH = %+v, want %+v", got, want)
	}
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

// TestTargetConfigForCarriesProfileName pins the one non-connection field
// TargetConfigFor maps: the profile's name, which the remote providers need to
// namespace editor ssh aliases. It must stay out of Scope, which the registry
// persists and which a rename must not change.
func TestTargetConfigForCarriesProfileName(t *testing.T) {
	for _, typ := range []profiles.Type{profiles.TypeRemoteSSH, profiles.TypeProxmox} {
		p := profiles.Profile{Name: "Home PVE", Type: typ, Host: "h", User: "u", Node: "n", Pool: "p"}
		cfg := TargetConfigFor(p)
		if cfg.ProfileName != p.Name {
			t.Fatalf("%s: ProfileName = %q, want %q", typ, cfg.ProfileName, p.Name)
		}
		renamed := p
		renamed.Name = "other"
		if TargetConfigFor(renamed).Scope() != cfg.Scope() {
			t.Fatalf("%s: a rename changed the scope", typ)
		}
	}
}

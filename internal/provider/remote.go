package provider

import (
	"os"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/vm"
)

// remoteLimaProvider is the remote-Lima-over-SSH backend. It IS the local Lima
// provider (Components 2-3) with only the host-access implementation swapped: its
// lima core and its provisioner both drive limactl over an SSHHost instead of a
// local execRunner, so every lifecycle/transport method — List, Start, Shell,
// Copy, Create, Reset, … — is inherited from limaProvider unchanged and simply
// runs over the hop. The two-stage copy is inherited too: limaProvider.Copy calls
// core.Copy, which detects the SSHHost as a remoteCopier and stages across the hop
// (lima/client.go), so aptcache.go and ui/transfer.go get it with zero drift.
//
// Only the three guest-identity/attach methods genuinely differ, because they
// read the REMOTE host's instance files and prefix the attach with `ssh -t`, so
// they are the only ones overridden here.
type remoteLimaProvider struct {
	*limaProvider
	host *lima.SSHHost
}

var _ Provider = (*remoteLimaProvider)(nil)

// AttachArgv returns the ssh-wrapped attach argv (`ssh -t <host> limactl shell
// --workdir H NAME bash -c <expr>`), resolving the guest home from the REMOTE
// host's cloud-config.yaml and keeping the guest tmux expression byte-for-byte
// identical (host.AttachArgv reuses lima.AttachArgv). This is what gives `sand
// shell` and the TUI `S` verb the remote form with no drift.
func (p *remoteLimaProvider) AttachArgv(v vm.VM) []string {
	return p.host.AttachArgv(v.Name, lima.GuestHomeVia(p.host, v.Dir), os.Getenv("COLORTERM"))
}

// GuestHome / GuestUser read v's instance files off the REMOTE host (via the SSH
// HostFiles), not the local filesystem where they do not exist.
func (p *remoteLimaProvider) GuestHome(v vm.VM) string { return lima.GuestHomeVia(p.host, v.Dir) }
func (p *remoteLimaProvider) GuestUser(v vm.VM) string { return lima.GuestUserVia(p.host, v.Dir) }

// NewRemoteLima builds the remote-Lima-over-SSH provider for cfg. It wires ONE
// SSHHost as both the lima core's Runner and the provisioner's host-access seam,
// so limactl runs on the remote host and the base image / stamp / lock are read
// and written there — the whole difference from local Lima is this one swap.
//
// It calls provision.SetHostFiles because the provisioner's base-image file
// touches go through a package-global seam (see that setter's doc); a single sand
// process runs exactly one provider, so pointing that seam at the remote host on
// construction is correct and does not disturb the default local path, which
// never calls it.
func NewRemoteLima(cfg TargetConfig) (Provider, error) {
	host := lima.NewSSHHost(lima.SSHConfig{
		Host:           cfg.Host,
		User:           cfg.User,
		Port:           cfg.Port,
		IdentityPath:   cfg.IdentityPath,
		RemoteLimaHome: cfg.RemoteLimaHome,
	})
	// PlaybookDir left empty — located lazily on first create/reset (see
	// NewDefault and Provisioner.playbookDir); a remote `sand shell` must not
	// trigger playbook extraction either.
	provision.SetHostFiles(host)
	core := lima.New(host)
	prov := &provision.Provisioner{Lima: core}
	return &remoteLimaProvider{
		limaProvider: &limaProvider{core: core, prov: prov},
		host:         host,
	}, nil
}

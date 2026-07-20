// Package profiles owns the persisted, secret-free connection-profile model
// that replaces the SAND_* environment variables as the
// single source of truth for every location Sandbar can run VMs on. A
// Profile is the permanent Local profile, a RemoteSSH profile, or a Proxmox
// profile; the fleet builder, CLI, and TUI convert a Profile into a
// provider.TargetConfig in the provider layer — this package deliberately
// does not import internal/provider or internal/ui, to avoid an import
// cycle (provider will import profiles, not the other way around).
package profiles

import "fmt"

// Type enumerates the kind of location a Profile connects to. It is modeled
// so it can grow (e.g. a future RemoteDocker), and today spans Local,
// RemoteSSH, and Proxmox.
type Type string

const (
	// TypeLocal is the permanent, always-present local Lima profile.
	TypeLocal Type = "local"
	// TypeRemoteSSH is a remote host reached over SSH (remote Lima).
	TypeRemoteSSH Type = "remote-ssh"
	// TypeProxmox is a Proxmox VE host reached over its REST API.
	TypeProxmox Type = "proxmox"
)

// LocalProfileID is the fixed, reserved ID of the permanent Local profile.
// Other packages can reference "the local profile" deterministically by this
// constant rather than by name (which is renameable).
const LocalProfileID = "local"

// DefaultLocalName is the default, renameable display name seeded for the
// permanent Local profile.
const DefaultLocalName = "local"

// Profile is one location Sandbar can run VMs on. ID is immutable once
// created (generated at creation time — never derived from Name or from the
// connection target, both of which are editable). Name is a renameable
// display label. The RemoteSSH connection fields mirror
// provider.TargetConfig and are secret-free: IdentityPath is a path to a
// private key file on disk, never key material. The Proxmox connection
// fields carry the same invariant: TokenFile is a path to a credential file,
// never credential material. Fields are zero for types that don't use them.
type Profile struct {
	ID      string `yaml:"id"`
	Name    string `yaml:"name"`
	Type    Type   `yaml:"type"`
	Enabled bool   `yaml:"enabled"`

	// RemoteSSH-only connection fields; zero for TypeLocal.
	Host         string `yaml:"host,omitempty"`
	User         string `yaml:"user,omitempty"`
	Port         int    `yaml:"port,omitempty"`
	IdentityPath string `yaml:"identity_path,omitempty"`
	LimaHome     string `yaml:"lima_home,omitempty"`

	// Proxmox-only connection fields; zero for other types. Like IdentityPath,
	// TokenFile is a PATH to a credential file, never credential material — the
	// profiles store is secret-free and must stay that way, because these fields
	// are folded into the registry scope that gets persisted.
	Node      string `yaml:"node,omitempty"`
	Pool      string `yaml:"pool,omitempty"`
	Storage   string `yaml:"storage,omitempty"`
	Bridge    string `yaml:"bridge,omitempty"`
	TokenFile string `yaml:"token_file,omitempty"`
	Insecure  bool   `yaml:"insecure,omitempty"`
	CAFile    string `yaml:"ca_file,omitempty"`
}

// remoteTarget returns the stable, secret-free "user@host:port" identity for
// a RemoteSSH profile, used to detect two profiles that resolve to the same
// connection. This intentionally duplicates the trivial formatting in
// provider.TargetConfig.Scope() (internal/provider/select.go:86) rather than
// importing internal/provider, which would create an import cycle (provider
// converts a Profile into a TargetConfig, not the reverse). Keep the two
// formats in agreement if either changes.
func (p Profile) remoteTarget() string {
	return fmt.Sprintf("%s@%s:%d", p.User, p.Host, p.Port)
}

// proxmoxTarget returns the stable, secret-free "host:node/pool" identity for
// a Proxmox profile, used to detect two profiles that resolve to the same
// pool on the same node — mirroring remoteTarget above. TokenFile is
// deliberately excluded: two profiles pointing at the same pool via
// different token files are still the same target and must still collide.
func (p Profile) proxmoxTarget() string {
	return fmt.Sprintf("%s:%s/%s", p.Host, p.Node, p.Pool)
}

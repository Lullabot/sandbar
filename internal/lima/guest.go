package lima

import (
	"bufio"
	"bytes"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// hostFiles is the host-access seam these guest-identity reads go through. It
// defaults to the local filesystem; the remote-Lima provider (sshhost.go) reads
// the same ssh.config / cloud-config.yaml off the host where the instance lives.
var hostFiles HostFiles = LocalFiles()

// The guest home is read from Lima's generated files rather than guessed, and it
// lives here rather than in one caller because BOTH shell entrypoints need it:
// the TUI's `S` verb and `sand shell` each pass it to AttachArgv as --workdir.
// Duplicating it would be the same drift AGENTS.md warns about for the create
// paths, and getting it wrong is not cosmetic — a --workdir pointing at a
// directory that does not exist in the guest reintroduces the exact
// `bash: cd: … No such file or directory` papercut the flag exists to suppress.
//
// Do not confuse these with provision.guestHome, which answers the same question
// by shelling out to `getent passwd` in a running guest. These read files Lima
// wrote on the host, so they work without the VM running and without a round trip.

// GuestHome returns the guest login user's home directory for the Lima instance
// whose data dir is instanceDir, read from Lima's generated cloud-config.yaml
// (the cloud-init `homedir`). Lima places the guest home at /home/<user>.guest —
// not /home/<user> — so the home cannot be reconstructed from the username. The
// entry matching the ssh.config login user is preferred, otherwise the first
// user carrying a homedir. Returns "" when it can't be determined so the caller
// can fall back.
//
// It reads through the package-default local host-access seam. The remote-Lima
// provider, whose instance files live on another host, reads the
// same cloud-config.yaml over SSH via GuestHomeVia, passing its own HostFiles —
// so the guest home is resolved from wherever the instance actually lives.
func GuestHome(instanceDir string) string { return GuestHomeVia(hostFiles, instanceDir) }

// GuestHomeVia is GuestHome reading through an explicit HostFiles rather than the
// package default. It is the seam the remote-Lima provider uses to resolve the
// guest home off the REMOTE host (where the instance files live) without mutating
// the package-global seam that local callers share.
func GuestHomeVia(hf HostFiles, instanceDir string) string {
	if instanceDir == "" {
		return ""
	}
	data, err := hf.ReadFile(filepath.Join(instanceDir, "cloud-config.yaml"))
	if err != nil {
		return ""
	}
	var doc struct {
		Users []yaml.Node `yaml:"users"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return ""
	}
	want := GuestUserVia(hf, instanceDir) // prefer the entry for the ssh login user
	first := ""
	for i := range doc.Users {
		// The users list can hold a bare "default" string alongside mappings; skip
		// anything that isn't a user mapping.
		if doc.Users[i].Kind != yaml.MappingNode {
			continue
		}
		var u struct {
			Name    string `yaml:"name"`
			Homedir string `yaml:"homedir"`
		}
		if err := doc.Users[i].Decode(&u); err != nil || u.Homedir == "" {
			continue
		}
		if want != "" && u.Name == want {
			return u.Homedir
		}
		if first == "" {
			first = u.Homedir
		}
	}
	return first
}

// GuestUser returns the guest login user for the Lima instance whose data dir is
// instanceDir, parsed from Lima's generated ssh.config ("User <name>"). That is
// the account limactl authenticates as for shell/copy, which Lima may name
// differently from the host user. Returns "" when it can't be determined, so the
// caller can fall back.
func GuestUser(instanceDir string) string { return GuestUserVia(hostFiles, instanceDir) }

// GuestUserVia is GuestUser reading through an explicit HostFiles rather than the
// package default — the seam the remote-Lima provider uses to read the login user
// off the REMOTE host's ssh.config.
func GuestUserVia(hf HostFiles, instanceDir string) string {
	if instanceDir == "" {
		return ""
	}
	data, err := hf.ReadFile(filepath.Join(instanceDir, "ssh.config"))
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		// ssh.config indents directives, e.g. "  User debian".
		if fields := strings.Fields(sc.Text()); len(fields) == 2 && fields[0] == "User" {
			return fields[1]
		}
	}
	return ""
}

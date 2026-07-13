package lima

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

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
func GuestHome(instanceDir string) string {
	if instanceDir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(instanceDir, "cloud-config.yaml"))
	if err != nil {
		return ""
	}
	var doc struct {
		Users []yaml.Node `yaml:"users"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return ""
	}
	want := GuestUser(instanceDir) // prefer the entry for the ssh login user
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
func GuestUser(instanceDir string) string {
	if instanceDir == "" {
		return ""
	}
	f, err := os.Open(filepath.Join(instanceDir, "ssh.config"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// ssh.config indents directives, e.g. "  User debian".
		if fields := strings.Fields(sc.Text()); len(fields) == 2 && fields[0] == "User" {
			return fields[1]
		}
	}
	return ""
}

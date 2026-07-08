// Package vm defines the shared domain model for Claude Code development VMs:
// the VM record reported by Lima and the CreateConfig answers gathered when a
// new VM is provisioned. The lima, provision, and ui packages all consume it.
package vm

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// BaseDiskFloor is the virtual disk size the base image is built at. Clones are
// grown from this floor to their requested size; qcow2 can grow but not shrink
// live, so a small floor is what lets each VM pick (and effectively "shrink" to)
// any size >= the floor without rebuilding the base.
const BaseDiskFloor = "20GiB"

// VM is one Lima instance as reported by `limactl list`.
type VM struct {
	Name     string
	Status   string // Running | Stopped | ...
	CPUs     int
	Memory   string
	Disk     string // virtual/maximum size Lima reports (qcow2 max)
	DiskUsed string // allocated on-disk bytes (raw string); "" = unknown/unmeasurable
	Dir      string
	Arch     string
}

// CreateConfig mirrors the answers the original bash provisioner gathers.
type CreateConfig struct {
	Name            string
	BaseName        string
	Hostname        string
	User            string
	GitName         string
	GitEmail        string
	CPUs            int
	Memory          string
	Disk            string
	Locale          string
	Domain          string
	DockerProxyHost string
	CloneURL        string
	CloneToken      string
}

// DefaultCreateConfig returns the script's defaults (cpus left to caller/host).
func DefaultCreateConfig() CreateConfig {
	return CreateConfig{
		Name:     "claude",
		BaseName: "claude-base",
		Memory:   "8GiB",
		Disk:     "100GiB",
		Domain:   "lan",
		Locale:   "en_US.UTF-8",
		CPUs:     2,
	}
}

// Validate enforces the same required-field and consistency rules as the
// original bash provisioner: a git identity is required, the instance name
// must differ from the base image name, and CPUs must be a positive integer.
func (c CreateConfig) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("instance name is required")
	}
	if c.GitName == "" {
		return fmt.Errorf("git user.name is required: pass --git-name or set it with `git config --global user.name \"...\"`")
	}
	if c.GitEmail == "" {
		return fmt.Errorf("git user.email is required: pass --git-email or set it with `git config --global user.email \"...\"`")
	}
	if c.Name == c.BaseName {
		return fmt.Errorf("instance name %q must differ from base image name %q", c.Name, c.BaseName)
	}
	if c.CPUs < 1 {
		return fmt.Errorf("cpus must be a positive integer (got %d)", c.CPUs)
	}
	return nil
}

// EffectiveHostname defaults to Name when unset; helper used by the form/provisioner.
func (c CreateConfig) EffectiveHostname() string {
	if c.Hostname != "" {
		return c.Hostname
	}
	return c.Name
}

// HostUser returns the primary VM user to default to when one is not given.
// Lima creates a guest user matching the host username, so mirror the original
// bash provisioner (`id -un`, falling back to $USER and then "claude"). It is
// deliberately never empty: an empty user_name passed to Ansible would override
// the user role's default and break in-guest user creation.
func HostUser() string {
	if out, err := exec.Command("id", "-un").Output(); err == nil {
		if u := strings.TrimSpace(string(out)); u != "" {
			return u
		}
	}
	if u := strings.TrimSpace(os.Getenv("USER")); u != "" {
		return u
	}
	return "claude"
}

// HostGitConfig reads a single value from the host git config, best-effort: any
// error (git missing, key unset) yields an empty string. Both the headless
// `sand create` path and the TUI form seed the git identity from here so
// --git-name/--git-email may be omitted when the host already has an identity;
// it is the single source of truth for that default (mirroring HostUser).
func HostGitConfig(key string) string {
	out, err := exec.Command("git", "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ParseCPUs validates the script's "cpus must be a positive integer" rule from a string field.
func ParseCPUs(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("cpus must be a positive integer (got %q)", s)
	}
	return n, nil
}

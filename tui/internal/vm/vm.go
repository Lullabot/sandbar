// Package vm defines the shared domain model for Claude Code development VMs:
// the VM record reported by Lima and the CreateConfig answers gathered when a
// new VM is provisioned. The lima, provision, and ui packages all consume it.
package vm

import (
	"fmt"
	"strconv"
)

// VM is one Lima instance as reported by `limactl list`.
type VM struct {
	Name   string
	Status string // Running | Stopped | ...
	CPUs   int
	Memory string
	Disk   string
	Dir    string
	Arch   string
}

// CreateConfig mirrors the answers new-vm.sh gathers.
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

// Validate enforces the same required-field and consistency rules as new-vm.sh:
// a git identity is required, the instance name must differ from the base image
// name, and CPUs must be a positive integer.
func (c CreateConfig) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("instance name is required")
	}
	if c.GitName == "" {
		return fmt.Errorf("git user.name is required")
	}
	if c.GitEmail == "" {
		return fmt.Errorf("git user.email is required")
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

// ParseCPUs validates the script's "cpus must be a positive integer" rule from a string field.
func ParseCPUs(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("cpus must be a positive integer (got %q)", s)
	}
	return n, nil
}

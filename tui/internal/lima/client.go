// Package lima wraps the limactl CLI behind a small typed Client: listing
// instances and starting, stopping, cloning, creating, deleting, and shelling
// into VMs. All subprocess execution goes through a Runner so the package is
// testable without a real limactl binary.
package lima

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/deviantintegral/claude-code-ansible/tui/internal/vm"
)

// Client exposes the limactl lifecycle operations the TUI needs.
type Client struct{ r Runner }

// New wraps a Runner in a Client.
func New(r Runner) *Client { return &Client{r: r} }

// listEntry mirrors the documented `limactl list --format json` object. Lima
// emits one such object per line. Unknown fields are ignored and missing fields
// decode to their zero value, so the parser is tolerant of Lima version drift.
type listEntry struct {
	Name   string      `json:"name"`
	Status string      `json:"status"`
	CPUs   int         `json:"cpus"`
	Memory json.Number `json:"memory"`
	Disk   json.Number `json:"disk"`
	Dir    string      `json:"dir"`
	Arch   string      `json:"arch"`
}

// List parses `limactl list --format json`, which emits one JSON object per
// line, into a slice of vm.VM. It decodes with a streaming json.Decoder so the
// newline-delimited stream is handled without splitting lines by hand.
func (c *Client) List() ([]vm.VM, error) {
	out, err := c.r.Output(context.Background(), "list", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("limactl list: %w: %s", err, strings.TrimSpace(string(out)))
	}

	var vms []vm.VM
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var e listEntry
		if err := dec.Decode(&e); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("parse limactl list output: %w", err)
		}
		vms = append(vms, vm.VM{
			Name:   e.Name,
			Status: e.Status,
			CPUs:   e.CPUs,
			Memory: e.Memory.String(),
			Disk:   e.Disk.String(),
			Dir:    e.Dir,
			Arch:   e.Arch,
		})
	}
	return vms, nil
}

// Status reports a single instance's status (e.g. "Running", "Stopped").
func (c *Client) Status(name string) (string, error) {
	out, err := c.r.Output(context.Background(), "list", name, "--format", "{{.Status}}")
	if err != nil {
		return "", fmt.Errorf("limactl status %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Start boots a stopped instance.
func (c *Client) Start(name string) error { return c.run("start", name) }

// Stop shuts down a running instance.
func (c *Client) Stop(name string) error { return c.run("stop", name) }

// Delete removes an instance. When force is true it passes -f to skip prompts.
func (c *Client) Delete(name string, force bool) error {
	args := []string{"delete", name}
	if force {
		args = append(args, "-f")
	}
	return c.run(args...)
}

// Clone creates a new instance as a copy of an existing base image.
func (c *Client) Clone(base, name string) error { return c.run("clone", base, name) }

// Create builds and starts a new instance from an overlay/template file.
func (c *Client) Create(name, overlayPath string) error {
	return c.run("start", "--name", name, "--tty=false", overlayPath)
}

// Shell runs a command (or an interactive shell when argv is empty) inside an
// instance, streaming I/O so the caller sees live output.
func (c *Client) Shell(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error {
	args := append([]string{"shell", name}, argv...)
	return c.r.Stream(ctx, stdin, out, args...)
}

// Preflight mirrors new-vm.sh's guards: limactl must be installed and new
// enough to support `limactl clone`.
func (c *Client) Preflight() error {
	ctx := context.Background()
	if _, err := c.r.Output(ctx, "--version"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("limactl not found — install Lima (https://lima-vm.io/docs/installation/): %w", err)
		}
		return fmt.Errorf("limactl --version failed: %w", err)
	}
	if _, err := c.r.Output(ctx, "clone", "--help"); err != nil {
		return fmt.Errorf("your Lima is too old: 'limactl clone' is required — upgrade Lima: https://lima-vm.io/docs/installation/")
	}
	return nil
}

// run executes a fire-and-forget limactl command, folding any captured output
// into the error for diagnostics.
func (c *Client) run(args ...string) error {
	out, err := c.r.Output(context.Background(), args...)
	if err != nil {
		return fmt.Errorf("limactl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

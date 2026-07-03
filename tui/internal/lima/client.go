// Package lima wraps the limactl CLI behind a small typed Client: listing
// instances and starting, stopping, cloning, creating, deleting, and shelling
// into VMs. All subprocess execution goes through a Runner so the package is
// testable without a real limactl binary.
package lima

import (
	"bufio"
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
// line, into a slice of vm.VM. It decodes line by line and skips any line that
// is not a JSON object, so a stray non-JSON line on stdout (a deprecation
// notice, a leaked log line) degrades to "ignored" rather than failing the
// whole listing.
func (c *Client) List() ([]vm.VM, error) {
	out, err := c.r.Output(context.Background(), "list", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("limactl list: %w", err)
	}

	var vms []vm.VM
	sc := bufio.NewScanner(bytes.NewReader(out))
	// Instance dirs and JSON can be long; raise the line limit well above the
	// default 64 KiB so a fat entry is not silently truncated.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue // blank line or non-JSON noise (e.g. a logrus line)
		}
		var e listEntry
		if err := json.Unmarshal(line, &e); err != nil {
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
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read limactl list output: %w", err)
	}
	return vms, nil
}

// Status reports a single instance's status (e.g. "Running", "Stopped").
func (c *Client) Status(name string) (string, error) {
	out, err := c.r.Output(context.Background(), "list", name, "--format", "{{.Status}}")
	if err != nil {
		return "", fmt.Errorf("limactl status %s: %w", name, err)
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

// Configure sets a STOPPED instance's cpus/memory/disk via `limactl edit --set`.
// Applied on next start; disk may only grow (qcow2 cannot shrink live). memory
// and disk are Lima size strings (e.g. "8GiB", "100GiB"). The grow-on-start
// behaviour (qcow2 resize + the Debian image's growpart) is validated manually
// on a real Lima host; the tests only cover command construction.
func (c *Client) Configure(name string, cpus int, memory, disk string) error {
	expr := fmt.Sprintf(`.cpus=%d | .memory=%q | .disk=%q`, cpus, memory, disk)
	return c.run("edit", "--set", expr, name)
}

// Create builds and starts a new instance from an overlay/template file.
func (c *Client) Create(name, overlayPath string) error {
	return c.run("start", "--name", name, "--tty=false", overlayPath)
}

// The streaming variants below mirror Create/Clone/Start/Stop but pipe limactl's
// live output to out and honour ctx, so the provisioner can show base-build and
// boot progress in its pane and a cancelled ctx kills the running limactl. The
// buffered forms above stay for fire-and-forget list actions, which fold stderr
// into the returned error instead of streaming it.

// CreateStreaming builds and boots a new instance from an overlay/template,
// streaming the (slow) image download + first boot to out.
func (c *Client) CreateStreaming(ctx context.Context, name, overlayPath string, out io.Writer) error {
	return c.runStream(ctx, out, "start", "--name", name, "--tty=false", overlayPath)
}

// CloneStreaming copies a base image into a new instance, streaming progress.
func (c *Client) CloneStreaming(ctx context.Context, base, name string, out io.Writer) error {
	return c.runStream(ctx, out, "clone", base, name)
}

// StartStreaming boots a stopped instance, streaming its boot output to out.
func (c *Client) StartStreaming(ctx context.Context, name string, out io.Writer) error {
	return c.runStream(ctx, out, "start", name)
}

// StopStreaming shuts a running instance down, streaming progress to out.
func (c *Client) StopStreaming(ctx context.Context, name string, out io.Writer) error {
	return c.runStream(ctx, out, "stop", name)
}

// Shell runs a command (or an interactive shell when argv is empty) inside an
// instance, streaming I/O so the caller sees live output.
func (c *Client) Shell(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error {
	args := append([]string{"shell", name}, argv...)
	return c.r.Stream(ctx, stdin, out, args...)
}

// Copy wraps `limactl copy`, streaming its verbose output to out. The auto
// backend prefers rsync (resumable) and falls back to scp; -v streams progress;
// -r is used for directory sources. Guest endpoints are formed with GuestPath
// ("<vm>:/path"); host endpoints are plain paths. The caller's contract is that
// dst is always a DIRECTORY and src is placed inside it, so the result is
// identical whether rsync or scp runs. It goes through Runner.Stream so a
// cancelled ctx kills the transfer, exactly like the *Streaming methods.
func (c *Client) Copy(ctx context.Context, out io.Writer, recursive bool, src, dst string) error {
	args := []string{"copy", "-v", "--backend=auto"}
	if recursive {
		args = append(args, "-r")
	}
	args = append(args, src, dst)
	return c.runStream(ctx, out, args...)
}

// GuestPath forms a limactl guest endpoint ("<instance>:<path>") for copy/shell.
// Host endpoints are plain paths and are passed through unchanged by callers.
func GuestPath(instance, path string) string { return instance + ":" + path }

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
	if _, err := c.r.Output(context.Background(), args...); err != nil {
		return fmt.Errorf("limactl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// runStream executes a limactl command with no stdin, streaming its combined
// output to out and honouring ctx. Backs the *Streaming lifecycle methods.
func (c *Client) runStream(ctx context.Context, out io.Writer, args ...string) error {
	if err := c.r.Stream(ctx, nil, out, args...); err != nil {
		return fmt.Errorf("limactl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

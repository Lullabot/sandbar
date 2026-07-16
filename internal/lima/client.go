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

	"github.com/lullabot/sandbar/internal/vm"
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

// ErrListRacedInstanceDir reports that `limactl list` failed ONLY because another
// instance was mid-clone or mid-delete, and not because anything is wrong.
//
// lima-vm/lima#5236: `limactl clone` creates the instance directory before it
// writes that directory's lima.yaml, and `limactl delete` removes the lima.yaml
// before it removes the directory. In both windows `~/.lima/<name>/` exists without
// a readable lima.yaml, and `limactl list` does not skip that instance — it exits 1
// and prints NOTHING, so every other instance vanishes from the listing too.
//
// The window is sub-second for a delete and 40–60s for a clone of a large base
// image, which is most of the time a `sand` create or reset is running. Callers get
// this sentinel so they can keep the listing they already have instead of treating
// a routine clone as a failure.
var ErrListRacedInstanceDir = errors.New("limactl list raced an instance being cloned or deleted")

// listRacePattern is the failure signature, matched against limactl's own stderr
// (which Runner.Output folds into the error). Both halves are required: the message
// names the instance it could not load AND the file it could not find, so a genuine
// "this instance is corrupt" failure — which reports the same missing lima.yaml
// while nothing is being cloned — is not what this matches. It is a narrow read of
// one upstream error string, and if lima#5236 is fixed the string simply stops
// appearing and this stops firing.
func listRacedInstanceDir(err error) bool {
	s := err.Error()
	return strings.Contains(s, "unable to load instance") && strings.Contains(s, "lima.yaml")
}

// List parses `limactl list --format json`, which emits one JSON object per
// line, into a slice of vm.VM. It decodes line by line and skips any line that
// is not a JSON object, so a stray non-JSON line on stdout (a deprecation
// notice, a leaked log line) degrades to "ignored" rather than failing the
// whole listing.
//
// A listing that failed only because another instance is mid-clone or mid-delete
// comes back as ErrListRacedInstanceDir — see that error.
func (c *Client) List() ([]vm.VM, error) {
	out, err := c.r.Output(context.Background(), "list", "--format", "json")
	if err != nil {
		if listRacedInstanceDir(err) {
			return nil, fmt.Errorf("%w: %v", ErrListRacedInstanceDir, err)
		}
		return nil, fmt.Errorf("limactl list: %w", err)
	}

	return parseListJSON(out)
}

// parseListJSON decodes `limactl list --format json` — one JSON object per line —
// shared by List (every instance) and Get (one). Any line that is not a JSON
// object is skipped, so a stray deprecation notice or leaked log line degrades to
// "ignored" rather than failing the whole listing.
func parseListJSON(out []byte) ([]vm.VM, error) {
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

// ErrNoSuchInstance reports that limactl knows no instance by that name.
var ErrNoSuchInstance = errors.New("no such instance")

// Get looks up ONE instance by name, and is what every caller that wants a single
// VM should use instead of scanning List().
//
// It is not a convenience. `limactl list` with no name FAILS OUTRIGHT — exit 1,
// no output — while ANY instance directory is mid-clone or mid-delete
// (lima-vm/lima#5236; see ErrListRacedInstanceDir), a window that lasts 40-60s
// for a clone of a large base image. So a caller that scans the full listing to
// find one VM is broken for the whole time any OTHER VM is being created: that is
// what made `sand shell web` die instantly — and, from a host tmux, close its new
// window before the error could be read — whenever a create was running.
//
// `limactl list <name>` loads only that instance, so a half-written sibling cannot
// take it down with it. Verified against a real limactl: with a broken instance
// dir present, the bare listing exits 1 while the scoped one returns its JSON.
func (c *Client) Get(name string) (vm.VM, error) {
	out, err := c.r.Output(context.Background(), "list", name, "--format", "json")
	if err != nil {
		// limactl says "No instance matching <name> found." / "unmatched instances".
		if strings.Contains(err.Error(), "unmatched instance") || strings.Contains(err.Error(), "No instance matching") {
			return vm.VM{}, fmt.Errorf("%w: %s", ErrNoSuchInstance, name)
		}
		return vm.VM{}, fmt.Errorf("limactl list %s: %w", name, err)
	}
	vms, err := parseListJSON(out)
	if err != nil {
		return vm.VM{}, err
	}
	for _, v := range vms {
		if v.Name == name {
			return v, nil
		}
	}
	return vm.VM{}, fmt.Errorf("%w: %s", ErrNoSuchInstance, name)
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

// Configure sets a STOPPED clone's cpus/memory/disk — and strips any writable
// mount the clone inherited from its base.
//
// Clones inherit the base's lima.yaml wholesale (`limactl clone` copies the
// whole instance dir), so ANY writable mount RenderBaseOverlay ever puts on
// the base (internal/provision/overlay.go) arrives here whether wanted or
// not. Work VMs must carry NO writable host mount — that is the invariant
// that makes "delete the VM and everything it produced is gone" true, and it
// does not hold by itself; it holds because this strip runs on every clone,
// every time. RenderBaseOverlay does not currently add a writable mount
// (a writable apt-archive-cache mount was tried and backed out — see that
// function's doc comment — in favour of an `limactl copy` seed/harvest that
// needs no mount at all), so today this strip has nothing to remove. It stays
// anyway, as a standing guard: the day someone adds a writable mount back to
// the base overlay, this is what stops it from silently reaching every clone.
//
// The strip selects OUT any mount with `writable: true`, rather than removing
// one specific mount by mountPoint. That guarantees the property we actually
// want — no writable mount, full stop — so a future writable mount added to
// the base overlay is stripped automatically instead of silently surviving
// because nobody updated this expression to name it. The read-only playbook
// mount is preserved: finalize rsyncs from /mnt/playbook inside the clone.
//
// Applied on next start; disk may only grow (qcow2 cannot shrink live). memory
// and disk are Lima size strings (e.g. "8GiB", "100GiB"). The grow-on-start
// behaviour (qcow2 resize + the Debian image's growpart) and the yq mount
// filter are validated manually against a real Lima host; the unit tests only
// cover command construction and, where limactl is on PATH, a real
// `limactl edit` round trip (see TestConfigureStripsWritableMountAgainstRealLimactl
// in configure_strip_test.go) — do not remove either without replacing it.
func (c *Client) Configure(name string, cpus int, memory, disk string) error {
	expr := fmt.Sprintf(
		`.cpus=%d | .memory=%q | .disk=%q | .mounts |= map(select(.writable != true))`,
		cpus, memory, disk)
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

// ShellStreamOut runs a command inside an instance, streaming its stdout ONLY
// to out while keeping stderr separate (folded into the error on failure).
// Unlike Shell — which merges stdout and stderr into out for live display —
// this is the right call when out receives a binary/parseable payload, e.g. a
// `tar -czf -` stream piped straight into an archive file: merging limactl's
// `cd` warning on stderr would corrupt the archive. stdin feeds the command's
// input (nil for none).
func (c *Client) ShellStreamOut(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error {
	args := append([]string{"shell", name}, argv...)
	return c.r.StreamOut(ctx, stdin, out, args...)
}

// ShellOut runs a command inside an instance and returns its stdout ONLY, with
// the guest's stderr kept separate (folded into the error on failure). Unlike
// Shell — which streams stdout and stderr merged into one writer for live
// display — this is the right call whenever the caller PARSES the output:
// `limactl shell` injects a `cd <host-cwd>` into the guest login shell and, when
// that directory does not exist in the guest, the resulting `bash: cd: … No such
// file or directory` warning would otherwise be merged into (and corrupt) the
// parsed stdout.
func (c *Client) ShellOut(ctx context.Context, name string, argv ...string) ([]byte, error) {
	args := append([]string{"shell", name}, argv...)
	return c.r.Output(ctx, args...)
}

// Copy wraps `limactl copy`, streaming its verbose output to out. -v streams
// progress; -r is used for directory sources. Guest endpoints are formed with
// GuestPath ("<vm>:/path"); host endpoints are plain paths. The caller's contract
// is that dst is always a DIRECTORY and src is placed inside it. It goes through
// Runner.Stream so a cancelled ctx kills the transfer, exactly like the
// *Streaming methods.
//
// The backend is PINNED TO SCP, and the pin is load-bearing: under limactl 2.1.3
// the choice of backend changes WHERE THE FILES LAND, not merely how fast they get
// there.
//
//   - scp copies the source directory INTO the destination:
//     `srcdir` + `vm:/dst` → /dst/srcdir/… — the contract above.
//   - rsync copies the source's CONTENTS into the destination, dropping the
//     directory itself: /dst/… — because Lima appends a trailing slash to every
//     path of a recursive copy (pkg/copytool/rsync.go), and to rsync `srcdir/`
//     means "the contents of srcdir".
//
// `--backend=auto` picks rsync when it is present on BOTH host and guest, so an
// upload of ~/project would nest correctly on a guest without rsync and splat its
// contents loose into the destination on a guest with it. Placement that depends
// on which packages a sandbox happens to have installed is not a contract at all,
// and the failure is silent: the bytes arrive, just not where the user put them.
// Determinism is worth more here than rsync's resumability — sand copies project
// files between a laptop and a local VM, not archives over a wide-area link.
func (c *Client) Copy(ctx context.Context, out io.Writer, recursive bool, src, dst string) error {
	// When limactl runs on ANOTHER host (the SSH host-access implementation),
	// `limactl copy`'s host endpoint is THAT host's filesystem, not ours, so a
	// host<->guest transfer has to be staged across the hop (local <-> remote host
	// <-> guest). A Runner whose limactl runs remotely takes over the whole copy —
	// staging plus the far-side `limactl copy` — via remoteCopier; the local
	// execRunner does not implement it and falls through to the direct single-stage
	// copy below. Keeping the delegation HERE, rather than in each caller, is what
	// lets aptcache.go, ui/transfer.go, and every other Copy caller inherit the
	// two-stage topology in exactly one place — and the `--backend=scp` argv stays
	// built once in copyLimactlArgv, shared by both transports.
	if rc, ok := c.r.(remoteCopier); ok {
		return rc.copyAcrossHop(ctx, out, recursive, src, dst)
	}
	return streamCopy(ctx, c.r, out, recursive, src, dst)
}

// remoteCopier is implemented by a Runner whose limactl runs on a different host,
// so it must stage a host-local copy endpoint across the hop before invoking
// `limactl copy` there. Client.Copy consults it (the local execRunner does not
// implement it, so nothing changes for local Lima). The method is unexported so
// only this package's SSH host-access implementation can satisfy it.
type remoteCopier interface {
	copyAcrossHop(ctx context.Context, out io.Writer, recursive bool, src, dst string) error
}

// copyLimactlArgv builds the `limactl copy` argv, with the load-bearing
// --backend=scp pin (see Client.Copy's doc for why scp, not rsync/auto). It is
// shared by the local single-stage copy and the SSH host-access two-stage copy so
// the backend pin and the recursive flag are spelled ONCE and cannot drift between
// the two transports.
func copyLimactlArgv(recursive bool, src, dst string) []string {
	args := []string{"copy", "-v", "--backend=scp"}
	if recursive {
		args = append(args, "-r")
	}
	return append(args, src, dst)
}

// streamCopy runs one `limactl copy` invocation through r, applying the
// scpDebugFilter to out (-v also switches on ssh's debug1 chatter, which the
// filter drops). It is the single place `limactl copy` is actually executed —
// for both the local transport and the remote host end of the two-stage copy —
// so the filter and error wrapping live in one spot too.
func streamCopy(ctx context.Context, r Runner, out io.Writer, recursive bool, src, dst string) error {
	args := copyLimactlArgv(recursive, src, dst)
	wrap := func(err error) error {
		if err != nil {
			return fmt.Errorf("limactl %s: %w", strings.Join(args, " "), err)
		}
		return nil
	}
	if out == nil {
		return wrap(r.Stream(ctx, nil, nil, args...))
	}
	f := &scpDebugFilter{w: out}
	err := wrap(r.Stream(ctx, nil, f, args...))
	if ferr := f.Flush(); err == nil {
		err = ferr
	}
	return err
}

// GuestPath forms a limactl guest endpoint ("<instance>:<path>") for copy/shell.
// Host endpoints are plain paths and are passed through unchanged by callers.
func GuestPath(instance, path string) string { return instance + ":" + path }

// Preflight mirrors the original bash provisioner's guards: limactl must be
// installed and new enough to support `limactl clone`.
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

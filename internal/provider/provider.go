// Package provider is sand's backend-agnostic seam: a single Provider interface
// that owns the whole VM lifecycle (discovery, power, provisioning), the guest
// transport (exec and copy), and the interactive attach argv, so every consumer
// depends on one thing that can be satisfied by more than one backend.
//
// Today there is exactly one implementation — the local Lima provider
// (NewLocalLima), which composes the seam-backed lima core and the existing
// provision.Provisioner — and its behaviour is identical to sand's current
// direct use of *lima.Client. A remote-Lima-over-SSH provider (plan 15 task 5)
// will satisfy the SAME interface by running limactl on another host; the
// future proxmox / DigitalOcean / Linode backends plug in the same way. The
// interface is a faithful ENVELOPE of what sand already does — every method maps
// to a current *lima.Client method, a free provision function, or a lima helper,
// and nothing speculative is added.
//
// Deliberately NOT on this interface: the base-image machinery internals
// (limactl clone / edit / the streaming create-and-clone) that only the
// provisioner drives. Those stay behind the Lima core and are reached by the
// local provider's provisioner field, never by a consumer, so the interface
// does not leak base-build concerns that a non-Lima backend would not share.
package provider

import (
	"context"
	"io"

	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/vm"
)

// Provider is the whole backend seam a consumer depends on. Method groups:
// discovery, power, provisioning lifecycle, guest transport, interactive attach
// + guest paths, and preflight. No method exposes a Lima-only type — the surface
// is expressed in vm.VM, provision intent structs, strings, and io types — so a
// non-Lima backend can implement it without inheriting Lima's vocabulary. The
// one carried-over Lima detail is vm.VM.Dir, which is treated as a provider-
// OPAQUE instance-directory handle: a consumer passes the vm.VM it is acting on
// and the provider reaches into Dir itself (for local Lima it is a ~/.lima path;
// for remote Lima a path on the remote host), so no consumer ever builds a
// Lima-shaped path or command by hand.
type Provider interface {
	// --- Discovery ---

	// List returns every instance the backend knows about. A listing that failed
	// only because another instance was mid-clone/-delete comes back as
	// lima.ErrListRacedInstanceDir (see that error) — callers keep the fleet they
	// already have rather than treating a routine clone as a failure.
	List() ([]vm.VM, error)
	// Get looks up ONE instance by name, returning lima.ErrNoSuchInstance when the
	// backend knows no such instance. It is not a convenience over List: scanning a
	// full listing to find one VM is broken for the 40-60s any OTHER instance is
	// mid-clone (lima#5236), so a single-VM lookup must ask about that VM alone.
	Get(name string) (vm.VM, error)
	// Status reports a single instance's status (e.g. "Running", "Stopped").
	Status(name string) (string, error)

	// --- Power ---

	// Start boots a stopped instance (buffered; for fire-and-forget actions).
	Start(name string) error
	// Stop shuts down a running instance (buffered).
	Stop(name string) error
	// Delete removes an instance; force skips limactl's prompts.
	Delete(name string, force bool) error
	// StartStreaming boots a stopped instance, streaming its boot output to out and
	// honouring ctx (a cancelled ctx kills the running command).
	StartStreaming(ctx context.Context, name string, out io.Writer) error
	// StopStreaming shuts a running instance down, streaming progress to out.
	StopStreaming(ctx context.Context, name string, out io.Writer) error

	// --- Provisioning lifecycle ---

	// Create ensures a base image exists, clones it into cfg.Name, sizes and boots
	// it, and runs the finalize pass — the full create. It refuses an existing
	// target. opts carries per-run intent (e.g. Rebuild the base). Streams progress
	// to out and honours ctx.
	Create(ctx context.Context, cfg vm.CreateConfig, opts provision.CreateOptions, out io.Writer) error
	// Recreate force-deletes cfg.Name and re-clones it from the base — a fast reset
	// of one VM that skips Create's exists-guard. opts carries the base intent so
	// create --recreate --rebuild can ask for both.
	Recreate(ctx context.Context, cfg vm.CreateConfig, opts provision.CreateOptions, out io.Writer) error
	// Reset recreates a managed VM from a (possibly edited) config, optionally
	// preserving the Claude login and/or the per-org project tree across the
	// destroy/recreate (opts). Streams progress to out.
	Reset(ctx context.Context, cfg vm.CreateConfig, opts provision.ResetOptions, out io.Writer) error

	// --- Guest transport ---

	// Shell runs argv (or an interactive shell when argv is empty) inside an
	// instance, streaming stdin in and stdout+stderr MERGED to out for live display.
	Shell(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error
	// ShellStreamOut runs argv inside an instance, streaming its stdout ONLY to out
	// and keeping stderr separate (folded into the error). The right call when out
	// receives a binary/parseable payload (e.g. a `tar -czf -` stream) that a merged
	// stderr warning would corrupt.
	ShellStreamOut(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error
	// ShellOut runs argv inside an instance and returns its stdout ONLY, with stderr
	// kept separate (folded into the error). The right call whenever the caller
	// PARSES the output (limactl injects a `cd <host-cwd>` warning on stderr that
	// would otherwise corrupt a parse).
	ShellOut(ctx context.Context, name string, argv ...string) ([]byte, error)
	// Copy transfers src to dst (one of which is a guest endpoint from GuestPath),
	// streaming progress to out; recursive is set for directory sources. The
	// caller's contract is that dst is a DIRECTORY and src is placed inside it. A
	// cancelled ctx kills the transfer.
	Copy(ctx context.Context, out io.Writer, recursive bool, src, dst string) error

	// --- Interactive attach & guest paths ---

	// AttachArgv returns the full argv that attaches a caller to v's persistent
	// guest shell session, resolving the guest home from v itself so the caller
	// never hand-builds it. The caller execs it against a real TTY. For local Lima
	// this is a `limactl shell …` form; a remote provider returns an `ssh -t …`
	// wrapper of the same guest expression.
	AttachArgv(v vm.VM) []string
	// GuestHome returns v's guest login-user home directory, read from the backend's
	// instance files (for local Lima, Lima's generated cloud-config.yaml). Returns
	// "" when it cannot be determined so the caller can fall back.
	GuestHome(v vm.VM) string
	// GuestUser returns v's guest login user, read from the backend's instance files
	// (for local Lima, Lima's generated ssh.config). Returns "" when it cannot be
	// determined.
	GuestUser(v vm.VM) string
	// GuestPath forms a guest transport endpoint for Copy/Shell against the named
	// instance ("<instance>:<path>" for Lima). Host endpoints are plain paths passed
	// through unchanged.
	GuestPath(name, path string) string

	// --- Host identity & capacity ---

	// HostUser is the login user of the host limactl runs on — the user Lima
	// creates a matching guest account for, and therefore the account `limactl
	// shell` logs into. A new VM's user must DEFAULT to this so the playbook
	// provisions (git identity, ~/.tmux.conf, secrets) the same user the shell
	// lands in. For local Lima it is this machine's user; a remote provider
	// returns the REMOTE host's user — NOT the laptop's, which would leave the
	// guest login user unprovisioned (its ~/.tmux.conf missing, so tmux falls back
	// to its default prefix). "" when it cannot be determined, so the caller falls
	// back to the local default.
	HostUser() string

	// HostResources reports the limactl HOST's own capacity — CPU count, total
	// memory, and free disk on the Lima store — for the board header's "how much of
	// my machine are the sandboxes eating" denominators. For local Lima that host is
	// this machine, and the provider returns the ZERO value so the UI keeps sampling
	// it directly (the platform probes live in the ui package); a remote provider
	// samples the REMOTE host over ssh, since that is where the VMs actually run.
	// Any field the backend cannot determine is left 0 ("unknown") and the header
	// drops that clause.
	HostResources() HostResources

	// --- Preflight ---

	// Preflight verifies the backend is usable before any lifecycle op (for Lima:
	// limactl is installed and new enough to support `limactl clone`).
	Preflight() error
}

// HostResources is the limactl host's own capacity, used only for the board
// header's denominators (see Provider.HostResources). A zero field means
// "unknown" — the header drops the corresponding clause rather than showing a
// fabricated total.
type HostResources struct {
	CPUs          int
	MemBytes      int64
	DiskFreeBytes int64
}

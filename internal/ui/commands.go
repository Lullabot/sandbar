package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/paste"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"

	tea "charm.land/bubbletea/v2"
)

// Message types flowing through Update. Every blocking lima/provision call is
// wrapped in a tea.Cmd that returns one of these — Update itself never blocks.
type (
	// vmsLoadedMsg carries the result of one FLEET MEMBER's List refresh. scope
	// is the identity of the member the result belongs to, so Update routes it
	// to the right sub-state — a fleet lists every member on its own tea.Cmd,
	// and a bare (untagged) result would be ambiguous the moment more than one
	// profile is live. A zero-value scope routes to the active member (see
	// model.routeIndex), which is what lets a test drive its single member with
	// a bare vmsLoadedMsg{...}.
	vmsLoadedMsg struct {
		scope registry.Scope
		vms   []vm.VM
		err   error
		// The host's own capacity, sampled in the same command for the same reason as
		// the per-VM stats: hostMemBytes reads /proc/meminfo and hostDiskFree statfs's
		// the Lima volume, and the header called BOTH on every render. Zero means "not
		// sampled" — a test that hands the model a vmsLoadedMsg by hand — and the
		// header falls back to probing directly (see hostCapacityText).
		hostMem      int64
		hostDiskFree int64
		// hostCPUs is the limactl host's core count, sampled the same way. Zero
		// means "not sampled" and the header falls back to the local core count.
		// For a remote provider these three are the REMOTE host's totals.
		hostCPUs int
		// hostUser is the limactl host's login user — the account Lima creates the
		// guest for and `limactl shell` logs into, so the create form defaults a new
		// VM's user to it. For a remote provider it is the REMOTE host's user.
		hostUser string

		// hostMemAvail is the host's AVAILABLE memory (/proc/meminfo's
		// MemAvailable, read through the member's own HostFiles — see
		// hostwarn.go's hostMemAvailBytes), and hostDiskTotal is the TOTAL size
		// of the volume hostDiskFree is measured against. Both are new: the
		// low-capacity-warning feature needs a free% for each resource, and
		// hostMem/hostDiskFree alone are only one half of that. Zero means "not
		// sampled" (no /proc/meminfo on this host, local or remote — a macOS
		// box has none) exactly like their siblings above, and a warning check
		// must never compute a percentage from one.
		hostMemAvail  int64
		hostDiskTotal int64

		// provenance is this member's provenance map, fetched in the SAME command
		// as vms via the provider's batched Provenancer.Provenance — one host
		// round trip, never one per VM (see refreshCmd). nil when the provider
		// does not implement Provenancer or the batched read failed; either way
		// the board falls back to the legacy registry gate for every VM.
		provenance map[string]provider.Provenance
		// provenanceErr is WHY the batched read failed, when it did. It never
		// fails the refresh — provenance simply stays nil and the legacy gate
		// answers — it exists so that degradation is SAID rather than silently
		// returning the fleet to one-view-per-controller. See
		// member.provenanceWarned.
		provenanceErr error
	}
	// actionDoneMsg reports a lifecycle action (start/stop/restart/delete). name
	// is the affected instance, so the model can update the managed registry.
	// scope is the owning member's scope, so the handler prunes (on delete) and
	// refreshes the RIGHT profile — a delete fired on a remote VM must never
	// prune the active/local scope's same-named state. A zero-value scope falls
	// back to the active member (tests). warn carries a non-fatal problem
	// (currently: a failed ApplySecrets) that must NOT be treated as a failure —
	// err staying nil is what tells the handler in model.go the action itself
	// succeeded.
	actionDoneMsg struct {
		action string
		name   string
		scope  registry.Scope
		err    error
		warn   string
	}
	// provisionOutputMsg is one chunk of streamed output from ONE job. It used to
	// be a bare string — which is precisely why only one job could ever exist: a
	// chunk that does not say which run it came from can only be appended to a
	// single global buffer. The jobKey (VM + kind) is what lets N jobs stream at
	// once, each into its own log — including a VM's build and a copy against that
	// same VM, which are two runs and must not share a buffer.
	provisionOutputMsg struct {
		job   jobKey
		chunk string
	}
	// provisionDoneMsg signals that ONE job's goroutine finished. Keyed for the
	// same reason.
	provisionDoneMsg struct {
		job jobKey
		err error
	}
	// pasteResultMsg reports the outcome of the `v` paste-image verb:
	// PasteImage's Result, or an error if the guest write itself failed. name is
	// the VM the verb fired on — the command-registry argument, never
	// m.detail — so the status line always describes the tile that actually
	// acted, not whatever VM a stale field happened to hold. This is a plain
	// result, not routed through actionDoneMsg/beginAction: a single small
	// clipboard write has no progress or cancel to track, so it does not need
	// the job registry's spinner machinery.
	pasteResultMsg struct {
		name   string
		result paste.Result
		err    error
	}
)

// refreshCmd lists ONE fleet member off the Update goroutine, tagging the
// result with that member's scope so Update routes it to the right sub-state.
// Each member gets its own refreshCmd, so a slow or unreachable remote never
// blocks another member's list (or the UI) — the whole point of the async
// fleet.
//
// It measures each VM's real disk consumption AND its up/last-used times here in
// the command, through THIS MEMBER's host-access seam (hf) — a blocking per-host
// stat that must NOT run in Update, so an unresponsive mount (stale NFS, a
// wedged SSH host) can't stall the Bubble Tea event loop, and so a remote VM's
// files are stat'd on the REMOTE host while a local VM's are stat'd locally, in
// the same refresh. This is what retired the ui.hostFiles process-global: the
// seam is the member's, resolved per VM by its owning profile. A non-positive
// disk result leaves DiskUsed empty so the cell renders blank.
//
// When preflight is true it runs the backend's Preflight FIRST (startup, and an
// errored member's self-heal retry) so a wedged handshake surfaces as an error
// binding rather than a blocked list; a member already connected skips it.
func refreshCmd(sc registry.Scope, prov provider.Provider, hf lima.HostFiles, preflight bool) tea.Cmd {
	// Captured HERE — synchronously, on the caller's own goroutine — rather
	// than read again inside the closure below, which runs on its OWN
	// goroutine once Bubble Tea (or a test's teaLoop) executes the returned
	// tea.Cmd. These four are test-only seams (header.go) that pinHostCapacity
	// swaps between tests; reading the package var lazily from that spawned
	// goroutine raced against a LATER test's swap whenever an earlier test's
	// goroutine outlived it (teaLoop, jobs_test.go, does not join every
	// goroutine it starts) — caught by `go test -race`. Capturing local copies
	// here closes it: the spawned goroutine below touches only these locals,
	// never the mutable package vars.
	memFn, diskFreeFn, diskTotalFn, memAvailFn := hostMemBytesFn, hostDiskFreeFn, hostDiskTotalFn, hostMemAvailFn
	return func() tea.Msg {
		if prov == nil {
			// An error binding: nothing to list. Surfaced as an errored member.
			return vmsLoadedMsg{scope: sc, err: errNoProvider}
		}
		if preflight {
			if err := prov.Preflight(); err != nil {
				return vmsLoadedMsg{scope: sc, err: err}
			}
		}
		vms, err := prov.List()
		if err == nil {
			for i := range vms {
				if n := diskUsedBytes(hf, vms[i].Dir); n > 0 {
					vms[i].DiskUsed = strconv.FormatInt(n, 10)
				}
				if vms[i].Status == limaRunning {
					if t, ok := upSince(hf, vms[i].Dir); ok {
						vms[i].UpSince = t
					}
				} else if t, ok := lastUsed(hf, vms[i].Dir); ok {
					vms[i].LastUsed = t
				}
			}
		}
		// Host capacity for the header's denominators. A remote provider reports the
		// REMOTE host's totals (over ssh); the local provider returns the zero value,
		// so each field falls back to sampling THIS machine directly — the unchanged
		// local behaviour. Sampled here, off the Update goroutine, because the remote
		// case is a blocking ssh round trip.
		res := prov.HostResources()
		mem := res.MemBytes
		if mem == 0 {
			mem = memFn()
		}
		disk := res.DiskFreeBytes
		if disk == 0 {
			disk = diskFreeFn()
		}
		diskTotal := res.DiskTotalBytes
		if diskTotal == 0 {
			diskTotal = diskTotalFn()
		}
		// The host's available memory, for the low-capacity-warning feature's
		// free% (hostwarn.go). Sampled through THIS member's own HostFiles —
		// local or the (now-multiplexed, so cheap) ssh seam — exactly like the
		// per-VM disk/up-since/last-used sampling above, rather than through
		// Provider.HostResources: it must resolve identically for either kind
		// of member, and !ok (no /proc/meminfo at all) leaves it 0, "not
		// sampled" — never a guessed number.
		memAvail, _ := memAvailFn(hf)
		// Provenance is fetched HERE, in the same off-Update-goroutine command as
		// List/HostResources above, via the provider's BATCHED read — one host
		// round trip for the whole member, never one per VM. A provider that does
		// not implement Provenancer (a test double, or a future backend with no
		// marker facility) simply yields no markers, and a failed read degrades
		// the same way rather than failing the refresh: either way every VM falls
		// back to the legacy registry gate until it re-provisions and picks up a
		// marker.
		var provenance map[string]provider.Provenance
		var provenanceErr error
		if pv, ok := prov.(provider.Provenancer); ok {
			mp, pErr := pv.Provenance(context.Background())
			if pErr == nil {
				provenance = mp
			} else {
				// Reported, not swallowed. The refresh still succeeds — the
				// fallback keeps the board usable — but the handler logs it ONCE
				// so a broken marker read cannot masquerade as "these are simply
				// the VMs on this host".
				provenanceErr = pErr
			}
		}
		return vmsLoadedMsg{
			scope: sc, vms: vms, err: err,
			hostMem: mem, hostDiskFree: disk, hostCPUs: res.CPUs, hostUser: prov.HostUser(),
			hostMemAvail: memAvail, hostDiskTotal: diskTotal,
			provenance: provenance, provenanceErr: provenanceErr,
		}
	}
}

// errNoProvider is what an error binding's member reports as its list error: its
// provider failed to construct, so there is nothing to list. It is surfaced as
// the member's lastErr (the per-profile status bar) rather than crashing the fleet.
var errNoProvider = fmt.Errorf("connection profile could not be constructed")

// startCmd boots a stopped VM and then writes its host-stored secrets into
// the guest. A secrets failure is reported as a warning, not a failure: a VM
// that is up without its secrets is more useful than one reported as
// failed-to-start. If Start itself fails, ApplySecrets is never attempted.
//
// Note: a VM started outside sand (a bare `limactl start`) does not get
// freshly applied secrets — it sources whatever secrets.env was last written
// by a previous sand-initiated start (or none, if there never was one).
func startCmd(p provider.Provider, scope registry.Scope, name, user string, scopes map[string]map[string]string) tea.Cmd {
	return func() tea.Msg {
		if err := p.Start(name); err != nil {
			return actionDoneMsg{action: "start", name: name, scope: scope, err: err}
		}
		warn := ""
		if err := provision.ApplySecrets(context.Background(), p, name, user, scopes, io.Discard); err != nil {
			warn = "secrets not applied: " + err.Error()
		}
		return actionDoneMsg{action: "start", name: name, scope: scope, warn: warn}
	}
}

// stopCmd shuts a running VM down.
func stopCmd(p provider.Provider, scope registry.Scope, name string) tea.Cmd {
	return func() tea.Msg {
		return actionDoneMsg{action: "stop", name: name, scope: scope, err: p.Stop(name)}
	}
}

// restartCmd stops then starts a VM, surfacing the first failure, and applies
// secrets after a successful start — same warn-not-fail semantics as startCmd.
// This is not redundant with startCmd: restartCmd drives cli.Stop/cli.Start
// directly rather than re-dispatching startCmd, so it would otherwise skip
// the apply step entirely.
func restartCmd(p provider.Provider, scope registry.Scope, name, user string, scopes map[string]map[string]string) tea.Cmd {
	return func() tea.Msg {
		if err := p.Stop(name); err != nil {
			return actionDoneMsg{action: "restart", name: name, scope: scope, err: err}
		}
		if err := p.Start(name); err != nil {
			return actionDoneMsg{action: "restart", name: name, scope: scope, err: err}
		}
		warn := ""
		if err := provision.ApplySecrets(context.Background(), p, name, user, scopes, io.Discard); err != nil {
			warn = "secrets not applied: " + err.Error()
		}
		return actionDoneMsg{action: "restart", name: name, scope: scope, warn: warn}
	}
}

// applySecretsCmd writes name's stored secrets into the guest without
// starting or stopping anything. It backs two seams:
//
//   - The create/reset follow-up: createVM and Reset each end with their own
//     StartStreaming (so the VM is already up by the time provisionDoneMsg
//     fires), and by then a create-form GH_TOKEN has just landed in the store —
//     so this pushes it in rather than waiting for the VM's *next* start.
//   - The secrets editor's save path (updateSecrets, secrets.go), gated on the
//     VM being Running.
//
// Unlike startCmd/restartCmd — where a failed apply is a non-fatal warning
// because the VM itself already started/stopped successfully — this command's
// entire job IS the apply, so its failure is reported as a real error
// (actionDoneMsg.err), not swallowed into a warning next to a false "ok".
func applySecretsCmd(p provider.Provider, scope registry.Scope, name, user string, scopes map[string]map[string]string) tea.Cmd {
	return func() tea.Msg {
		err := provision.ApplySecrets(context.Background(), p, name, user, scopes, io.Discard)
		return actionDoneMsg{action: "apply secrets", name: name, scope: scope, err: err}
	}
}

// secretsFor returns the guest user and the VM's full scope map (global plus
// any directory-scoped secrets) FOR THE GIVEN SCOPE, defaulting the user to the
// host username when the VM has no recorded config (mirroring openResetForm's
// fallback in detail.go). scope is the owning member's — a fleet may hold a
// same-named VM under two profiles, each with its own recorded config and
// secrets, so the caller passes the scope of the VM it is acting on.
func (m model) secretsFor(scope registry.Scope, name string) (user string, scopes map[string]map[string]string) {
	user = vm.HostUser()
	if cfg, ok := m.reg.ConfigInScope(name, scope); ok && cfg.User != "" {
		user = cfg.User
	}
	return user, m.sec.GetAll(name, scope)
}

// deleteCmd force-removes a VM.
func deleteCmd(p provider.Provider, scope registry.Scope, name string) tea.Cmd {
	return func() tea.Msg {
		return actionDoneMsg{action: "delete", name: name, scope: scope, err: p.Delete(name, true)}
	}
}

// deleteTemplateCmd removes a golden template's Lima instance. Mirrors
// deleteCmd's shape — a synchronous provider call wrapped in a tea.Cmd,
// reported via actionDoneMsg — and, like every other command here, touches
// nothing but the provider call: the registry removal happens where
// actionDoneMsg is HANDLED (model.go, the Update goroutine), never inside
// this closure, which runs on its own goroutine.
func deleteTemplateCmd(p provider.Provider, scope registry.Scope, name, templateInstance string) tea.Cmd {
	return func() tea.Msg {
		err := p.DeleteTemplate(context.Background(), templateInstance, io.Discard)
		return actionDoneMsg{action: "delete template", name: name, scope: scope, err: err}
	}
}

// stopAllCmd stops each named VM in turn, accumulating failures. Stopping is
// sequential rather than concurrent: the provider gives no concurrency
// guarantees, and a serial loop yields a deterministic error report. VMs that
// stop successfully stay stopped even if a later one fails.
//
// name carries a count rather than a single VM name (there is no single VM
// here): model.go's actionDoneMsg handler builds its status label as
// `action + " " + name`, so passing a descriptive count keeps that label
// readable ("stop all (3 VMs) ok") instead of leaving a trailing space.
func stopAllCmd(p provider.Provider, scope registry.Scope, names []string) tea.Cmd {
	return func() tea.Msg {
		var failed []string
		for _, n := range names {
			if err := p.Stop(n); err != nil {
				failed = append(failed, n)
			}
		}
		var err error
		if len(failed) > 0 {
			err = fmt.Errorf("could not stop: %s", strings.Join(failed, ", "))
		}
		return actionDoneMsg{action: "stop all", name: fmt.Sprintf("(%d VMs)", len(names)), scope: scope, err: err}
	}
}

// shellCmd opens a shell into v's persistent guest tmux session, its argv
// built by p.AttachArgv (the one seam that knows tmux exists on the guest
// side; this function constructs no guest tmux command of its own), so no
// shell greets the user with `bash: cd: … No such file or directory`.
//
// It branches on whether the TUI is ITSELF already running inside host tmux:
//
//   - $TMUX unset (the common case — a plain terminal, an IDE pane, an SSH
//     session): this is the only branch that suspends anything. A tmux
//     CLIENT needs a real TTY, which only tea.ExecProcess can hand it — it
//     releases the terminal, blocks until the child exits, then restores the
//     TUI. The TUI resumes on detach (C-a d) or exit either way.
//   - $TMUX set: instead of suspending, open a new HOST window with `tmux
//     new-window` and let it run in the background. This is an ORDINARY
//     tea.Cmd, deliberately NOT tea.ExecProcess: `tmux new-window` returns in
//     milliseconds, and wrapping a command that fast in tea.ExecProcess would
//     tear down the alt-screen and release input to suspend the TUI for
//     essentially no time at all — defeating the entire point of the branch,
//     which is to keep the live board and its in-flight job progress bars on
//     screen next to the new shell. The new window re-enters through `sand
//     shell <name>` rather than reaching into the provider directly, so the
//     fast path and a user typing the command by hand are the exact same
//     code.
func shellCmd(p provider.Provider, scope registry.Scope, v vm.VM) tea.Cmd {
	if hostInTmux() {
		return hostTmuxWindowCmd(scope, v.Name)
	}

	argv := p.AttachArgv(v)
	c := exec.Command(argv[0], argv[1:]...)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return actionDoneMsg{action: "shell", name: v.Name, scope: scope, err: err}
	})
}

// pasteCmd runs the `v` paste-image verb's one guest round trip off the
// Update goroutine: it stages the host clipboard's image on v's guest via
// paste.PasteImage, then reports the outcome as a pasteResultMsg. v is the
// VM the command REGISTRY handed the action (see commandreg.go's `v` entry),
// never m.detail — the same wrong-VM fix startTransfer's doc comment
// explains. PasteImage does not itself check v.Status == Running; that guard
// is this verb's enabledFor, so pasteCmd is only ever dispatched against a
// VM already known to be up.
func pasteCmd(p provider.Provider, v vm.VM) tea.Cmd {
	return func() tea.Msg {
		result, err := paste.PasteImage(context.Background(), p, v)
		return pasteResultMsg{name: v.Name, result: result, err: err}
	}
}

// hostInTmux reports whether the TUI is itself running inside a host tmux
// session, which is what decides between shellCmd's two branches. It is one
// function rather than two reads of the environment because the `S` verb's log
// line has to describe the branch that actually fired: two independent checks
// would let the copy drift into telling the user the board stayed live while the
// TUI was in fact suspending.
func hostInTmux() bool { return os.Getenv("TMUX") != "" }

// runHostTmuxNewWindow runs `tmux new-window shellCommand` on the host.
// Indirected through a package-level var — the same seam header.go's
// hostMemBytesFn/hostDiskFreeFn use — so a test can substitute a fake
// without ever driving a real tmux server: this package must not require one
// in a unit test, the same reason no test here may require a real limactl.
var runHostTmuxNewWindow = func(shellCommand string) error {
	return exec.Command("tmux", "new-window", shellCommand).Run()
}

// hostTmuxWindowCmd is shellCmd's fast-path branch: an ordinary (non-
// suspending) tea.Cmd that opens name's shell in a new HOST tmux window. See
// shellCmd's doc comment for why this must never be wrapped in
// tea.ExecProcess.
func hostTmuxWindowCmd(scope registry.Scope, name string) tea.Cmd {
	return func() tea.Msg {
		self, err := os.Executable()
		if err != nil {
			self = "sand" // last resort: PATH lookup: os.Executable rarely fails
		}
		err = runHostTmuxNewWindow(hostTmuxShellCommand(self, name))
		return actionDoneMsg{action: "shell", name: name, scope: scope, err: err}
	}
}

// hostTmuxShellCommand builds the single shell-command string `tmux
// new-window` runs in its new window. tmux hands that string to $SHELL -c,
// so self (the running binary's own resolved path — NOT the bare word
// "sand", which may not be on PATH for an absolute invocation or a `go run`)
// and name are each quoted as one POSIX shell word rather than joined with a
// bare space: a resolved binary path is not guaranteed to be space-free.
//
// THE WINDOW IS HELD OPEN ON FAILURE. tmux closes a window the instant its command
// exits, so a `sand shell` that fails — the VM stopped between the keypress and the
// attach, limactl not on PATH, a guest with no tmux — printed its error into a
// window that vanished in the same frame. What the user sees is "a tmux window
// opened and immediately closed", with no way to find out why; the board, meanwhile,
// reports the failure on one status line that the next message scrolls away. On a
// non-zero exit we pause for a keypress so the error can actually be read. A clean
// exit (a detach, or the guest shell exiting) closes the window as before — the hold
// must not tax the normal path.
func hostTmuxShellCommand(self, name string) string {
	attach := shQuote(self) + " shell " + shQuote(name)
	return attach + ` || { printf '\n[sand] shell exited with an error — press enter to close this window.'; read -r _; }`
}

// shQuote quotes s as a single POSIX shell word.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

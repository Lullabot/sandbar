package ui

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/vm"

	tea "charm.land/bubbletea/v2"
)

// Message types flowing through Update. Every blocking lima/provision call is
// wrapped in a tea.Cmd that returns one of these — Update itself never blocks.
type (
	// vmsLoadedMsg carries the result of a List refresh.
	vmsLoadedMsg struct {
		vms []vm.VM
		err error
	}
	// actionDoneMsg reports a lifecycle action (start/stop/restart/delete). name
	// is the affected instance, so the model can update the managed registry.
	// warn carries a non-fatal problem (currently: a failed ApplySecrets) that
	// must NOT be treated as a failure — err staying nil is what tells the
	// handler in model.go the action itself succeeded.
	actionDoneMsg struct {
		action string
		name   string
		err    error
		warn   string
	}
	// provisionOutputMsg is one chunk of streamed provisioner output.
	provisionOutputMsg string
	// provisionDoneMsg signals the provisioner goroutine finished.
	provisionDoneMsg struct{ err error }
)

// listCmd loads the VM list off the Update goroutine, and measures each VM's
// real disk consumption here in the command — a blocking per-VM stat that must
// NOT run in Update, so an unresponsive mount (stale NFS, sleeping USB, autofs)
// can't stall the Bubble Tea event loop. A non-positive result leaves DiskUsed
// empty so the cell renders blank.
func listCmd(cli *lima.Client) tea.Cmd {
	return func() tea.Msg {
		vms, err := cli.List()
		if err == nil {
			for i := range vms {
				if n := diskUsedBytes(vms[i].Dir); n > 0 {
					vms[i].DiskUsed = strconv.FormatInt(n, 10)
				}
			}
		}
		return vmsLoadedMsg{vms: vms, err: err}
	}
}

// startCmd boots a stopped VM and then writes its host-stored secrets into
// the guest. A secrets failure is reported as a warning, not a failure: a VM
// that is up without its secrets is more useful than one reported as
// failed-to-start. If Start itself fails, ApplySecrets is never attempted.
//
// Note: a VM started outside sand (a bare `limactl start`) does not get
// freshly applied secrets — it sources whatever secrets.env was last written
// by a previous sand-initiated start (or none, if there never was one).
func startCmd(cli *lima.Client, name, user string, scopes map[string]map[string]string) tea.Cmd {
	return func() tea.Msg {
		if err := cli.Start(name); err != nil {
			return actionDoneMsg{action: "start", name: name, err: err}
		}
		warn := ""
		if err := provision.ApplySecrets(context.Background(), cli, name, user, scopes, io.Discard); err != nil {
			warn = "secrets not applied: " + err.Error()
		}
		return actionDoneMsg{action: "start", name: name, warn: warn}
	}
}

// stopCmd shuts a running VM down.
func stopCmd(cli *lima.Client, name string) tea.Cmd {
	return func() tea.Msg {
		return actionDoneMsg{action: "stop", name: name, err: cli.Stop(name)}
	}
}

// restartCmd stops then starts a VM, surfacing the first failure, and applies
// secrets after a successful start — same warn-not-fail semantics as startCmd.
// This is not redundant with startCmd: restartCmd drives cli.Stop/cli.Start
// directly rather than re-dispatching startCmd, so it would otherwise skip
// the apply step entirely.
func restartCmd(cli *lima.Client, name, user string, scopes map[string]map[string]string) tea.Cmd {
	return func() tea.Msg {
		if err := cli.Stop(name); err != nil {
			return actionDoneMsg{action: "restart", name: name, err: err}
		}
		if err := cli.Start(name); err != nil {
			return actionDoneMsg{action: "restart", name: name, err: err}
		}
		warn := ""
		if err := provision.ApplySecrets(context.Background(), cli, name, user, scopes, io.Discard); err != nil {
			warn = "secrets not applied: " + err.Error()
		}
		return actionDoneMsg{action: "restart", name: name, warn: warn}
	}
}

// applySecretsCmd writes name's stored secrets into the guest without
// starting or stopping anything. It backs the create/reset seam: createVM and
// Reset each end with their own StartStreaming (so the VM is already up by
// the time provisionDoneMsg fires), and by then a create-form GH_TOKEN has
// just landed in the store — so this pushes it in rather than waiting for the
// VM's *next* start. Failure is a warning, matching startCmd/restartCmd.
func applySecretsCmd(cli *lima.Client, name, user string, scopes map[string]map[string]string) tea.Cmd {
	return func() tea.Msg {
		warn := ""
		if err := provision.ApplySecrets(context.Background(), cli, name, user, scopes, io.Discard); err != nil {
			warn = "secrets not applied: " + err.Error()
		}
		return actionDoneMsg{action: "apply secrets", name: name, warn: warn}
	}
}

// secretsFor returns the guest user and the VM's full scope map (global plus
// any directory-scoped secrets), defaulting the user to the host username
// when the VM has no recorded config (mirroring openResetForm's fallback in
// detail.go).
func (m model) secretsFor(name string) (user string, scopes map[string]map[string]string) {
	user = vm.HostUser()
	if cfg, ok := m.reg.Config(name); ok && cfg.User != "" {
		user = cfg.User
	}
	return user, m.sec.GetAll(name)
}

// deleteCmd force-removes a VM.
func deleteCmd(cli *lima.Client, name string) tea.Cmd {
	return func() tea.Msg {
		return actionDoneMsg{action: "delete", name: name, err: cli.Delete(name, true)}
	}
}

// stopAllCmd stops each named VM in turn, accumulating failures. Stopping is
// sequential rather than concurrent: limactl stop is I/O-heavy, lima.Client
// gives no concurrency guarantees, and a serial loop yields a deterministic
// error report. VMs that stop successfully stay stopped even if a later one
// fails.
//
// name carries a count rather than a single VM name (there is no single VM
// here): model.go's actionDoneMsg handler builds its status label as
// `action + " " + name`, so passing a descriptive count keeps that label
// readable ("stop all (3 VMs) ok") instead of leaving a trailing space.
func stopAllCmd(cli *lima.Client, names []string) tea.Cmd {
	return func() tea.Msg {
		var failed []string
		for _, n := range names {
			if err := cli.Stop(n); err != nil {
				failed = append(failed, n)
			}
		}
		var err error
		if len(failed) > 0 {
			err = fmt.Errorf("could not stop: %s", strings.Join(failed, ", "))
		}
		return actionDoneMsg{action: "stop all", name: fmt.Sprintf("(%d VMs)", len(names)), err: err}
	}
}

// shellCmd opens an interactive shell inside a VM. It uses tea.ExecProcess to
// suspend the TUI and hand the real terminal to `limactl shell <name>`, then
// resumes the TUI when the shell exits. This deliberately bypasses lima.Runner
// (which captures output) — an interactive session needs the actual TTY, which
// only the real process attached to stdin/stdout can provide.
func shellCmd(name string) tea.Cmd {
	c := exec.Command("limactl", "shell", name)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return actionDoneMsg{action: "shell", name: name, err: err}
	})
}

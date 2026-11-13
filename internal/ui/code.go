package ui

// code.go is the board's `c` verb: open the focused VM in VS Code's
// Remote-SSH. The work itself is internal/vscode.Open, shared with `sandbar
// code` so the two front ends cannot drift; this file adds only what the
// board owns — the consent overlay for ~/.ssh/config, running the check and
// the operation off the Update goroutine, and laying the shared messages out
// as log lines.

import (
	"context"
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lullabot/sandbar/internal/manage"
	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/sshconfig"
	"github.com/lullabot/sandbar/internal/vm"
	"github.com/lullabot/sandbar/internal/vscode"
)

// codeTimeout bounds the whole operation. Open's EditorSSH is a network
// round trip on remote Lima (authorizing the key) and Proxmox (resolving the
// guest address); the board shows a spinner for it, and a wedged host must
// end in an error the user can read rather than a spinner that never stops.
const codeTimeout = 2 * time.Minute

// codeNote is one line codeCmd reports besides its outcome. warn marks a line
// the user has to act on — today only "add this Include line yourself" — so
// it renders with the log's warning treatment and is not lost among the
// routine lines.
type codeNote struct {
	text string
	warn bool
}

// codeResultMsg reports the `c` verb's outcome. name is the VM the registry
// handed the action, so the log always describes the tile that acted.
type codeResultMsg struct {
	name  string
	notes []codeNote
	text  string
	err   error
}

// manualIncludeNote is the shared "add this line yourself" message, on one
// line because the log is a list of lines. It never fails the verb: the entry
// is still written, so adding the line later is all it takes.
func manualIncludeNote(why string) codeNote {
	return codeNote{text: vscode.ManualIncludeMessage(why, " "), warn: true}
}

// openInCode is the `c` action. It only starts the Include check: that runs
// ssh (vscode.CheckInclude asks `ssh -G` whether ~/.ssh/config really applies
// the managed file), so it belongs in a command, and handleCodeIncludeChecked
// decides what happens next from its answer. Everything the rest needs is
// captured here, by VM identity, so focus moving while ssh answers changes
// nothing about which VM is opened.
func (m *model) openInCode(v boardVM) tea.Cmd {
	p := m.provFor(v.scope)
	mem, ok := m.memberByScope(v.scope)
	if p == nil || !ok {
		// Unreachable via dispatch: a VM gets a tile only from a member that
		// listed it. Guarded so a direct call cannot hand Open a nil guest.
		return nil
	}
	req := codeRequest{scope: v.scope, vm: v.VM, prof: mem.profile, cloneURL: m.recordedCloneURL(v.scope, v.Name)}
	return func() tea.Msg {
		action, why := vscode.CheckInclude(context.Background())
		return codeIncludeCheckedMsg{req: req, action: action, why: why}
	}
}

// codeRequest is one press of `c`: the VM it was pressed on and what opening
// it needs, fixed at the keypress.
type codeRequest struct {
	scope    registry.Scope
	vm       vm.VM
	prof     profiles.Profile
	cloneURL string
}

// codeIncludeCheckedMsg carries vscode.CheckInclude's answer for req.
type codeIncludeCheckedMsg struct {
	req    codeRequest
	action vscode.IncludeAction
	why    string
}

// handleCodeIncludeChecked acts on the Include check. Whether ~/.ssh/config
// may be edited is the user's decision, asked here — before any work —
// through the same overlay every other confirmation uses; the editing and the
// launch happen in codeCmd. A config sandbar will not edit (a symlink,
// unwritable, or unreadable) gets no question, only the line to add, exactly
// as the CLI does.
func (m model) handleCodeIncludeChecked(msg codeIncludeCheckedMsg) (tea.Model, tea.Cmd) {
	req := msg.req
	p := m.provFor(req.scope)
	if p == nil {
		// The profile was disabled or deleted while ssh answered.
		m.logMsg("code " + req.vm.Name + ": its profile is no longer connected")
		return m, nil
	}
	working := "opening " + req.vm.Name + " in VS Code…"

	var notes []codeNote
	switch msg.action {
	case vscode.IncludeManual:
		notes = append(notes, manualIncludeNote(msg.why))
	case vscode.IncludeAsk:
		if m.confirm != nil || m.view != viewBoard {
			// The user raised another question, or left the board, while ssh
			// answered. Putting this one up now would replace theirs or appear
			// on a screen it does not belong to; pressing c again asks afresh.
			m.logMsg("code " + req.vm.Name + ": not asking about ~/.ssh/config while another screen or question is open; press c again")
			return m, nil
		}
		path := sshconfig.UserConfigPath()
		if msg.why != "" {
			m.logWarn(msg.why + ".")
		}
		// The full line goes into the log as well as the prompt: the prompt is
		// one row and may be clipped on a narrow terminal, and the user is being
		// asked to agree to exactly this text.
		m.logMsg(fmt.Sprintf("ssh reads sandbar's VM host entries only once %s includes them, with this line at the top: %s", path, sshconfig.IncludeLine()))
		home, _ := os.UserHomeDir()
		m.confirm = &confirmState{
			// Shortened for a prompt that has one row to spend; display only.
			prompt:    fmt.Sprintf("Add %q to %s?", sshconfig.IncludeLine(), shortenGuestPath(path, home)),
			run:       codeCmd(p, req, true, nil),
			cancelRun: codeCmd(p, req, false, []codeNote{manualIncludeNote("Left ~/.ssh/config unchanged")}),
			working:   working,
			scope:     req.scope,
			vmName:    req.vm.Name,
		}
		return m, nil
	}
	m.logMsg(working)
	return m, m.beginAction(codeCmd(p, req, false, notes))
}

// codeCmd runs the operation off the Update goroutine: the consented Include
// edit, then vscode.Open (which may be an ssh round trip, then the launch).
// notes are lines decided before it ran, reported ahead of its own.
func codeCmd(p provider.Provider, req codeRequest, addInclude bool, notes []codeNote) tea.Cmd {
	return func() tea.Msg {
		out := codeResultMsg{name: req.vm.Name, notes: append([]codeNote(nil), notes...)}
		if addInclude {
			if err := sshconfig.AddInclude(); err != nil {
				out.notes = append(out.notes, manualIncludeNote(vscode.AddIncludeFailure(err)))
			} else {
				out.notes = append(out.notes, codeNote{text: "Added the line to " + sshconfig.UserConfigPath() + "."})
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), codeTimeout)
		defer cancel()
		lines, launched, err := vscode.Open(ctx, p, req.prof, req.vm, req.cloneURL)
		if err != nil {
			for _, l := range lines {
				out.notes = append(out.notes, codeNote{text: l})
			}
			out.err = err
			return out
		}
		last := len(lines) - 1
		for _, l := range lines[:last] {
			out.notes = append(out.notes, codeNote{text: l})
		}
		out.text = lines[last]
		if launched {
			// VS Code is still connecting when the log says this.
			out.text += "…"
		}
		return out
	}
}

// handleCodeResult logs the outcome. Nothing on the board changes — the VM
// is as it was — so there is no refresh.
func (m model) handleCodeResult(msg codeResultMsg) (tea.Model, tea.Cmd) {
	m.acting = false
	for _, n := range msg.notes {
		if n.warn {
			m.logWarn(n.text)
		} else {
			m.logMsg(n.text)
		}
	}
	if msg.err != nil {
		m.logMsg("code " + msg.name + " failed: " + msg.err.Error())
		return m, nil
	}
	m.logMsg(msg.text)
	return m, nil
}

// recordedCloneURL is the project the VM was created for, by the same rule
// `sandbar code` uses (manage.RecordedCloneURL). The board already holds the
// member's batched marker read, so the marker source is a map lookup, not a
// round trip. "" opens the guest home.
func (m model) recordedCloneURL(sc registry.Scope, name string) string {
	marker := func() (vm.CreateConfig, bool) {
		mem, ok := m.memberByScope(sc)
		if !ok {
			return vm.CreateConfig{}, false
		}
		pv, found := mem.provenance[name]
		return pv.Config, found
	}
	return manage.RecordedCloneURL(marker, m.reg, sc, name)
}

// editorEntriesRemovedMsg reports removeEditorEntriesCmd's outcome; only a
// failure is logged. what names the entries, to read before "not removed".
type editorEntriesRemovedMsg struct {
	what string
	err  error
}

// removeEditorEntriesCmd runs one of internal/vscode's entry cleanups (a
// deleted VM's, the VMs a reconcile dropped, or a deleted profile's) off the
// Update goroutine, because each locks and rewrites a file. It matters most
// for Proxmox, whose entry names a DHCP address that may later belong to an
// unrelated machine.
func removeEditorEntriesCmd(what string, remove func() ([]string, error)) tea.Cmd {
	return func() tea.Msg {
		_, err := remove()
		return editorEntriesRemovedMsg{what: what, err: err}
	}
}

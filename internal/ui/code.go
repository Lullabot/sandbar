package ui

// code.go is the board's `c` verb: open the focused VM in VS Code's
// Remote-SSH. The work itself is internal/vscode.Prepare, shared with
// `sandbar code` so the two front ends cannot drift; this file adds only what
// the board owns — the consent overlay for ~/.ssh/config, running the
// operation off the Update goroutine, and the messages, worded as the CLI
// words them.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/sshconfig"
	"github.com/lullabot/sandbar/internal/vm"
	"github.com/lullabot/sandbar/internal/vscode"
)

// codeTimeout bounds the whole operation. Prepare's EditorSSH is a network
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

// openInCode is the `c` action. Whether ~/.ssh/config may be edited is the
// user's decision, asked here — before any work — through the same overlay
// every other confirmation uses; the editing and the launch happen in
// codeCmd. A config sandbar will not edit (a symlink, unwritable, or
// unreadable) gets no question, only the line to add, exactly as the CLI
// does.
func (m *model) openInCode(v boardVM) tea.Cmd {
	p := m.provFor(v.scope)
	if p == nil {
		// Unreachable via dispatch: a VM gets a tile only from a member that
		// listed it. Guarded so a direct call cannot hand Prepare a nil guest.
		return nil
	}
	cloneURL := m.recordedCloneURL(v.scope, v.Name)
	working := "opening " + v.Name + " in VS Code…"
	path := sshconfig.UserConfigPath()

	var notes []codeNote
	st, err := sshconfig.IncludeStatus()
	switch {
	case err != nil:
		notes = append(notes, manualIncludeNote(vscode.UnreadableReason(err)))
	case vscode.ClassifyInclude(st) == vscode.IncludeManual:
		notes = append(notes, manualIncludeNote(vscode.ManualReason(st)))
	case vscode.ClassifyInclude(st) == vscode.IncludeAsk:
		// The full line goes into the log as well as the prompt: the prompt is
		// one row and may be clipped on a narrow terminal, and the user is being
		// asked to agree to exactly this text.
		m.logMsg(fmt.Sprintf("ssh reads sandbar's VM host entries only once %s includes them, with this line at the top: %s", path, sshconfig.IncludeLine()))
		m.confirm = &confirmState{
			prompt:    fmt.Sprintf("Add %q to %s?", sshconfig.IncludeLine(), tildePath(path)),
			choices:   "[y] add   [n] skip   [esc] cancel",
			run:       codeCmd(p, v.VM, cloneURL, true, nil),
			cancelRun: codeCmd(p, v.VM, cloneURL, false, []codeNote{manualIncludeNote("Left ~/.ssh/config unchanged")}),
			working:   working,
			scope:     v.scope,
			vmName:    v.Name,
		}
		return nil
	}
	m.logMsg(working)
	return m.beginAction(codeCmd(p, v.VM, cloneURL, false, notes))
}

// codeCmd runs the operation off the Update goroutine: the consented Include
// edit, then Prepare (which may be an ssh round trip), then the launch. notes
// are lines decided before it ran, reported ahead of its own.
func codeCmd(p provider.Provider, v vm.VM, cloneURL string, addInclude bool, notes []codeNote) tea.Cmd {
	return func() tea.Msg {
		out := codeResultMsg{name: v.Name, notes: append([]codeNote(nil), notes...)}
		if addInclude {
			if err := sshconfig.AddInclude(); err != nil {
				out.notes = append(out.notes, manualIncludeNote(vscode.AddIncludeFailure(err)))
			} else {
				out.notes = append(out.notes, codeNote{text: "Added the line to " + sshconfig.UserConfigPath() + "."})
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), codeTimeout)
		defer cancel()
		res, err := vscode.Prepare(ctx, p, v, cloneURL)
		if err != nil {
			out.err = err
			return out
		}
		if msg := res.AuthorizedMessage(); msg != "" {
			out.notes = append(out.notes, codeNote{text: msg})
		}
		if res.LaunchArgv == nil {
			out.text = res.NotFoundMessage()
			return out
		}
		if err := vscode.Launch(res.LaunchArgv); err != nil {
			out.err = fmt.Errorf("start %s: %w", res.CodePath, err)
			return out
		}
		out.text = res.OpeningMessage() + "…"
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

// recordedCloneURL is the project the VM was created for, read the way
// `sandbar code` reads it: the provenance marker first (it lives with the VM,
// so it is right even for a VM another controller created), then the managed
// index. The board already holds the member's batched marker read, so this is
// a map lookup, not a round trip. "" opens the guest home.
func (m model) recordedCloneURL(sc registry.Scope, name string) string {
	if mem, ok := m.memberByScope(sc); ok {
		if pv, found := mem.provenance[name]; found && pv.Config.CloneURL != "" {
			return pv.Config.CloneURL
		}
	}
	if cfg, ok := m.reg.ConfigInScope(name, sc); ok {
		return cfg.CloneURL
	}
	return ""
}

// editorAlias is the ssh alias the VM's provider writes for it, by the same
// helpers the providers use: Lima's own name for the Local profile, a
// profile-namespaced one otherwise. ok is false when no member owns sc any
// more (its profile was deleted mid-action), leaving nothing to name.
func (m model) editorAlias(sc registry.Scope, name string) (string, bool) {
	mem, ok := m.memberByScope(sc)
	if !ok {
		return "", false
	}
	if mem.profile.Type == profiles.TypeLocal {
		return sshconfig.LimaAlias(name), true
	}
	return sshconfig.ProfileAlias(mem.profile.Name, name), true
}

// tildePath shortens a path under the home directory to its ~/ form, for a
// prompt that has one row to spend. Display only: what is written is always
// the absolute path.
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if rel, err := filepath.Rel(home, p); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return "~/" + rel
	}
	return p
}

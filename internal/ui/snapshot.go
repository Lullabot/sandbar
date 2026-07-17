package ui

// snapshot.go is the "snapshot this VM into a golden template" verb: a
// one-field prompt for the template's user-facing name (commandreg.go's `t`
// binding opens it), followed by a tracked job — the same io.Pipe → registry
// → viewport → spinner machinery every other job in this package uses
// (progress.go, jobs.go) — that streams Provider.SnapshotTemplate's
// stop→clone→restore progress and, on success, records the result as a
// registry.Template.
//
// The job is keyed kindSnapshot, not kindProvision: it acts on an EXISTING
// managed VM (the one being captured) without creating or replacing it, so it
// must never move that VM's derived status the way a build does (see
// deriveStatus, jobs.go) — a source VM being snapshotted stays exactly as
// Running/Stopped as it already was, save for the transient stop/restart
// SnapshotTemplate itself performs.

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// pendingSnapshot is a kindSnapshot job's template metadata, kept on the model
// between launchSnapshot (which starts the job) and the provisionDoneMsg that
// ends it — mirroring how a provision job carries its vm.CreateConfig through
// the job registry (jobs.go's markProvision/config), except this state lives
// on the model rather than inside a *job, since nothing about it needs to be
// visible to a tile or a progress screen.
//
// Every field but outcome is decided BEFORE the job starts and never touched
// again. outcome is different: it is a pointer the RUN CLOSURE itself writes
// into, just before returning, with the provisioner's actual result — the one
// fact that is only known once SnapshotTemplate finishes. Reading it here (in
// the Update goroutine's provisionDoneMsg handler) is race-free without a
// mutex because that handler only ever runs AFTER this job's io.Pipe has been
// closed by the same run closure (beginStream's goroutine contract, jobs.go):
// the pipe close/EOF-read sequencing is what orders "run closure writes
// outcome" before "Update goroutine reads it", exactly the way a channel send
// orders before its receive.
type pendingSnapshot struct {
	name      string          // user-facing template name
	cfg       vm.CreateConfig // the source VM's own recorded config, BaseName already overridden to the template's instance name
	createdAt time.Time
	outcome   *provision.SnapshotResult
}

// openSnapshotPrompt starts the snapshot flow for v: a single text field for
// the template's name, reusing the same textinput/label/footer chrome the
// rest of this package's small prompts use (see destView) rather than a
// second visual language for one field.
func (m *model) openSnapshotPrompt(v boardVM) tea.Cmd {
	m.snapshotSrcVM = v.Name
	m.snapshotSrcScope = v.scope
	ti := textinput.New()
	ti.CharLimit = 64
	ti.SetWidth(44)
	ti.Placeholder = "e.g. golden"
	m.snapshotInput = ti
	m.snapshotErr = nil
	m.view = viewSnapshotPrompt
	return m.snapshotInput.Focus()
}

// updateSnapshotPrompt handles keys while the snapshot-name prompt is open.
func (m model) updateSnapshotPrompt(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Code == tea.KeyEsc:
		m.view = viewBoard
		return m, nil
	case key.Matches(msg, m.keys.Submit):
		return m.launchSnapshot()
	}
	var cmd tea.Cmd
	m.snapshotInput, cmd = m.snapshotInput.Update(msg)
	return m, cmd
}

// launchSnapshot validates the typed name, refuses an obvious collision (an
// existing template/VM/base already answering to it — mirroring the
// headless `sand template snapshot` command's own guard), then starts the
// job through the shared beginStream plumbing. On success it stashes this
// job's template metadata in m.pendingSnapshots so the provisionDoneMsg
// handler (model.go) can build and persist the registry.Template once the
// provisioner's result comes back.
func (m model) launchSnapshot() (tea.Model, tea.Cmd) {
	name := strings.TrimSpace(m.snapshotInput.Value())
	if err := vm.ValidateTemplateName(name); err != nil {
		m.snapshotErr = err
		return m, nil
	}
	scope := m.snapshotSrcScope
	if _, exists := m.reg.TemplateInScope(name, scope); exists {
		m.snapshotErr = fmt.Errorf("a template named %q already exists", name)
		return m, nil
	}
	if m.reg.IsManagedInScope(name, scope) || m.isBaseImage(name) {
		m.snapshotErr = fmt.Errorf("%q is already in use by a VM or base image", name)
		return m, nil
	}
	prov := m.provFor(scope)
	if prov == nil {
		m.snapshotErr = fmt.Errorf("this connection profile is not available")
		return m, nil
	}

	// The template's Config is the source VM's own recorded create config
	// (already clone-token-stripped by ConfigInScope, exactly like a VM
	// entry's own Config) with BaseName overridden to the template's own
	// instance name — see registry.Template's doc comment: a later clone or
	// reset must clone from THIS template, never from whatever base the
	// source itself happened to be cloned from. A VM the registry has no
	// config for (a pre-snapshot index entry) still gets a usable, if
	// minimal, template.
	source := m.snapshotSrcVM
	templateInstance := vm.TemplateInstanceName(name)
	cfg, _ := m.reg.ConfigInScope(source, scope)
	cfg.Name = source
	cfg.BaseName = templateInstance

	outcome := new(provision.SnapshotResult)
	run := func(ctx context.Context, out io.Writer) error {
		res, err := prov.SnapshotTemplate(ctx, source, templateInstance, out)
		*outcome = res
		return err
	}
	key := snapshotKey(scope, source)
	title := "Snapshotting " + source + " into template " + name
	cmd, started := m.beginStream(key, title, run)
	if !started {
		return m, cmd
	}
	if m.pendingSnapshots == nil {
		m.pendingSnapshots = make(map[jobKey]pendingSnapshot)
	}
	m.pendingSnapshots[key] = pendingSnapshot{name: name, cfg: cfg, createdAt: templateNowFn(), outcome: outcome}
	m.view = viewBoard
	return m, cmd
}

// finishSnapshot completes a kindSnapshot job once its provisionDoneMsg
// arrives (model.go's dispatch): it drops the job's pending metadata — done
// unconditionally, so a canceled or failed attempt never leaks an entry — and,
// on a real success, records the registry.Template. registry.Scope/Source
// come from the job key itself (the owning member and the VM it captured),
// never from anything that could drift out of step with it.
func (m *model) finishSnapshot(key jobKey, canceled bool, err error) {
	sp, ok := m.pendingSnapshots[key]
	if !ok {
		return
	}
	delete(m.pendingSnapshots, key)
	if err != nil || canceled {
		return
	}
	t := registry.Template{
		Name:            sp.name,
		Scope:           key.scope,
		Source:          key.vm,
		CreatedAt:       sp.createdAt,
		PlaybookVersion: sp.outcome.PlaybookVersion,
		ToolsetKey:      sp.outcome.ToolsetKey,
		Config:          sp.cfg,
	}
	if addErr := m.reg.AddTemplate(t); addErr != nil {
		m.logMsg("template " + sp.name + " captured, but could not be recorded: " + addErr.Error())
		return
	}
	m.logMsg("template " + sp.name + " saved from " + key.vm)
}

// snapshotSubmitHelp is a display-only relabeling of m.keys.Submit ("ctrl+s
// create", meant for the create form) so this screen's footer reads "ctrl+s
// snapshot" instead — updateSnapshotPrompt still matches the KEY against
// m.keys.Submit itself, so the two can never disagree about which key fires.
var snapshotSubmitHelp = key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "snapshot"))

// snapshotPromptHelp returns the bindings shown in the snapshot-name prompt's
// help bar.
func (m model) snapshotPromptHelp() []key.Binding {
	return []key.Binding{snapshotSubmitHelp, m.keys.Back}
}

// snapshotPromptView renders the snapshot-name prompt.
func (m model) snapshotPromptView() string {
	cw := m.layout.ContentWidth
	var b strings.Builder
	b.WriteString(titleStyle.Render("Snapshot " + m.snapshotSrcVM + " into a template"))
	b.WriteString("\n\n")
	b.WriteString(labelStyle.Render("Template name:") + " " + m.snapshotInput.View())
	b.WriteString("\n\n")
	b.WriteString(fieldInfoStyle.Width(cw - 2).Render(
		"Stops " + m.snapshotSrcVM + " if it is running, clones it into a reusable golden " +
			"template, then restores it to how it was. " + m.snapshotSrcVM + " itself is left untouched."))
	if m.snapshotErr != nil {
		b.WriteString("\n\n" + errStyle.Width(cw).Render("Error: "+m.snapshotErr.Error()))
	}
	b.WriteString("\n\n" + m.footerView(m.snapshotPromptHelp()))
	return appStyle.Render(b.String())
}

package ui

// form_async_toolset_test.go is the regression suite for finding 4 in the
// plan-16 code review: openForm used to read the shared base image's
// recorded tool-set stamp SYNCHRONOUSLY, on the Update goroutine
// (provision.BaseToolset(p.HostFiles(), ...)). For a remote profile that read
// goes over ssh (HostFiles.ReadFile), so a slow or dead remote host froze the
// WHOLE TUI the instant 'n' was pressed — the exact never-block guarantee the
// rest of the fleet (async connect/list per member, task 7) exists to
// provide. The fix moves the read into a tea.Cmd (formToolsetCmd, kicked by
// kickFormToolsetLoad) whose result — toolsetLoadedMsg, form.go — is applied
// only if the form is still open and still targeting the scope it was read
// for.

import (
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"
)

// blockingHostFiles is a lima.HostFiles whose ReadFile blocks until release
// is closed — standing in for a remote profile's SSHHost against a slow or
// wedged host. Every other method delegates to the embedded (local)
// HostFiles, matching how little a real remote implementation's OTHER
// methods matter here: only ReadFile is on provision.BaseToolset's path.
type blockingHostFiles struct {
	lima.HostFiles
	release chan struct{}
}

func (b blockingHostFiles) ReadFile(path string) ([]byte, error) {
	<-b.release
	return nil, errors.New("blockingHostFiles: released without data")
}

func (b blockingHostFiles) Stat(path string) (fs.FileInfo, error) {
	return nil, fs.ErrNotExist
}

// blockingHostFilesProvider is a providerfake.Provider whose HostFiles()
// returns a blockingHostFiles — a stand-in for a remote connection profile
// whose HostFiles seam never answers.
type blockingHostFilesProvider struct {
	providerfake.Provider
	release chan struct{}
}

func (b *blockingHostFilesProvider) HostFiles() lima.HostFiles {
	return blockingHostFiles{HostFiles: lima.LocalFiles(), release: b.release}
}

// TestOpenFormNeverBlocksOnRemoteToolsetRead drives the REAL Bubble Tea
// runtime: the form's target member's HostFiles.ReadFile blocks forever, and
// the create form must still render and accept keystrokes immediately — the
// tool-set read must never run on the Update goroutine (see formToolsetCmd).
func TestOpenFormNeverBlocksOnRemoteToolsetRead(t *testing.T) {
	isolateHostState(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) }) // unblock the hung read on exit

	blocked := &blockingHostFilesProvider{release: release}
	tm := teatest.NewTestModel(t, New(singleFleet(blocked, registry.LocalScope)), teatest.WithInitialTermSize(100, 30))

	tm.Send(runeKey('n')) // open the create form
	waitForText(t, tm, "New VM")

	// The form must accept keys immediately — typing must not stall behind
	// the hung tool-set read.
	tm.Type("myvm")
	waitForTypedText(t, tm, "myvm")

	tm.Send(tea.KeyPressMsg{Code: tea.KeyEsc})
	tm.Quit()
	fm := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	m, ok := fm.(model)
	if !ok {
		t.Fatal("FinalModel did not return a model")
	}
	if m.view != viewBoard {
		t.Fatalf("esc should have returned to the board, got view %v", m.view)
	}
}

// TestToolsetLoadedMsgIgnoresStaleScopeOrClosedForm pins the other half of
// finding 4: a toolsetLoadedMsg carries the scope it was read FOR, and the
// handler (model.go) must apply it ONLY when the form is still open and
// still targeting that exact scope — never when the user has since switched
// to a different profile (cycleFormProfile re-kicks its own, differently
// scoped read) or closed the form outright. Applying a stale result would
// clobber the CURRENTLY selected profile's toggles with a read that belongs
// to a profile the user has since left.
func TestToolsetLoadedMsgIgnoresStaleScopeOrClosedForm(t *testing.T) {
	m := newTestModel(t)
	cmd := m.openForm()
	if cmd == nil {
		t.Fatal("openForm should return a command (focus + tool-set read)")
	}

	// The all-on default, before any async result lands.
	if !m.toolClaude || !m.toolDDEV || !m.toolGo || !m.toolJava {
		t.Fatal("openForm should start from the all-on default before any async result lands")
	}

	// Simulate the user cycling to a DIFFERENT profile before the ORIGINAL
	// profile's (slow) tool-set read comes back: formScope now points
	// somewhere else, exactly as cycleFormProfile would leave it.
	staleScope := m.formScope
	otherScope := registry.Scope{Provider: "lima-remote", RemoteTarget: "user@build-host:22"}
	m.formScope = otherScope

	// THE FIX: a stale result for the scope the form is NO LONGER targeting
	// must be ignored — an all-tools-off toolset here would otherwise flip
	// every toggle, clobbering the newly-selected profile's state.
	next, _ := m.Update(toolsetLoadedMsg{scope: staleScope, toolset: map[string]bool{}, ok: true})
	m = next.(model)
	if !m.toolClaude || !m.toolDDEV || !m.toolGo || !m.toolJava {
		t.Fatal("a stale toolset result for a previously-selected scope must not clobber the currently-selected profile's toggles")
	}

	// A result for the CURRENTLY targeted scope is still applied normally.
	next, _ = m.Update(toolsetLoadedMsg{scope: otherScope, toolset: map[string]bool{}, ok: true})
	m = next.(model)
	if m.toolClaude || m.toolDDEV || m.toolGo || m.toolJava {
		t.Fatal("a result for the CURRENTLY targeted scope should still apply")
	}

	// Closing the form entirely must also make a late result inert.
	m.view = viewBoard
	next, _ = m.Update(toolsetLoadedMsg{scope: otherScope, toolset: map[string]bool{"claude": true, "ddev": true, "go": true, "java": true}, ok: true})
	m = next.(model)
	if m.view != viewBoard {
		t.Fatal("a toolset result must not reopen or otherwise touch a closed form")
	}
	if m.toolClaude || m.toolDDEV || m.toolGo || m.toolJava {
		t.Fatal("a toolset result delivered after the form closed must not touch its (now irrelevant) toggle state")
	}
}

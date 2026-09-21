package ui

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"
	"github.com/lullabot/sandbar/internal/agentprefs"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

func TestTUIAgentChoicesPersistAt80x24(t *testing.T) {
	isolateHostState(t)
	all := agentprefs.Selection{Claude: true, Codex: true, OpenCode: true, Pi: true}
	if err := agentprefs.Save(all); err != nil {
		t.Fatal(err)
	}
	created := make(chan vm.CreateConfig, 1)
	reset := make(chan provision.ResetOptions, 1)
	p := &providerfake.Provider{
		ListFunc: func() ([]vm.VM, error) { return []vm.VM{{Name: "agent-test", Status: "Stopped"}}, nil },
		CreateFunc: func(_ context.Context, c vm.CreateConfig, _ provision.CreateOptions, _ io.Writer) error {
			created <- c
			return nil
		},
		ResetFunc: func(_ context.Context, c vm.CreateConfig, opts provision.ResetOptions, _ io.Writer) error {
			if agentprefs.FromConfig(c) != all {
				t.Errorf("reset lost recorded agents: %+v", c)
			}
			reset <- opts
			return nil
		},
	}
	newForm := func() model {
		m := New(singleFleet(p, registry.LocalScope)).(model)
		deliverToolsetLoad(t, &m, m.openForm())
		m.inputs[fName].SetValue("agent-test")
		m.inputs[fUser].SetValue("tester")
		m.inputs[fGitName].SetValue("Test Author")
		m.inputs[fGitEmail].SetValue("test@example.com")
		m.focusIdx = fCloneToken
		return m
	}
	m := newForm()
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	waitForText(t, tm, "New VM")
	for range 4 {
		tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
		tm.Send(tea.KeyPressMsg{Code: tea.KeySpace})
	}
	tm.Send(ctrlKey('s'))
	select {
	case c := <-created:
		if agentprefs.FromConfig(c) != (agentprefs.Selection{}) {
			t.Fatalf("provider received selected agents: %+v", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("create did not reach provider")
	}
	finalModel(t, tm)

	m = newForm()
	if m.toolClaude || m.toolCodex || m.toolOpenCode || m.toolPi {
		t.Fatal("fresh model lost saved all-off selection")
	}
	tm = teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	waitForText(t, tm, "New VM")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	tm.Send(tea.KeyPressMsg{Code: tea.KeySpace})
	fm := finalModel(t, tm)
	if !fm.toolClaude {
		t.Fatal("checkbox did not respond before cancelling")
	}
	saved, found, err := agentprefs.Load()
	if err != nil || !found || saved != (agentprefs.Selection{}) {
		t.Fatalf("cancel changed preferences: %+v %v", saved, err)
	}

	cfg := vm.DefaultCreateConfig()
	cfg.Name, cfg.User, cfg.GitName, cfg.GitEmail = "agent-test", "tester", "Test Author", "test@example.com"
	all.Apply(&cfg)
	m = New(singleFleet(p, registry.LocalScope)).(model)
	m.openResetForm(registry.LocalScope, cfg.Name, cfg)
	m.focusIdx = fCloneToken
	tm = teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	waitForText(t, tm, "Reset VM")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	tm.Send(tea.KeyPressMsg{Code: tea.KeySpace})
	tm.Send(ctrlKey('s'))
	select {
	case opts := <-reset:
		if !opts.PreserveAgents {
			t.Fatal("unified preserve option did not reach provider")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reset did not reach provider")
	}
	finalModel(t, tm)
	saved, _, err = agentprefs.Load()
	if err != nil || saved != (agentprefs.Selection{}) {
		t.Fatalf("reset changed preferences: %+v %v", saved, err)
	}
}

func TestTUIAgentFormGoldens(t *testing.T) {
	for _, name := range []string{"create", "reset", "error"} {
		t.Run(name, func(t *testing.T) {
			reset := name == "reset"
			isolateHostState(t)
			pinHostCapacity(t, 16<<30, 100<<30)
			m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)
			cfg := vm.DefaultCreateConfig()
			cfg.Name, cfg.User, cfg.GitName, cfg.GitEmail = "agent-test", "tester", "Test Author", "test@example.com"
			if reset {
				m.openResetForm(registry.LocalScope, cfg.Name, cfg)
				m.toggleFocus = toggleIndexByLabel(m, "Preserve agent settings and files")
			} else {
				deliverToolsetLoad(t, &m, m.openForm())
				m.inputs[fUser].SetValue(cfg.User)
				m.inputs[fGitName].SetValue(cfg.GitName)
				m.inputs[fGitEmail].SetValue(cfg.GitEmail)
				m.inputs[fCPUs].SetValue("2")
				m.inputs[fMemory].SetValue("8GiB")
				m.toggleFocus = 3
			}
			m.hostDiskFree = 100 << 30
			if name == "error" {
				m.inputs[fGitEmail].SetValue("")
			}
			tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
			if reset {
				tm.Send(tea.KeyPressMsg{Code: tea.KeySpace})
			}
			if reset {
				waitForText(t, tm, "Do NOT preserve")
			} else {
				waitForText(t, tm, "Install Pi")
			}
			if name == "error" {
				tm.Send(ctrlKey('s'))
				waitForText(t, tm, "Error:")
			}
			screen := finalScreen(t, tm)
			if len(strings.Split(strings.TrimSuffix(ansi.Strip(string(screen)), "\n"), "\n")) > 24 {
				t.Fatal("form overflowed terminal")
			}
			for _, text := range []string{"ctrl+s", "esc"} {
				if !strings.Contains(string(screen), text) {
					t.Fatalf("missing footer %q", text)
				}
			}
			teatest.RequireEqualOutput(t, screen)
		})
	}
}

func TestAgentLoadGenerationAndIndividualEdits(t *testing.T) {
	m := newTestModel(t)
	m.openForm()
	stale := m.formGeneration
	m.kickFormToolsetLoad()
	m.createToggles()[3].set(&m, true)
	next, _ := m.Update(toolsetLoadedMsg{generation: stale, scope: m.formScope, agents: agentprefs.Selection{OpenCode: true}})
	m = next.(model)
	if !m.agentsLoading || m.toolOpenCode {
		t.Fatal("old request from the same profile was applied")
	}
	next, _ = m.Update(toolsetLoadedMsg{generation: m.formGeneration, scope: m.formScope, agents: agentprefs.Selection{Codex: true}})
	m = next.(model)
	if m.agentsLoading || m.toolClaude || !m.toolCodex || m.toolOpenCode || !m.toolPi {
		t.Fatal("migration must update untouched choices while retaining the edited Pi checkbox")
	}
}

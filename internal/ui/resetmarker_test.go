package ui

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"
	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// markerResetProvider is a host where a VM carries a provenance marker that
// this machine's index has never recorded: another controller built it.
type markerResetProvider struct {
	*providerfake.Provider
	markers map[string]provider.Provenance
}

func (p markerResetProvider) Provenance(context.Context) (map[string]provider.Provenance, error) {
	return p.markers, nil
}
func (p markerResetProvider) ProvenanceOf(_ context.Context, name string) (provider.Provenance, bool, error) {
	v, ok := p.markers[name]
	return v, ok, nil
}
func (p markerResetProvider) MarkManaged(_ context.Context, name string, pv provider.Provenance) error {
	p.markers[name] = pv
	return nil
}
func (p markerResetProvider) Unmark(context.Context, string) error { return nil }

func resetCommand(t *testing.T) vmCommand {
	t.Helper()
	for _, c := range vmCommands {
		if c.binding.Help().Key == "R" {
			return c
		}
	}
	t.Fatal("no R command")
	return vmCommand{}
}

func markerRecordedVM() vm.CreateConfig {
	return vm.CreateConfig{
		Name: "web", BaseName: "work-base", Hostname: "web-box", User: "ada",
		GitName: "Ada", GitEmail: "ada@example.com",
		CPUs: 2, Memory: "6GiB", Disk: "120GiB",
		CloneURL:  "https://github.com/octocat/hello",
		WithCodex: true, WithPi: true,
	}
}

// TestResetFormOfAMarkerOnlyVMKeepsItsProject is the TUI half of "a reset
// keeps the target VM's identity and project" for a VM known only by its
// marker. The form must open from the marker's config, and what reaches the
// provider — with preserve-project on — must name the recorded repo, sizing
// and agents, not DefaultCreateConfig()'s.
func TestResetFormOfAMarkerOnlyVMKeepsItsProject(t *testing.T) {
	isolateHostState(t)
	pinHostCapacity(t, 16<<30, 500<<30)
	rec := markerRecordedVM()
	type resetCall struct {
		cfg  vm.CreateConfig
		opts provision.ResetOptions
	}
	got := make(chan resetCall, 1)
	p := markerResetProvider{
		Provider: &providerfake.Provider{
			HostFilesFunc: lima.LocalFiles,
			ResetFunc: func(_ context.Context, cfg vm.CreateConfig, opts provision.ResetOptions, _ io.Writer) error {
				got <- resetCall{cfg, opts}
				return nil
			},
		},
		markers: map[string]provider.Provenance{"web": provider.NewProvenance(rec, false)},
	}
	m := New(singleFleet(p, registry.LocalScope)).(model)
	if _, found := m.reg.ConfigInScope("web", registry.LocalScope); found {
		t.Fatal("precondition: the index must not know this VM")
	}
	resetCommand(t).action(&m, boardVM{VM: vm.VM{Name: "web", Status: limaRunning}, scope: registry.LocalScope})

	if m.view != viewForm || !m.resetMode {
		t.Fatalf("reset form did not open (view %v)", m.view)
	}
	if m.resetCloneURL != rec.CloneURL || !m.projectToggleEnabled {
		t.Fatalf("reset form repo = %q (project toggle %v), want the marker's %q", m.resetCloneURL, m.projectToggleEnabled, rec.CloneURL)
	}
	if m.inputs[fMemory].Value() != rec.Memory || m.inputs[fDisk].Value() != rec.Disk {
		t.Errorf("reset form sizing = %s/%s, want the marker's %s/%s", m.inputs[fMemory].Value(), m.inputs[fDisk].Value(), rec.Memory, rec.Disk)
	}
	m.preserveProject = true
	m.hostDiskFree = 500 << 30

	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	waitForText(t, tm, "Reset VM")
	tm.Send(ctrlKey('s'))
	select {
	case c := <-got:
		if c.cfg.CloneURL != rec.CloneURL || c.cfg.Memory != rec.Memory || c.cfg.Disk != rec.Disk || c.cfg.BaseName != rec.BaseName {
			t.Errorf("provider got url %q mem %s disk %s base %s; want %q %s %s %s",
				c.cfg.CloneURL, c.cfg.Memory, c.cfg.Disk, c.cfg.BaseName, rec.CloneURL, rec.Memory, rec.Disk, rec.BaseName)
		}
		if c.cfg.WithClaude || !c.cfg.WithCodex || c.cfg.WithOpenCode || !c.cfg.WithPi {
			t.Errorf("provider got agents %+v, want the marker's (codex, pi)", c.cfg.AgentPtrs())
		}
		if !c.opts.PreserveProject {
			t.Error("preserve-project did not reach the provider")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reset did not reach the provider")
	}
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEsc})
	finalModel(t, tm)
}

// TestResetFormRefusesToPreserveIntoAnUnrecordedRebuild: with no config in the
// marker or the index the form opens from defaults, and asking it to keep
// anything is refused before the VM is touched, as `sandbar reset` refuses.
func TestResetFormRefusesToPreserveIntoAnUnrecordedRebuild(t *testing.T) {
	isolateHostState(t)
	called := false
	p := markerResetProvider{
		Provider: &providerfake.Provider{
			ResetFunc: func(context.Context, vm.CreateConfig, provision.ResetOptions, io.Writer) error {
				called = true
				return nil
			},
		},
		markers: map[string]provider.Provenance{"web": provider.NewProvenance(vm.CreateConfig{BaseName: "sandbar-base"}, false)},
	}
	m := New(singleFleet(p, registry.LocalScope)).(model)
	resetCommand(t).action(&m, boardVM{VM: vm.VM{Name: "web", Status: limaRunning}, scope: registry.LocalScope})
	if !m.resetMode || !m.resetUnrecorded {
		t.Fatalf("form open %v, unrecorded %v; want an open form marked unrecorded", m.resetMode, m.resetUnrecorded)
	}
	m.inputs[fGitName].SetValue("Ada")
	m.inputs[fGitEmail].SetValue("ada@example.com")
	m.inputs[fUser].SetValue("ada")
	m.hostDiskFree = 500 << 30
	m.preserveAgents = true

	cfg, err := m.buildConfig()
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	next, cmd := m.submitReset(cfg)
	m = next.(model)
	if m.formErr == nil || !strings.Contains(m.formErr.Error(), "no recorded configuration") || cmd != nil {
		t.Fatalf("formErr = %v, cmd nil %v; want a refusal and no reset", m.formErr, cmd == nil)
	}
	if called {
		t.Fatal("the provider's Reset ran")
	}
}

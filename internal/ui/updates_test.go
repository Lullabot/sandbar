package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

type updateTestProvider struct {
	*providerfake.Provider
	markers     map[string]provider.Provenance
	base, setup string
}

type progressTestProvider struct {
	updateTestProvider
	progressCalls int
	markCalls     int
}

func (p *progressTestProvider) MarkManaged(context.Context, string, provider.Provenance) error {
	p.markCalls++
	return nil
}
func (p *progressTestProvider) MarkProgress(context.Context, string, provider.Provenance) error {
	p.progressCalls++
	return nil
}

func (p updateTestProvider) Provenance(context.Context) (map[string]provider.Provenance, error) {
	return p.markers, nil
}
func (p updateTestProvider) ProvenanceOf(_ context.Context, name string) (provider.Provenance, bool, error) {
	v, ok := p.markers[name]
	return v, ok, nil
}
func (p updateTestProvider) MarkManaged(context.Context, string, provider.Provenance) error {
	return nil
}
func (p updateTestProvider) Unmark(context.Context, string) error { return nil }
func (p updateTestProvider) DesiredBaseRevision(context.Context, vm.CreateConfig) string {
	return p.base
}
func (p updateTestProvider) DesiredSetupRevision(context.Context) string { return p.setup }

func TestRefreshAndTileShowIndependentUpdates(t *testing.T) {
	marker := provider.NewProvenance(vm.CreateConfig{Name: "dev", BaseName: "sandbar-base"}, false)
	marker.BaseRevision, marker.SetupRevision = "old-base", "old-setup"
	p := updateTestProvider{Provider: &providerfake.Provider{ListFunc: func() ([]vm.VM, error) {
		return []vm.VM{{Name: "dev", Status: "Stopped"}}, nil
	}}, markers: map[string]provider.Provenance{"dev": marker}, base: "new-base", setup: "new-setup"}
	msg := refreshCmd(registry.LocalScope, p, nil, false)().(vmsLoadedMsg)
	if got := msg.updates["dev"]; got.Base != provider.UpdateAvailable || got.Setup != provider.UpdateAvailable {
		t.Fatalf("refresh update status = %+v", got)
	}
	view := renderTile(tileInput{VM: vm.VM{Name: "dev", Status: "Stopped"}, Width: 40, Update: msg.updates["dev"], HasUpdate: true})
	if !strings.Contains(view, "base and setup updates") || !strings.Contains(view, "R reset") {
		t.Fatalf("tile has no combined notice or reset action:\n%s", view)
	}
}

func TestTileUpdateNoticesAndFailedBuild(t *testing.T) {
	for _, tc := range []struct {
		status provider.UpdateStatus
		want   string
	}{
		{provider.UpdateStatus{Base: provider.UpdateAvailable, Setup: provider.UpdateCurrent}, "base update available"},
		{provider.UpdateStatus{Base: provider.UpdateCurrent, Setup: provider.UpdateAvailable}, "setup update available"},
		{provider.UpdateStatus{Base: provider.UpdateUnknown, Setup: provider.UpdateCurrent}, "base version unknown"},
		{provider.UpdateStatus{Base: provider.UpdateAvailable, Setup: provider.UpdateUnknown}, "base update"},
	} {
		view := renderTile(tileInput{VM: vm.VM{Name: "dev", Status: "Stopped"}, Width: 40, Update: tc.status, HasUpdate: true})
		if !strings.Contains(view, tc.want) || !strings.Contains(view, "R reset") {
			t.Errorf("status %+v: tile lacks %q or reset advice:\n%s", tc.status, tc.want, view)
		}
	}
	status := provider.UpdateStatus{Base: provider.UpdateAvailable, Setup: provider.UpdateAvailable}
	for _, tc := range []struct {
		name string
		in   tileInput
	}{
		{"building", tileInput{VM: vm.VM{Name: "dev", Status: "Running"}, Width: 40, Update: status, HasUpdate: true, RemoteProvisioning: true}},
		{"failed", tileInput{VM: vm.VM{Name: "dev", Status: "Running"}, Width: 40, Update: status, HasUpdate: true, Job: jobSnapshot{State: jobFailed, Err: errors.New("failed"), Provision: true}, HasJob: true}},
	} {
		if view := renderTile(tc.in); strings.Contains(view, "updates") || strings.Contains(view, "R reset") {
			t.Errorf("%s tile offered update reset during a failed/in-flight build:\n%s", tc.name, view)
		}
	}
}

func TestTileUpdateNoticePreservesWorkBadge(t *testing.T) {
	view := renderTile(tileInput{VM: vm.VM{Name: "dev", Status: "Stopped"}, Width: 40,
		Update: provider.UpdateStatus{Base: provider.UpdateAvailable, Setup: provider.UpdateUnknown}, HasUpdate: true, Badge: "dirty"})
	if !strings.Contains(view, "base update") || !strings.Contains(view, "setup unknown") || !strings.Contains(view, "R") || !strings.Contains(view, "dirty") {
		t.Fatalf("notice or work badge lost:\n%s", view)
	}
	view = renderTile(tileInput{VM: vm.VM{Name: "dev", Status: "Stopped"}, Width: 32,
		Update: provider.UpdateStatus{Base: provider.UpdateAvailable, Setup: provider.UpdateAvailable}, HasUpdate: true})
	if !strings.Contains(view, "base/setup updates") || !strings.Contains(view, "R") {
		t.Fatalf("narrow tile lost one update state or reset key:\n%s", view)
	}
	view = renderTile(tileInput{VM: vm.VM{Name: "dev", Status: "Stopped"}, Width: 32,
		Update: provider.UpdateStatus{Base: provider.UpdateAvailable, Setup: provider.UpdateUnknown}, HasUpdate: true, Badge: "dirty"})
	if !strings.Contains(view, "base↑") || !strings.Contains(view, "setup?") || !strings.Contains(view, "R reset") || !strings.Contains(view, "dirty") {
		t.Fatalf("narrow tile lost a state, reset advice or work badge:\n%s", view)
	}
}

func TestTUIUpdateTileGoldens(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status provider.UpdateStatus
	}{
		{"base", provider.UpdateStatus{Base: provider.UpdateAvailable, Setup: provider.UpdateCurrent}},
		{"setup", provider.UpdateStatus{Base: provider.UpdateCurrent, Setup: provider.UpdateAvailable}},
		{"unknown", provider.UpdateStatus{Base: provider.UpdateUnknown, Setup: provider.UpdateUnknown}},
		{"both", provider.UpdateStatus{Base: provider.UpdateAvailable, Setup: provider.UpdateAvailable}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := renderTile(tileInput{VM: vm.VM{Name: "dev", Status: "Stopped"}, Width: 40, Update: tc.status, HasUpdate: true})
			golden.RequireEqual(t, []byte(ansi.Strip(out)))
		})
	}
}

func TestFormTemplateFreshnessUsesProviderBaseRevision(t *testing.T) {
	isolateHostState(t)
	m := New(singleFleet(updateTestProvider{Provider: &providerfake.Provider{}, base: "published-image-v1"}, registry.LocalScope)).(model)
	if err := m.reg.AddTemplate(registry.Template{Name: "golden", Scope: registry.LocalScope,
		PlaybookVersion: "published-image-v1", Config: vm.CreateConfig{Name: "dev", BaseName: "sandbar-base"}}); err != nil {
		t.Fatal(err)
	}
	rows := m.computeFormSourceRows(m.formProvider(), registry.LocalScope)
	if len(rows) != 1 || rows[0].Stale {
		t.Fatalf("matching provider source should be current: %+v", rows)
	}
}

func TestPublishProgressUsesSerializedProviderPath(t *testing.T) {
	p := &progressTestProvider{}
	cmd := publishProgressCmd(p, provisionKey(registry.LocalScope, "dev"), vm.CreateConfig{Name: "dev"}, provider.BuildProgress{})
	if cmd == nil {
		t.Fatal("no progress command")
	}
	cmd()
	if p.progressCalls != 1 || p.markCalls != 0 {
		t.Fatalf("progress calls = %d, marker replacements = %d", p.progressCalls, p.markCalls)
	}
}

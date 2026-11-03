package ui

import (
	"context"
	"testing"

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

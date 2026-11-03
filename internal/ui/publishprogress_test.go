package ui

import (
	"context"
	"testing"

	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// progressTestProvider counts which write path a progress update takes: the
// serialized MarkProgress, or a whole-marker MarkManaged replacement that
// could erase a completed marker's revisions.
type progressTestProvider struct {
	progressCalls int
	markCalls     int
}

func (p *progressTestProvider) Provenance(context.Context) (map[string]provider.Provenance, error) {
	return nil, nil
}
func (p *progressTestProvider) ProvenanceOf(context.Context, string) (provider.Provenance, bool, error) {
	return provider.Provenance{}, false, nil
}
func (p *progressTestProvider) MarkManaged(context.Context, string, provider.Provenance) error {
	p.markCalls++
	return nil
}
func (p *progressTestProvider) MarkProgress(context.Context, string, provider.Provenance) error {
	p.progressCalls++
	return nil
}
func (p *progressTestProvider) Unmark(context.Context, string) error { return nil }

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

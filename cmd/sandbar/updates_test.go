package main

import (
	"context"

	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/vm"
)

type updatesProvider struct {
	*providerfake.Provider
	marker      provider.Provenance
	found       bool
	base, setup string
}

func (p updatesProvider) ProvenanceOf(context.Context, string) (provider.Provenance, bool, error) {
	return p.marker, p.found, nil
}
func (p updatesProvider) Provenance(context.Context) (map[string]provider.Provenance, error) {
	return nil, nil
}
func (p updatesProvider) MarkManaged(context.Context, string, provider.Provenance) error {
	panic("read-only update check wrote marker")
}
func (p updatesProvider) Unmark(context.Context, string) error {
	panic("read-only update check removed marker")
}
func (p updatesProvider) DesiredBaseRevision(context.Context, vm.CreateConfig) string { return p.base }
func (p updatesProvider) DesiredSetupRevision(context.Context) string                 { return p.setup }

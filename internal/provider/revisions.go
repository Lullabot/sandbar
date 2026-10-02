package provider

import (
	"context"
	"os"

	"github.com/lullabot/sandbar/internal/baseimage"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/vm"
)

// Local and remote Lima use the same revision logic. The base stamp is read
// through this provider's HostFiles, so remote Lima never consults local state.
func (p *limaProvider) DesiredBaseRevision(_ context.Context, cfg vm.CreateConfig) string {
	if p.prov == nil || p.prov.UsePublishedBase {
		return baseimage.PinnedManifest.Version
	}
	fsys := provision.CurrentPlaybookFS()
	if p.prov.PlaybookDir != "" {
		fsys = os.DirFS(p.prov.PlaybookDir)
	}
	v, err := provision.PlaybookVersion(fsys, cfg.ToolsetKey())
	if err != nil {
		return ""
	}
	return v
}

func (p *limaProvider) DesiredSetupRevision(context.Context) string {
	fsys := provision.CurrentPlaybookFS()
	if p.prov != nil && p.prov.PlaybookDir != "" {
		fsys = os.DirFS(p.prov.PlaybookDir)
	}
	v, err := provision.SetupVersion(fsys)
	if err != nil {
		return ""
	}
	return v
}

func (p *proxmoxProvider) DesiredBaseRevision(_ context.Context, cfg vm.CreateConfig) string {
	v, err := p.templateVersion(cfg)
	if err != nil {
		return ""
	}
	return v
}

func (p *proxmoxProvider) DesiredSetupRevision(context.Context) string {
	v, err := provision.SetupVersion(provision.CurrentPlaybookFS())
	if err != nil {
		return ""
	}
	return v
}

func (p *proxmoxProvider) markCompleted(ctx context.Context, cfg vm.CreateConfig, sourceRevision, setupRevision string) {
	pv := NewProvenance(cfg, false)
	pv.BaseRevision = sourceRevision
	pv.SetupRevision = setupRevision
	_ = p.MarkManaged(ctx, cfg.Name, pv)
}

var _ RevisionProvider = (*limaProvider)(nil)
var _ RevisionProvider = (*proxmoxProvider)(nil)

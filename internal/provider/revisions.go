package provider

import (
	"context"

	"github.com/lullabot/sandbar/internal/vm"
)

func (p *proxmoxProvider) markCompleted(ctx context.Context, cfg vm.CreateConfig, sourceRevision, setupRevision string) {
	pv := NewProvenance(cfg, false)
	pv.BaseRevision = sourceRevision
	pv.SetupRevision = setupRevision
	_ = p.MarkManaged(ctx, cfg.Name, pv)
}

// revisionCapture keeps the revisions a provisioning run reports so the
// provider can record them on success, while still forwarding each one to a
// callback the caller supplied.
type revisionCapture struct{ source, setup string }

func (c *revisionCapture) wrap(onSource, onSetup *func(string)) {
	prevSource, prevSetup := *onSource, *onSetup
	*onSource = func(v string) {
		c.source = v
		if prevSource != nil {
			prevSource(v)
		}
	}
	*onSetup = func(v string) {
		c.setup = v
		if prevSetup != nil {
			prevSetup(v)
		}
	}
}

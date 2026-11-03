package provider

import (
	"context"

	"github.com/lullabot/sandbar/internal/vm"
)

// UpdateState describes one independently comparable revision. Unknown is
// deliberately the default when either side of the comparison is unavailable.
type UpdateState string

const (
	UpdateUnknown   UpdateState = "unknown"
	UpdateCurrent   UpdateState = "current"
	UpdateAvailable UpdateState = "update_available"
)

type UpdateStatus struct {
	Base  UpdateState `json:"base"`
	Setup UpdateState `json:"setup"`
}

// RevisionProvider is optional so lightweight Provider fakes and future
// backends can degrade to unknown. Implementations read metadata/source files
// only; they never start a guest or run provisioning.
type RevisionProvider interface {
	DesiredBaseRevision(context.Context, vm.CreateConfig) string
	DesiredSetupRevision(context.Context) string
}

func compareRevision(have, want string) UpdateState {
	if have == "" || want == "" {
		return UpdateUnknown
	}
	if have == want {
		return UpdateCurrent
	}
	return UpdateAvailable
}

// CheckUpdates is safe to call in a background CLI/TUI command. A failed
// desired-revision lookup affects only that field and does not mutate the VM.
func CheckUpdates(ctx context.Context, p Provider, marker Provenance) UpdateStatus {
	status := UpdateStatus{Base: UpdateUnknown, Setup: UpdateUnknown}
	r, ok := p.(RevisionProvider)
	if !ok {
		return status
	}
	status.Base = compareRevision(marker.BaseRevision, r.DesiredBaseRevision(ctx, marker.Config))
	status.Setup = compareRevision(marker.SetupRevision, r.DesiredSetupRevision(ctx))
	return status
}

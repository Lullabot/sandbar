package provider

import (
	"context"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"
)

type revisionStub struct {
	Provider
	base, setup string
}

func (s revisionStub) DesiredBaseRevision(context.Context, vm.CreateConfig) string { return s.base }
func (s revisionStub) DesiredSetupRevision(context.Context) string                 { return s.setup }

func TestCheckUpdatesIndependentStates(t *testing.T) {
	cases := []struct {
		name, storedBase, storedSetup, desiredBase, desiredSetup string
		wantBase, wantSetup                                      UpdateState
	}{
		{"current", "b2", "s2", "b2", "s2", UpdateCurrent, UpdateCurrent},
		{"base stale", "b1", "s2", "b2", "s2", UpdateAvailable, UpdateCurrent},
		{"setup stale", "b2", "s1", "b2", "s2", UpdateCurrent, UpdateAvailable},
		{"both stale", "b1", "s1", "b2", "s2", UpdateAvailable, UpdateAvailable},
		{"legacy marker", "", "", "b2", "s2", UpdateUnknown, UpdateUnknown},
		{"desired unavailable", "b1", "s1", "", "", UpdateUnknown, UpdateUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckUpdates(context.Background(), revisionStub{base: tc.desiredBase, setup: tc.desiredSetup}, Provenance{BaseRevision: tc.storedBase, SetupRevision: tc.storedSetup})
			if got.Base != tc.wantBase || got.Setup != tc.wantSetup {
				t.Fatalf("CheckUpdates = %+v, want base=%s setup=%s", got, tc.wantBase, tc.wantSetup)
			}
		})
	}
}

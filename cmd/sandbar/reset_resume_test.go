package main

import (
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// The half-built replacement a failed reset left behind is replaced, but only
// when it is provably that replacement; anything else under the name is
// refused, with the reason.
func TestResumeResetReplacesOnlyItsOwnRebuild(t *testing.T) {
	m := &provision.ResetManifest{Schema: 1, Config: recordedVM(), Plan: provision.PreservePlan{WholeHome: true}}
	dir := backupDir(t, m, "home.tar")
	live := []vm.VM{{Name: "web"}}

	o, explicit := resumeFlags(dir)
	if _, _, replace, err := resumeReset(registry.NewEmpty(), "web", registry.LocalScope, o, explicit, "ada", live, true); err != nil || !replace {
		t.Errorf("own rebuild: replace=%v err=%v, want it replaced", replace, err)
	}
	o, explicit = resumeFlags(dir)
	if _, _, replace, err := resumeReset(registry.NewEmpty(), "web", registry.LocalScope, o, explicit, "ada", live, false); err == nil || replace || !strings.Contains(err.Error(), "nothing shows it is the half-built VM") {
		t.Errorf("unknown VM: replace=%v err=%v, want a refusal explaining why", replace, err)
	}
	// Refusals that come before the delete still win over a replaceable VM.
	o, explicit = resumeFlags(dir)
	o.preserveHome = true
	if _, _, replace, err := resumeReset(registry.NewEmpty(), "web", registry.LocalScope, o, explicit, "ada", live, true); err == nil || replace {
		t.Errorf("conflicting flags: replace=%v err=%v, want a refusal before any delete", replace, err)
	}
}

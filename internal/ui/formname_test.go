package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// pickyNameProvider is a backend that refuses any name containing an underscore
// — Proxmox's rule, in one line, and the exact case that was reported.
func pickyNameProvider(created *int) *providerfake.Provider {
	return &providerfake.Provider{
		ValidateNameFunc: func(name string) error {
			if strings.Contains(name, "_") {
				return errors.New("proxmox: name " + name + " cannot contain '_' — Proxmox requires a DNS name")
			}
			return nil
		},
		CreateFunc: func(_ context.Context, _ vm.CreateConfig, _ provision.CreateOptions, _ io.Writer) error {
			*created++
			return nil
		},
	}
}

// fillCreateForm types a name plus the git identity Validate insists on, so the
// only thing a submission can fail on is the name.
func fillCreateForm(t *testing.T, m *model, name string) {
	t.Helper()
	deliverToolsetLoad(t, m, m.openForm())
	m.inputs[fName].SetValue(name)
	m.inputs[fGitName].SetValue("Dev")
	m.inputs[fGitEmail].SetValue("dev@example.com")
}

// TestCreateFormRefusesANameTheBackendWillNotTake is the whole feature, asserted
// at the boundary that matters: pressing enter on a name the backend will refuse
// must leave the user in the form with an explanation, and must NOT start a run.
//
// Starting one is what the bug was. A create that fails at clone time has
// already claimed a tile, and that tile stays — red, un-deletable (the cleanup
// delete fails in its turn against an instance that was never created) — for the
// rest of the session. So "no job was begun" is the assertion, not "an error
// string appeared": a golden could show the error and the job could still be
// running behind it.
func TestCreateFormRefusesANameTheBackendWillNotTake(t *testing.T) {
	isolateHostState(t)
	created := 0
	m := New(singleFleet(pickyNameProvider(&created), registry.LocalScope)).(model)
	fillCreateForm(t, &m, "test_vm")

	next, cmd := m.submitForm()
	m = next.(model)

	if m.formErr == nil {
		t.Fatal("submitting an invalid name left formErr nil; the user is given no reason")
	}
	if !strings.Contains(m.formErr.Error(), "test_vm") {
		t.Errorf("formErr = %q; it must quote the name the user typed", m.formErr)
	}
	if m.view != viewForm {
		t.Errorf("view = %v after a refused name; the form must stay open so the name can be corrected", m.view)
	}
	if cmd != nil {
		t.Error("submitForm returned a command for a refused name; nothing should be set in motion")
	}
	if m.jobs.exists(provisionKey(registry.LocalScope, "test_vm")) {
		t.Error("a job was registered for a name the backend refuses — this is the tile that gets stuck")
	}
	if created != 0 {
		t.Errorf("provider.Create was called %d time(s) for a name the backend refuses", created)
	}
}

// TestCreateFormAcceptsAValidName is the negative control: the same provider,
// the same path, a name it does not object to — a create must still start, or
// the check above would be indistinguishable from a form that never submits.
func TestCreateFormAcceptsAValidName(t *testing.T) {
	isolateHostState(t)
	created := 0
	m := New(singleFleet(pickyNameProvider(&created), registry.LocalScope)).(model)
	fillCreateForm(t, &m, "test-vm")

	next, cmd := m.submitForm()
	m = next.(model)

	if m.formErr != nil {
		t.Fatalf("formErr = %v for a name the backend accepts", m.formErr)
	}
	if cmd == nil {
		t.Fatal("submitForm returned no command for a valid name; the create never started")
	}
	if !m.jobs.exists(provisionKey(registry.LocalScope, "test-vm")) {
		t.Fatal("no provision job was registered for a valid name")
	}
	// submitForm starts the create on a goroutine. Drain its stream before the
	// test's isolated host directory is removed: the run saves agent preferences
	// there before calling the provider.
	if _, err := io.Copy(io.Discard, m.jobs.reader(provisionKey(registry.LocalScope, "test-vm")).r); err != nil {
		t.Fatalf("create job failed: %v", err)
	}
	if created != 1 {
		t.Errorf("provider.Create was called %d times; want 1", created)
	}
}

// TestResetDoesNotApplyTheBackendNameRule pins the deliberate exemption. A reset
// targets a VM that already exists and whose name is NOT an editable field, so a
// rule it failed would be an error with nothing to act on — and would strand any
// VM created before the rule existed, or by a backend that once allowed the
// name. Do not "fix" this by moving the check somewhere both paths share.
func TestResetDoesNotApplyTheBackendNameRule(t *testing.T) {
	isolateHostState(t)
	created := 0
	m := New(singleFleet(pickyNameProvider(&created), registry.LocalScope)).(model)

	// A managed VM whose name the backend would refuse today.
	v := vm.VM{Name: "legacy_box", Status: "Stopped"}
	m = putOnBoard(t, m, v)
	m.openResetForm(registry.LocalScope, v.Name, vm.CreateConfig{Name: v.Name, BaseName: "sandbar-base", CPUs: 2, Memory: "8GiB", Disk: "100GiB"})
	m.inputs[fGitName].SetValue("Dev")
	m.inputs[fGitEmail].SetValue("dev@example.com")

	next, cmd := m.submitForm()
	m = next.(model)

	if m.formErr != nil {
		t.Fatalf("reset of %q was refused: %v — an existing VM must stay rebuildable", v.Name, m.formErr)
	}
	if cmd == nil {
		t.Fatal("reset produced no command")
	}
}

// TestProviderSeamIsWhatTheFormAsks guards the wiring rather than the rule: the
// form must ask the provider bound to the SELECTED profile, because that is the
// only thing that knows which backend the VM is headed for. Asking anything else
// (a package-level default, the first member, vm.CreateConfig.Validate) gives
// the right answer only on a single-profile machine.
func TestProviderSeamIsWhatTheFormAsks(t *testing.T) {
	isolateHostState(t)
	var asked []string
	prov := &providerfake.Provider{
		ValidateNameFunc: func(name string) error {
			asked = append(asked, name)
			return errors.New("stop after validation")
		},
	}
	var _ provider.Provider = prov

	m := New(singleFleet(prov, registry.LocalScope)).(model)
	fillCreateForm(t, &m, "web")
	_, cmd := m.submitForm()
	if cmd != nil {
		t.Error("name validation failed, but submitForm started a create job")
	}

	if len(asked) != 1 || asked[0] != "web" {
		t.Errorf("provider was asked about %v; want exactly [web]", asked)
	}
}

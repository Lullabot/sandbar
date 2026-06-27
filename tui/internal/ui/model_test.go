package ui

import (
	"context"
	"io"
	"testing"

	"github.com/deviantintegral/claude-code-ansible/tui/internal/lima"
	"github.com/deviantintegral/claude-code-ansible/tui/internal/provision"
	"github.com/deviantintegral/claude-code-ansible/tui/internal/vm"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeRunner is a no-op lima.Runner so the model can be constructed and driven
// in tests without ever spawning a real limactl.
type fakeRunner struct{}

func (fakeRunner) Output(context.Context, ...string) ([]byte, error)             { return nil, nil }
func (fakeRunner) Stream(context.Context, io.Reader, io.Writer, ...string) error { return nil }

func newTestModel(t *testing.T) model {
	t.Helper()
	// Isolate the managed-VM registry to a temp dir so tests never read or write
	// the developer's real index.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cli := lima.New(fakeRunner{})
	prov := &provision.Provisioner{Lima: cli}
	m, ok := New(cli, prov).(model)
	if !ok {
		t.Fatalf("New did not return a model")
	}
	return m
}

func runeKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// Pressing 'd' on a populated list opens the confirm-delete overlay for the
// highlighted VM (Delete must always confirm before destroying).
func TestDeleteKeyEntersConfirm(t *testing.T) {
	m := newTestModel(t)

	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "claude", Status: "Running", CPUs: 2},
	}})
	m = loaded.(model)

	if m.confirming {
		t.Fatalf("model should not start in confirming state")
	}

	next, _ := m.Update(runeKey('d'))
	m = next.(model)

	if !m.confirming {
		t.Fatalf("pressing 'd' should enter confirm state")
	}
	if m.confirmName != "claude" {
		t.Fatalf("confirmName = %q, want %q", m.confirmName, "claude")
	}
}

// Recreate must be gated to claude-vm-managed VMs: pressing 'r' in the confirm
// overlay on an UNMANAGED VM is a no-op (it must never replace an unrelated VM
// with a Claude sandbox).
func TestRecreateGatedForUnmanagedVM(t *testing.T) {
	m := newTestModel(t) // empty (temp) registry => nothing is managed

	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "default", Status: "Running", CPUs: 2},
	}})
	m = loaded.(model)

	confirm, _ := m.Update(runeKey('d'))
	m = confirm.(model)
	if !m.confirming {
		t.Fatal("'d' should enter confirm state")
	}
	if m.confirmBase != "" {
		t.Fatalf("unmanaged VM must have no recreate base, got %q", m.confirmBase)
	}

	after, _ := m.Update(runeKey('r'))
	m = after.(model)
	if m.view == viewProgress || m.running {
		t.Fatal("recreate on an unmanaged VM must not start provisioning")
	}
}

// For a managed VM, recreate is available: 'r' starts provisioning.
func TestRecreateAllowedForManagedVM(t *testing.T) {
	m := newTestModel(t)
	if err := m.reg.Add(vm.CreateConfig{Name: "claude", BaseName: "claude-base"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "claude", Status: "Stopped", CPUs: 2},
	}})
	m = loaded.(model)

	confirm, _ := m.Update(runeKey('d'))
	m = confirm.(model)
	if m.confirmBase != "claude-base" {
		t.Fatalf("managed VM should carry its recreate base, got %q", m.confirmBase)
	}

	after, _ := m.Update(runeKey('r'))
	m = after.(model)
	if m.view != viewProgress || !m.running {
		t.Fatalf("recreate on a managed VM should start provisioning (view=%v running=%v)", m.view, m.running)
	}
}

// Backspace inside the create form must edit the focused field, not navigate
// back to the list. (The shared Back binding also matches backspace, so the form
// has to special-case it.)
func TestBackspaceEditsFieldInForm(t *testing.T) {
	m := newTestModel(t)

	opened, _ := m.Update(runeKey('n'))
	m = opened.(model)
	if m.view != viewForm {
		t.Fatalf("'n' should open the form, view = %v", m.view)
	}

	// Put a known value in the focused field (cursor lands at the end).
	m.inputs[m.focusIdx].SetValue("claude")

	after, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = after.(model)

	if m.view != viewForm {
		t.Fatalf("backspace must stay on the form, got view %v", m.view)
	}
	if got := m.inputs[m.focusIdx].Value(); got != "claud" {
		t.Fatalf("backspace should delete the last char: got %q, want %q", got, "claud")
	}
}

// Esc inside the create form returns to the list.
func TestEscLeavesForm(t *testing.T) {
	m := newTestModel(t)

	opened, _ := m.Update(runeKey('n'))
	m = opened.(model)
	if m.view != viewForm {
		t.Fatalf("'n' should open the form, view = %v", m.view)
	}

	after, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = after.(model)
	if m.view != viewList {
		t.Fatalf("esc should return to the list, got view %v", m.view)
	}
}

// The create form must open pre-populated with the script's defaults — notably
// hostname, primary user, CPUs, memory, and disk — not blank fields.
func TestNewInputsSeedsDefaults(t *testing.T) {
	in := newInputs()
	for _, f := range []struct {
		idx  int
		name string
	}{
		{fName, "name"}, {fHostname, "hostname"}, {fUser, "user"},
		{fCPUs, "cpus"}, {fMemory, "memory"}, {fDisk, "disk"},
	} {
		if in[f.idx].Value() == "" {
			t.Errorf("%s field should be seeded with a default, got empty", f.name)
		}
	}
}

// A blank field must fall back to its default (mirroring new-vm.sh) rather than
// being accepted empty: clearing name/hostname/user/memory/disk yields a valid,
// fully-populated config.
func TestBlankFieldsFallBackToDefaults(t *testing.T) {
	m := newTestModel(t)
	opened, _ := m.Update(runeKey('n'))
	m = opened.(model)

	m.inputs[fName].SetValue("")
	m.inputs[fHostname].SetValue("")
	m.inputs[fUser].SetValue("   ") // whitespace-only counts as blank
	m.inputs[fMemory].SetValue("")
	m.inputs[fDisk].SetValue("")
	m.inputs[fGitName].SetValue("Ada Lovelace")
	m.inputs[fGitEmail].SetValue("ada@example.com")

	cfg, err := m.buildConfig()
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	if cfg.Name != "claude" {
		t.Errorf("Name = %q, want default %q", cfg.Name, "claude")
	}
	if cfg.Hostname != "claude" {
		t.Errorf("Hostname = %q, want it to default to the name", cfg.Hostname)
	}
	if cfg.User == "" {
		t.Errorf("User should default to the host user, got empty")
	}
	if cfg.Memory != "8GiB" {
		t.Errorf("Memory = %q, want %q", cfg.Memory, "8GiB")
	}
	if cfg.Disk != "100GiB" {
		t.Errorf("Disk = %q, want %q", cfg.Disk, "100GiB")
	}
	if cfg.CPUs < 1 {
		t.Errorf("CPUs = %d, want a positive default", cfg.CPUs)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("a fully-defaulted config should validate, got %v", err)
	}
}

// Submitting the create form with an empty git name fails validation: the model
// stays on the form and surfaces the error instead of starting provisioning.
func TestSubmitFormValidationKeepsForm(t *testing.T) {
	m := newTestModel(t)

	opened, _ := m.Update(runeKey('n'))
	m = opened.(model)
	if m.view != viewForm {
		t.Fatalf("'n' should open the form, view = %v", m.view)
	}

	// Force the validation failure deterministically (the host git config may
	// otherwise seed a non-empty name).
	m.inputs[fGitName].SetValue("")

	submitted, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = submitted.(model)

	if m.view != viewForm {
		t.Fatalf("invalid submit should keep the form, view = %v", m.view)
	}
	if m.formErr == nil {
		t.Fatalf("invalid submit should surface a validation error")
	}
}

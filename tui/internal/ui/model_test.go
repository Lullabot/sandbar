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

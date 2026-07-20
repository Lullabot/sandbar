package ui

// snapshot_test.go carries the behavioral coverage for the golden-template
// feature's TUI surface — the snapshot verb actually starting a tracked job,
// and Reset correctly detecting golden-template provenance — leaving the
// visual regression coverage (the prompt screen, the form's source selector,
// the delete-confirmation text) to template_golden_test.go. Per the task's
// own scope note: these are the custom, critical-path behaviors; the
// framework plumbing underneath (beginStream, jobRegistry) already has its
// own tests elsewhere in this package.

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// TestSnapshotVerbStartsJob proves the signature behavior this task adds to
// the board: pressing 't' on a VM opens the name prompt, and submitting a
// valid name starts a REAL tracked job (kindSnapshot) rather than blocking or
// silently doing nothing — the same job/progress machinery every other verb
// in this package uses (progress.go, jobs.go).
func TestSnapshotVerbStartsJob(t *testing.T) {
	m := newTestModel(t)
	m = putOnBoard(t, m, vm.VM{Name: "claude", Status: limaRunning})

	next, _ := pressDispatch(t, m, runeKey('t'))
	m = next
	if m.view != viewSnapshotPrompt {
		t.Fatalf("'t' should open the snapshot-name prompt, got view %v", m.view)
	}

	m.snapshotInput.SetValue("golden")
	next2, cmd := pressDispatch(t, m, ctrlKey('s'))
	m = next2
	if cmd == nil {
		t.Fatal("submitting a valid template name should start a job")
	}
	scope := m.members[0].scope
	if !m.jobs.running(snapshotKey(scope, "claude")) {
		t.Fatal("expected a kindSnapshot job to be running for claude")
	}
	if m.view != viewBoard {
		t.Fatalf("starting the job should return to the board, got view %v", m.view)
	}
	if _, ok := m.pendingSnapshots[snapshotKey(scope, "claude")]; !ok {
		t.Fatal("expected the template metadata to be stashed pending the job's completion")
	}
}

// TestSnapshotPromptRejectsInvalidName proves the prompt validates through
// vm.ValidateTemplateName rather than starting a job for an unusable name —
// mirroring the create form's own validate-before-submit contract.
func TestSnapshotPromptRejectsInvalidName(t *testing.T) {
	m := newTestModel(t)
	m = putOnBoard(t, m, vm.VM{Name: "claude", Status: limaRunning})

	next, _ := pressDispatch(t, m, runeKey('t'))
	m = next
	m.snapshotInput.SetValue("   ") // slugs to empty
	next2, cmd := pressDispatch(t, m, ctrlKey('s'))
	m = next2
	if cmd != nil {
		t.Fatal("an invalid name should not start a job")
	}
	if m.view != viewSnapshotPrompt {
		t.Fatalf("an invalid name should keep the prompt open, got view %v", m.view)
	}
	if m.snapshotErr == nil {
		t.Fatal("expected a validation error to be surfaced")
	}
}

// TestResetOfTemplateSourcedVMSetsTemplateSource is the ADDITIONAL
// REQUIREMENT this task adds to the existing 'R' verb: a managed VM recorded
// with golden-template provenance (registry.AddScopedWithTemplate) must have
// its Reset carry ResetOptions.TemplateSource, so provision.Reset re-clones
// from the template instead of treating the recorded BaseName as an ordinary,
// buildable base image.
func TestResetOfTemplateSourcedVMSetsTemplateSource(t *testing.T) {
	m := newTestModel(t)
	scope := m.members[0].scope

	tmplName := "golden"
	inst := vm.TemplateInstanceName(tmplName)
	if err := m.reg.AddTemplate(registry.Template{
		Name: tmplName, Scope: scope, Source: "claude", CreatedAt: time.Now(),
		Config: vm.CreateConfig{Name: "claude", BaseName: inst},
	}); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	cfg := vm.CreateConfig{Name: "web", BaseName: inst, GitName: "Ada", GitEmail: "ada@example.com", CPUs: 2, Memory: "2GiB", Disk: "20GiB"}
	if err := m.reg.AddScopedWithTemplate(cfg, scope, tmplName); err != nil {
		t.Fatalf("seed managed VM with template provenance: %v", err)
	}

	gotOpts := make(chan provision.ResetOptions, 1)
	fake := &providerfake.Provider{ResetFunc: func(ctx context.Context, cfg vm.CreateConfig, opts provision.ResetOptions, out io.Writer) error {
		gotOpts <- opts
		return nil
	}}
	m.members[0].prov = fake
	m.formScope = scope
	m.resetMode = true
	m.resetName = "web"
	m.resetBaseName = inst

	m.submitReset(cfg)

	select {
	case opts := <-gotOpts:
		if opts.TemplateSource == "" {
			t.Fatal("expected ResetOptions.TemplateSource to be set for a template-provenanced VM")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("provider.Reset was never called")
	}
}

// TestResetOfOrdinaryVMLeavesTemplateSourceEmpty is the regression half of the
// requirement above: a VM cloned from the shared base image (no template
// provenance) must reset exactly as before — ResetOptions.TemplateSource
// stays empty, so Reset takes the ordinary base-ensure/converge path.
func TestResetOfOrdinaryVMLeavesTemplateSourceEmpty(t *testing.T) {
	m := newTestModel(t)
	scope := m.members[0].scope

	cfg := vm.CreateConfig{Name: "web", BaseName: "sandbar-base", GitName: "Ada", GitEmail: "ada@example.com", CPUs: 2, Memory: "2GiB", Disk: "20GiB"}
	if err := m.reg.AddScoped(cfg, scope); err != nil {
		t.Fatalf("seed managed VM: %v", err)
	}

	gotOpts := make(chan provision.ResetOptions, 1)
	fake := &providerfake.Provider{ResetFunc: func(ctx context.Context, cfg vm.CreateConfig, opts provision.ResetOptions, out io.Writer) error {
		gotOpts <- opts
		return nil
	}}
	m.members[0].prov = fake
	m.formScope = scope
	m.resetMode = true
	m.resetName = "web"
	m.resetBaseName = "sandbar-base"

	m.submitReset(cfg)

	select {
	case opts := <-gotOpts:
		if opts.TemplateSource != "" {
			t.Fatalf("expected ResetOptions.TemplateSource to stay empty for an ordinary VM, got %q", opts.TemplateSource)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("provider.Reset was never called")
	}
}

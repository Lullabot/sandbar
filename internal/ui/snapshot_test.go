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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"

	tea "charm.land/bubbletea/v2"
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

func TestLaunchSnapshotRejectsCollisionsAndMissingProvider(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *model, registry.Scope)
		scope func(registry.Scope) registry.Scope
	}{
		{
			name: "existing template",
			setup: func(t *testing.T, m *model, scope registry.Scope) {
				t.Helper()
				if err := m.reg.AddTemplate(registry.Template{Name: "golden", Scope: scope}); err != nil {
					t.Fatalf("seed template: %v", err)
				}
			},
			scope: func(scope registry.Scope) registry.Scope { return scope },
		},
		{
			name: "managed VM",
			setup: func(t *testing.T, m *model, scope registry.Scope) {
				t.Helper()
				if err := m.reg.AddScoped(vm.CreateConfig{Name: "golden"}, scope); err != nil {
					t.Fatalf("seed VM: %v", err)
				}
			},
			scope: func(scope registry.Scope) registry.Scope { return scope },
		},
		{
			name:  "base image",
			setup: func(t *testing.T, _ *model, _ registry.Scope) {},
			scope: func(scope registry.Scope) registry.Scope { return scope },
		},
		{
			name:  "missing provider",
			setup: func(t *testing.T, _ *model, _ registry.Scope) {},
			scope: func(registry.Scope) registry.Scope { return registry.Scope{Provider: "unavailable"} },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			scope := tc.scope(m.members[0].scope)
			tc.setup(t, &m, scope)
			m.openSnapshotPrompt(boardVM{VM: vm.VM{Name: "claude"}, scope: scope})
			name := "golden"
			if tc.name == "base image" {
				name = vm.DefaultCreateConfig().BaseName
			}
			m.snapshotInput.SetValue(name)

			next, cmd := m.launchSnapshot()
			got := next.(model)
			if cmd != nil {
				t.Fatal("a conflicting name or missing provider started a snapshot")
			}
			if got.snapshotErr == nil {
				t.Fatal("expected the rejected snapshot to explain why it could not start")
			}
		})
	}
}

func TestSnapshotPromptEscapeReturnsToBoard(t *testing.T) {
	m := newTestModel(t)
	m.openSnapshotPrompt(boardVM{VM: vm.VM{Name: "claude"}, scope: m.members[0].scope})
	next, cmd := pressDispatch(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmd != nil {
		t.Fatal("escape should not start a command")
	}
	if next.view != viewBoard {
		t.Fatalf("escape left the snapshot prompt on view %v, want board", next.view)
	}
}

func TestFinishSnapshotRecordsOnlySuccessfulSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name     string
		canceled bool
		err      error
		wantSave bool
	}{
		{name: "success", wantSave: true},
		{name: "canceled", canceled: true},
		{name: "failed", err: io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			scope := m.members[0].scope
			key := snapshotKey(scope, "claude")
			created := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
			cfg := vm.CreateConfig{Name: "claude", BaseName: vm.TemplateInstanceName("golden")}
			m.pendingSnapshots[key] = pendingSnapshot{
				name: "golden", cfg: cfg, createdAt: created,
				outcome: &provision.SnapshotResult{PlaybookVersion: "playbook-v1", ToolsetKey: "tools-v1"},
			}

			m.finishSnapshot(key, tc.canceled, tc.err)

			if _, pending := m.pendingSnapshots[key]; pending {
				t.Fatal("snapshot completion left pending metadata behind")
			}
			got, saved := m.reg.TemplateInScope("golden", scope)
			if saved != tc.wantSave {
				t.Fatalf("template saved = %v, want %v", saved, tc.wantSave)
			}
			if !tc.wantSave {
				return
			}
			if got.Source != "claude" || got.Config != cfg || !got.CreatedAt.Equal(created) {
				t.Errorf("saved template = %#v; source/config/created time were not preserved", got)
			}
			if got.PlaybookVersion != "playbook-v1" || got.ToolsetKey != "tools-v1" {
				t.Errorf("saved template metadata = %#v; snapshot result was not preserved", got)
			}
		})
	}
}

func TestFinishSnapshotIgnoresUnknownJob(t *testing.T) {
	m := newTestModel(t)
	m.finishSnapshot(snapshotKey(m.members[0].scope, "missing"), false, nil)
	if got := m.reg.TemplatesInScope(m.members[0].scope); len(got) != 0 {
		t.Fatalf("unknown snapshot job recorded templates: %+v", got)
	}
}

func TestFinishSnapshotReportsRegistryPersistenceFailure(t *testing.T) {
	m := newTestModel(t)
	scope := m.members[0].scope
	key := snapshotKey(scope, "claude")
	m.pendingSnapshots[key] = pendingSnapshot{
		name: "golden",
		cfg:  vm.CreateConfig{Name: "claude"},
		outcome: &provision.SnapshotResult{
			PlaybookVersion: "playbook-v1",
			ToolsetKey:      "tools-v1",
		},
	}

	// XDG_DATA_HOME is a per-test temp dir from newTestModel/isolateHostState.
	// Replace only that test's registry parent with a regular file so the atomic
	// save fails safely without touching developer state.
	registryDir := filepath.Join(os.Getenv("XDG_DATA_HOME"), "sandbar")
	if err := os.RemoveAll(registryDir); err != nil {
		t.Fatalf("remove isolated registry directory: %v", err)
	}
	if err := os.WriteFile(registryDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("block isolated registry directory: %v", err)
	}

	m.finishSnapshot(key, false, nil)

	if _, ok := m.reg.TemplateInScope("golden", scope); !ok {
		t.Fatal("the successfully captured template should remain available in memory")
	}
	found := false
	for _, log := range m.messages {
		if strings.Contains(log.text, "could not be recorded") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no user-visible warning about failed registry persistence in logs: %+v", m.messages)
	}
}

func TestDeleteTemplateCommandReportsProviderResult(t *testing.T) {
	scope := registry.LocalScope
	called := false
	p := &providerfake.Provider{DeleteTemplateFunc: func(_ context.Context, instance string, _ io.Writer) error {
		called = true
		if instance != vm.TemplateInstanceName("golden") {
			t.Errorf("DeleteTemplate instance = %q, want template instance name", instance)
		}
		return nil
	}}
	msg := deleteTemplateCmd(p, scope, "golden", vm.TemplateInstanceName("golden"))()
	done, ok := msg.(actionDoneMsg)
	if !ok {
		t.Fatalf("deleteTemplateCmd returned %T, want actionDoneMsg", msg)
	}
	if !called || done.action != "delete template" || done.name != "golden" || done.scope != scope || done.err != nil {
		t.Fatalf("delete result = %#v, provider called=%v", done, called)
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

package ui

// template_golden_test.go locks this task's new rendered surfaces: the
// snapshot-name prompt, the create form's clone-source selector (with a
// template's size/age/staleness rendered inline), and the template
// delete-confirmation raised from that selector. Ages are computed against a
// PINNED templateNowFn rather than the wall clock, and PlaybookVersion is
// seeded to a string that can never collide with a real content hash (see
// computeFormSourceRows), so every golden here is reproducible regardless of
// when or where it runs — never embedding a live date, per this task's own
// determinism requirement.

import (
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"

	"github.com/charmbracelet/x/exp/golden"
)

// seedTemplate records tmpl in the on-disk registry New will load (mirrors
// seedManagedScoped's pattern in fleet_test.go for VM entries).
func seedTemplate(t *testing.T, tmpl registry.Template) {
	t.Helper()
	reg, err := registry.Load()
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	if err := reg.AddTemplate(tmpl); err != nil {
		t.Fatalf("seed template %s: %v", tmpl.Name, err)
	}
}

// pinTemplateNow fixes computeFormSourceRows' notion of "now" for the
// duration of the test, so a template's age renders as a fixed, reproducible
// string (formatAgo) rather than a duration computed against the wall clock.
func pinTemplateNow(t *testing.T, when time.Time) {
	t.Helper()
	orig := templateNowFn
	templateNowFn = func() time.Time { return when }
	t.Cleanup(func() { templateNowFn = orig })
}

// TestTUISnapshotPromptGolden pins the 't' verb's name-entry screen.
func TestTUISnapshotPromptGolden(t *testing.T) {
	isolateHostState(t)
	pinHostCapacity(t, 16<<30, 100<<30)
	pinVersion(t, "v1.2.3")
	seedManagedScoped(t, registry.LocalScope, "claude")

	m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)
	m = resized(m, 100, 30)
	next, _ := m.Update(vmsLoadedMsg{
		scope: registry.LocalScope,
		vms:   []vm.VM{{Name: "claude", Status: "Running", CPUs: 2, Memory: "4294967296", Disk: "53687091200"}},
	})
	m = next.(model)

	next, _ = m.Update(runeKey('t'))
	m = next.(model)
	if m.view != viewSnapshotPrompt {
		t.Fatalf("precondition: 't' should open the snapshot prompt, got view %v", m.view)
	}

	golden.RequireEqual(t, renderModel(m))
}

// TestTUIFormSourceSelectorGolden pins the create form's clone-source
// selector cycled onto a saved template: its size (from a fake
// TemplateDiskBytesFunc, never the real filesystem), its age (from the
// pinned templateNowFn, exactly 3 days before "now"), and its staleness
// marker (PlaybookVersion seeded to a value that can never match a real
// content hash).
func TestTUIFormSourceSelectorGolden(t *testing.T) {
	isolateHostState(t)
	pinHostCapacity(t, 16<<30, 100<<30)
	pinVersion(t, "v1.2.3")
	fixedNow := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	pinTemplateNow(t, fixedNow)

	seedManagedScoped(t, registry.LocalScope, "claude")
	seedTemplate(t, registry.Template{
		Name:            "golden",
		Scope:           registry.LocalScope,
		Source:          "claude",
		CreatedAt:       fixedNow.Add(-72 * time.Hour), // formatAgo(72h) == "3d ago"
		PlaybookVersion: "not-a-real-content-hash",     // guaranteed to differ from any real stamp
		ToolsetKey:      "claude+ddev+go+java",
		Config:          vm.CreateConfig{Name: "claude", BaseName: vm.TemplateInstanceName("golden")},
	})

	prov := &providerfake.Provider{TemplateDiskBytesFunc: func(string) int64 { return 4 << 30 }}
	m := New(singleFleet(prov, registry.LocalScope)).(model)
	m = resized(m, 100, 30)
	next, _ := m.Update(vmsLoadedMsg{
		scope: registry.LocalScope,
		vms:   []vm.VM{{Name: "claude", Status: "Running", CPUs: 2, Memory: "4294967296", Disk: "53687091200"}},
	})
	m = next.(model)

	next, _ = m.Update(runeKey('n'))
	m = next.(model)
	if m.view != viewForm {
		t.Fatalf("precondition: 'n' should open the create form, got view %v", m.view)
	}
	// The disk-overflow warning compares the typed size against the REAL host's
	// free disk space (freeDiskBytes, form.go) — not portable across machines.
	// A tiny requested size keeps it well under whatever any test box actually
	// has free, so this golden's line count stays fixed everywhere.
	m.inputs[fDisk].SetValue("1GiB")
	// Pin the host-derived identity fields (fUser/fGitName/fGitEmail come from
	// hostUser()/hostGit(), which isolateHostState does not control) so the
	// golden is portable across machines and CI — same convention as the other
	// form golden tests.
	m.inputs[fUser].SetValue("ada")
	m.inputs[fGitName].SetValue("Ada Lovelace")
	m.inputs[fGitEmail].SetValue("ada@example.com")
	// The CPUs suggestion is half the host's core count (defaultCPUs → the
	// provider's HostResources, falling back to runtime.NumCPU) — not portable
	// across machines/CI. Pin it so the golden's value is fixed everywhere.
	m.inputs[fCPUs].SetValue("8")
	m.focusIdx = fSourceSelector
	m.cycleFormSource(1) // "" (base) -> "golden", the only saved template

	golden.RequireEqual(t, renderModel(m))
}

// TestTUIDeleteTemplateConfirmGolden pins the delete-confirmation raised by
// 'd' on the source selector's highlighted template: the disk size reclaimed
// and the (here, empty) dependents list — see confirmDeleteFormSource.
func TestTUIDeleteTemplateConfirmGolden(t *testing.T) {
	isolateHostState(t)
	pinHostCapacity(t, 16<<30, 100<<30)
	pinVersion(t, "v1.2.3")
	fixedNow := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	pinTemplateNow(t, fixedNow)

	seedManagedScoped(t, registry.LocalScope, "claude")
	seedTemplate(t, registry.Template{
		Name:            "golden",
		Scope:           registry.LocalScope,
		Source:          "claude",
		CreatedAt:       fixedNow.Add(-72 * time.Hour),
		PlaybookVersion: "not-a-real-content-hash",
		ToolsetKey:      "claude+ddev+go+java",
		Config:          vm.CreateConfig{Name: "claude", BaseName: vm.TemplateInstanceName("golden")},
	})

	prov := &providerfake.Provider{TemplateDiskBytesFunc: func(string) int64 { return 4 << 30 }}
	m := New(singleFleet(prov, registry.LocalScope)).(model)
	m = resized(m, 100, 30)
	next, _ := m.Update(vmsLoadedMsg{
		scope: registry.LocalScope,
		vms:   []vm.VM{{Name: "claude", Status: "Running", CPUs: 2, Memory: "4294967296", Disk: "53687091200"}},
	})
	m = next.(model)

	next, _ = m.Update(runeKey('n'))
	m = next.(model)
	// See TestTUIFormSourceSelectorGolden: keeps the disk-overflow warning
	// (compared against the REAL host's free space) from appearing.
	m.inputs[fDisk].SetValue("1GiB")
	// Pin the host-derived identity fields (fUser/fGitName/fGitEmail come from
	// hostUser()/hostGit(), which isolateHostState does not control) so the
	// golden is portable across machines and CI — same convention as the other
	// form golden tests.
	m.inputs[fUser].SetValue("ada")
	m.inputs[fGitName].SetValue("Ada Lovelace")
	m.inputs[fGitEmail].SetValue("ada@example.com")
	// The CPUs suggestion is half the host's core count (defaultCPUs → the
	// provider's HostResources, falling back to runtime.NumCPU) — not portable
	// across machines/CI. Pin it so the golden's value is fixed everywhere.
	m.inputs[fCPUs].SetValue("8")
	m.focusIdx = fSourceSelector
	m.cycleFormSource(1)

	next, _ = m.Update(runeKey('d'))
	m = next.(model)
	if m.confirm == nil {
		t.Fatal("precondition: 'd' on the highlighted template should raise a confirmation")
	}

	golden.RequireEqual(t, renderModel(m))
}

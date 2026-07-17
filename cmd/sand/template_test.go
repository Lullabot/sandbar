package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// TestDoTemplateSnapshotRejectsCollidingName proves a snapshot refuses to
// overwrite an existing template silently: the whole point of a template
// name is that `sand create --template NAME` always means the same golden
// image, so a second `snapshot` under a name already in use must be an error,
// not a quiet replace.
func TestDoTemplateSnapshotRejectsCollidingName(t *testing.T) {
	reg := registry.NewEmpty()
	if err := reg.AddTemplate(registry.Template{Name: "golden", Scope: registry.LocalScope}); err != nil {
		t.Fatalf("seed AddTemplate: %v", err)
	}
	if err := reg.AddScoped(vm.CreateConfig{Name: "dev", BaseName: "sandbar-base"}, registry.LocalScope); err != nil {
		t.Fatalf("seed AddScoped: %v", err)
	}

	prov := &providerfake.Provider{
		SnapshotTemplateFunc: func(ctx context.Context, source, templateInstance string, out io.Writer) (provision.SnapshotResult, error) {
			t.Fatal("SnapshotTemplate should not be called when the name already collides")
			return provision.SnapshotResult{}, nil
		},
	}

	err := doTemplateSnapshot(context.Background(), reg, prov, registry.LocalScope, "dev", "golden", io.Discard)
	if err == nil {
		t.Fatal("doTemplateSnapshot: got nil error for a colliding template name, want a rejection")
	}
	if !strings.Contains(err.Error(), "golden") {
		t.Fatalf("doTemplateSnapshot error = %q, want it to name the colliding template %q", err.Error(), "golden")
	}
}

// TestDoTemplateDeleteWarnsOnDependents proves a delete does not silently
// sever VMs that were cloned from the template being removed: it must print a
// warning naming them (warn-and-allow, no --force gate per the task spec) and
// still proceed with the delete.
func TestDoTemplateDeleteWarnsOnDependents(t *testing.T) {
	reg := registry.NewEmpty()
	if err := reg.AddTemplate(registry.Template{Name: "golden", Scope: registry.LocalScope, Config: vm.CreateConfig{BaseName: vm.TemplateInstanceName("golden")}}); err != nil {
		t.Fatalf("seed AddTemplate: %v", err)
	}
	depCfg := vm.CreateConfig{Name: "dev", BaseName: vm.TemplateInstanceName("golden")}
	if err := reg.AddScopedWithTemplate(depCfg, registry.LocalScope, "golden"); err != nil {
		t.Fatalf("seed AddScopedWithTemplate: %v", err)
	}

	deleted := false
	prov := &providerfake.Provider{
		DeleteTemplateFunc: func(ctx context.Context, templateInstance string, out io.Writer) error {
			deleted = true
			return nil
		},
	}

	var out bytes.Buffer
	if err := doTemplateDelete(context.Background(), reg, prov, registry.LocalScope, "golden", &out); err != nil {
		t.Fatalf("doTemplateDelete: %v", err)
	}
	if !deleted {
		t.Fatal("doTemplateDelete: DeleteTemplate was never called")
	}
	if !strings.Contains(out.String(), "dev") {
		t.Fatalf("doTemplateDelete output = %q, want a warning naming dependent VM %q", out.String(), "dev")
	}
	if _, ok := reg.TemplateInScope("golden", registry.LocalScope); ok {
		t.Fatal("doTemplateDelete: template still present in registry after delete")
	}
}

// TestDoTemplateDeleteRejectsUnknownName proves delete on a name with no
// registered template is a clean error rather than proceeding to call the
// provider with a made-up instance name.
func TestDoTemplateDeleteRejectsUnknownName(t *testing.T) {
	reg := registry.NewEmpty()
	prov := &providerfake.Provider{
		DeleteTemplateFunc: func(ctx context.Context, templateInstance string, out io.Writer) error {
			t.Fatal("DeleteTemplate should not be called for an unknown template name")
			return nil
		},
	}
	if err := doTemplateDelete(context.Background(), reg, prov, registry.LocalScope, "nope", io.Discard); err == nil {
		t.Fatal("doTemplateDelete: got nil error for an unknown template, want a rejection")
	}
}

// TestResolveTemplateCreateRejectsRebuild proves `sand create --template X
// --rebuild` is refused: --rebuild targets the shared base image, which a
// --template create never touches (it clones the template instance instead),
// so combining them is a contradictory request rather than a no-op.
func TestResolveTemplateCreateRejectsRebuild(t *testing.T) {
	reg := registry.NewEmpty()
	if err := reg.AddTemplate(registry.Template{Name: "golden", Scope: registry.LocalScope}); err != nil {
		t.Fatalf("seed AddTemplate: %v", err)
	}

	_, err := resolveTemplateCreate(reg, registry.LocalScope, "golden", true, false)
	if err == nil {
		t.Fatal("resolveTemplateCreate: got nil error for --template + --rebuild, want a rejection")
	}
	if !strings.Contains(err.Error(), "rebuild") {
		t.Fatalf("resolveTemplateCreate error = %q, want it to mention --rebuild", err.Error())
	}
}

// TestResolveTemplateCreateRejectsExplicitBaseName mirrors the --rebuild
// rejection for --base-name: a --template create's clone source IS the
// template instance, so an explicit --base-name would silently be ignored
// (or worse, ambiguous about which one wins) if allowed through.
func TestResolveTemplateCreateRejectsExplicitBaseName(t *testing.T) {
	reg := registry.NewEmpty()
	if err := reg.AddTemplate(registry.Template{Name: "golden", Scope: registry.LocalScope}); err != nil {
		t.Fatalf("seed AddTemplate: %v", err)
	}

	_, err := resolveTemplateCreate(reg, registry.LocalScope, "golden", false, true)
	if err == nil {
		t.Fatal("resolveTemplateCreate: got nil error for --template + explicit --base-name, want a rejection")
	}
	if !strings.Contains(err.Error(), "base-name") {
		t.Fatalf("resolveTemplateCreate error = %q, want it to mention --base-name", err.Error())
	}
}

// TestResolveTemplateCreateRejectsUnknownTemplate proves `sand create
// --template does-not-exist` exits with an error naming the missing template,
// per the task's acceptance criteria, rather than proceeding to clone a
// nonexistent instance.
func TestResolveTemplateCreateRejectsUnknownTemplate(t *testing.T) {
	reg := registry.NewEmpty()
	_, err := resolveTemplateCreate(reg, registry.LocalScope, "does-not-exist", false, false)
	if err == nil {
		t.Fatal("resolveTemplateCreate: got nil error for an unknown template, want a rejection")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("resolveTemplateCreate error = %q, want it to name the missing template", err.Error())
	}
}

// TestResolveTemplateCreateResolvesInstanceName proves a valid --template
// resolves to the template's reserved Lima instance name (vm.
// TemplateInstanceName), which is what CreateOptions.TemplateSource and
// cfg.BaseName must be set to for the clone-from-template path to work.
func TestResolveTemplateCreateResolvesInstanceName(t *testing.T) {
	reg := registry.NewEmpty()
	if err := reg.AddTemplate(registry.Template{Name: "golden", Scope: registry.LocalScope}); err != nil {
		t.Fatalf("seed AddTemplate: %v", err)
	}

	inst, err := resolveTemplateCreate(reg, registry.LocalScope, "golden", false, false)
	if err != nil {
		t.Fatalf("resolveTemplateCreate: %v", err)
	}
	if want := vm.TemplateInstanceName("golden"); inst != want {
		t.Fatalf("resolveTemplateCreate instance = %q, want %q", inst, want)
	}
}

// TestResolveTemplateCreateNoFlagIsNoOp proves that when --template is not
// passed at all, resolveTemplateCreate is inert: an empty instance name and
// no error, so an ordinary `sand create` is completely unaffected by this
// task's new flag.
func TestResolveTemplateCreateNoFlagIsNoOp(t *testing.T) {
	reg := registry.NewEmpty()
	inst, err := resolveTemplateCreate(reg, registry.LocalScope, "", true, true)
	if err != nil {
		t.Fatalf("resolveTemplateCreate with no --template: %v", err)
	}
	if inst != "" {
		t.Fatalf("resolveTemplateCreate with no --template: instance = %q, want empty", inst)
	}
}

// TestHeadlessCreateRecordsTemplateProvenance proves a `sand create
// --template` records TemplateSource provenance on the new VM's registry
// entry (via doHeadlessCreate's templateName parameter), so
// DependentsOfTemplate and a later --recreate can find it again.
func TestHeadlessCreateRecordsTemplateProvenance(t *testing.T) {
	reg := registry.NewEmpty()
	cfg := vm.CreateConfig{
		Name:     "dev",
		BaseName: vm.TemplateInstanceName("golden"),
		GitName:  "Ada Lovelace",
		GitEmail: "ada@example.com",
		CPUs:     2,
	}

	err := doHeadlessCreate(context.Background(), reg, &stubProvisioner{}, cfg, registry.LocalScope, false, false, "golden", io.Discard)
	if err != nil {
		t.Fatalf("doHeadlessCreate: %v", err)
	}

	got, ok := reg.TemplateSourceInScope("dev", registry.LocalScope)
	if !ok {
		t.Fatal("doHeadlessCreate: VM not recorded as managed")
	}
	if got != "golden" {
		t.Fatalf("TemplateSourceInScope = %q, want %q", got, "golden")
	}
}

// TestDoTemplateListEmptyRegistryIsGraceful proves `sand template list`
// against an empty registry prints something graceful instead of erroring or
// printing nothing at all silently.
func TestDoTemplateListEmptyRegistryIsGraceful(t *testing.T) {
	reg := registry.NewEmpty()
	prov := &providerfake.Provider{}

	var out bytes.Buffer
	if err := doTemplateList(reg, prov, registry.LocalScope, &out); err != nil {
		t.Fatalf("doTemplateList: %v", err)
	}
	if out.Len() == 0 {
		t.Fatal("doTemplateList: empty registry produced no output at all")
	}
}

// TestDoTemplateListPrintsRow proves a template shows up in the listing with
// its name, and does not error attempting to size/date-format it.
func TestDoTemplateListPrintsRow(t *testing.T) {
	reg := registry.NewEmpty()
	if err := reg.AddTemplate(registry.Template{
		Name:      "golden",
		Scope:     registry.LocalScope,
		Source:    "dev",
		CreatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed AddTemplate: %v", err)
	}
	prov := &providerfake.Provider{
		TemplateDiskBytesFunc: func(templateInstance string) int64 { return 1 << 30 },
	}

	var out bytes.Buffer
	if err := doTemplateList(reg, prov, registry.LocalScope, &out); err != nil {
		t.Fatalf("doTemplateList: %v", err)
	}
	if !strings.Contains(out.String(), "golden") {
		t.Fatalf("doTemplateList output = %q, want it to contain the template name %q", out.String(), "golden")
	}
}

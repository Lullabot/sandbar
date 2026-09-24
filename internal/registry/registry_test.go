package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/vm"
)

// TestRoundTrip exercises the custom persistence: add -> reload -> remove must
// survive across separate LoadFrom calls (i.e. it is actually written to disk),
// and the stored config (sizing/identity) must come back intact.
func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")

	r, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load (missing file): %v", err)
	}
	if r.IsManaged("claude") {
		t.Fatal("empty registry should not report claude as managed")
	}

	cfg := vm.CreateConfig{Name: "claude", BaseName: "sandbar-base", CPUs: 8, Memory: "32GiB", Hostname: "dev"}
	if err := r.Add(cfg); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Reload from disk: the entry, its base, and the stored config must persist.
	r2, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !r2.IsManaged("claude") {
		t.Fatal("claude should be managed after reload")
	}
	if got := r2.Base("claude"); got != "sandbar-base" {
		t.Fatalf("base = %q, want sandbar-base", got)
	}
	got, ok := r2.Config("claude")
	if !ok || got.CPUs != 8 || got.Memory != "32GiB" || got.Hostname != "dev" {
		t.Fatalf("stored config not restored: %+v (ok=%v)", got, ok)
	}

	if err := r2.Remove("claude"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	r3, _ := LoadFrom(path)
	if r3.IsManaged("claude") {
		t.Fatal("claude should be gone after remove")
	}
}

// TestTokenNeverPersisted: a clone token must never be written to the index.
func TestTokenNeverPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")
	r, _ := LoadFrom(path)
	if err := r.Add(vm.CreateConfig{Name: "claude", BaseName: "sandbar-base", CloneToken: "ghp_secret"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("index not written")
	}
	if containsStr(string(data), "ghp_secret") {
		t.Fatalf("clone token leaked into the index:\n%s", data)
	}
	cfg, _ := r.Config("claude")
	if cfg.CloneToken != "" {
		t.Fatalf("in-memory config retained the token: %q", cfg.CloneToken)
	}
}

// TestReconcilePrunesAbsent: entries for VMs no longer present must be dropped.
func TestReconcilePrunesAbsent(t *testing.T) {
	r := NewEmpty()
	_ = r.Add(vm.CreateConfig{Name: "claude", BaseName: "sandbar-base"})
	_ = r.Add(vm.CreateConfig{Name: "gone", BaseName: "sandbar-base"})

	dropped, err := r.Reconcile(map[string]bool{"claude": true})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(dropped) != 1 || dropped[0] != "gone" {
		t.Fatalf("reconcile should report the dropped name, got %v", dropped)
	}
	if r.IsManaged("gone") {
		t.Fatal("absent VM should be pruned")
	}
	if !r.IsManaged("claude") {
		t.Fatal("present VM should be kept")
	}

	if dropped, _ := r.Reconcile(map[string]bool{"claude": true}); len(dropped) != 0 {
		t.Fatal("second reconcile with no diff should report no change")
	}
}

func TestIsBase(t *testing.T) {
	r := NewEmpty()
	if r.IsBase("sandbar-base") {
		t.Error("empty registry: no base images recorded yet")
	}
	if r.IsBase("") {
		t.Error("empty name is never a base image")
	}

	_ = r.Add(vm.CreateConfig{Name: "claude", BaseName: "sandbar-base"})
	if !r.IsBase("sandbar-base") {
		t.Error("a recorded clone source should be a base image")
	}
	if r.IsBase("claude") {
		t.Error("the clone itself is not a base image")
	}
}

// TestMissingFileIsEmptyNotError: a first run with no index file is normal.
func TestMissingFileIsEmptyNotError(t *testing.T) {
	r, err := LoadFrom(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if r.IsManaged("anything") {
		t.Fatal("expected empty registry")
	}
}

// TestCorruptFileMovedAside: a corrupt index is reported AND preserved so a
// later save() cannot silently clobber it.
func TestCorruptFileMovedAside(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	r, err := LoadFrom(path)
	if err == nil {
		t.Fatal("corrupt index should return an error")
	}
	if r == nil || r.IsManaged("x") {
		t.Fatal("should still return a usable empty registry")
	}
	if _, statErr := os.Stat(path + ".corrupt"); statErr != nil {
		t.Fatalf("corrupt file should be preserved at %s.corrupt: %v", path, statErr)
	}
}

// TestMigrateLegacyIndex: a pre-rename claude-code-ansible index is migrated to
// the new sandbar location on Load() — the managed VM survives, and the old
// index file is removed once the copy is verified.
func TestMigrateLegacyIndex(t *testing.T) {
	base := t.TempDir()
	// Load() resolves the data dir from XDG_DATA_HOME via defaultPath().
	t.Setenv("XDG_DATA_HOME", base)

	// Seed a legacy index using the real writer so the on-disk JSON shape
	// matches production exactly.
	oldPath := filepath.Join(base, "claude-code-ansible", "managed-vms.json")
	seed, err := LoadFrom(oldPath)
	if err != nil {
		t.Fatalf("seed load: %v", err)
	}
	if err := seed.Add(vm.CreateConfig{Name: "claude", BaseName: "sandbar-base", CPUs: 8, Memory: "32GiB", Hostname: "dev"}); err != nil {
		t.Fatalf("seed add: %v", err)
	}

	// Load() migrates the legacy index before the first read.
	r, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !r.IsManaged("claude") {
		t.Fatal("migrated registry should report claude as managed")
	}

	// (a) the new index exists and parses with the VM present.
	newPath := filepath.Join(base, "sandbar", "managed-vms.json")
	if _, statErr := os.Stat(newPath); statErr != nil {
		t.Fatalf("new index should exist at %s: %v", newPath, statErr)
	}
	moved, err := LoadFrom(newPath)
	if err != nil {
		t.Fatalf("load migrated index: %v", err)
	}
	if !moved.IsManaged("claude") || moved.Base("claude") != "sandbar-base" {
		t.Fatalf("migrated index missing the VM: managed=%v base=%q", moved.IsManaged("claude"), moved.Base("claude"))
	}

	// (b) the old index file no longer exists.
	if _, statErr := os.Stat(oldPath); !os.IsNotExist(statErr) {
		t.Fatalf("old index should be removed, stat err = %v", statErr)
	}
}

// TestLoad_UnversionedFileMigrates: a legacy index with no "version" key must
// load with zero data loss, be rewritten to the current schema (version 4,
// the (scope,name)-keyed array with the (empty) templates array) with its
// entry tagged as the local Lima provider ON LOAD (not merely on the next
// save), and a subsequent save must keep carrying the version and the
// preserved entry.
func TestLoad_UnversionedFileMigrates(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	sandbarDir := filepath.Join(dataHome, "sandbar")
	if err := os.MkdirAll(sandbarDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(sandbarDir, "managed-vms.json")
	legacy := `{"vms":{"old-vm":{"base":"sandbar-base","config":{"Name":"old-vm","BaseName":"sandbar-base","CPUs":4}}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("seed legacy file: %v", err)
	}

	r, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load unversioned file: %v", err)
	}
	if !r.IsManaged("old-vm") {
		t.Fatal("old-vm should be managed after loading unversioned file")
	}
	cfg, ok := r.Config("old-vm")
	if !ok || cfg.CPUs != 4 {
		t.Fatalf("old-vm config not preserved: %+v (ok=%v)", cfg, ok)
	}

	// LoadFrom itself must have already rewritten the file: version 4, and
	// old-vm tagged as the local Lima provider.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read index after load: %v", err)
	}
	if !containsStr(string(raw), `"version": 4`) {
		t.Fatalf("expected version 4 stamped in file immediately after LoadFrom:\n%s", raw)
	}
	if !containsStr(string(raw), `"old-vm"`) || !containsStr(string(raw), `"CPUs": 4`) {
		t.Fatalf("old-vm entry with CPUs 4 not preserved after migration:\n%s", raw)
	}
	if !containsStr(string(raw), `"provider": "lima"`) {
		t.Fatalf("expected old-vm tagged with provider \"lima\" after migration:\n%s", raw)
	}

	// A further save (Add) must keep carrying the version and both entries.
	if err := r.Add(vm.CreateConfig{Name: "new-vm", BaseName: "claude-base"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !containsStr(string(raw), `"version": 4`) {
		t.Fatalf("expected version 4 stamped in file:\n%s", raw)
	}
	if !containsStr(string(raw), `"old-vm"`) || !containsStr(string(raw), `"CPUs": 4`) {
		t.Fatalf("old-vm entry with CPUs 4 not preserved after save:\n%s", raw)
	}
}

// TestLoad_MigratesLegacyBaseName: a pre-v2 index recorded under the old
// claude-base name must load with every entry rewritten to the current default
// base (sandbar-base), in both the Base field and the embedded config, and the
// file must be stamped the current schema version so the rewrite runs at most
// once.
func TestLoad_MigratesLegacyBaseName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")
	// A version-1 file (the last schema an old sand wrote) with two clones and a
	// custom-base VM that must be left alone.
	legacy := `{"version":1,"vms":{` +
		`"claude":{"base":"claude-base","config":{"Name":"claude","BaseName":"claude-base","CPUs":4}},` +
		`"web":{"base":"claude-base","config":{"Name":"web","BaseName":"claude-base"}},` +
		`"custom":{"base":"other-base","config":{"Name":"custom","BaseName":"other-base"}}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("seed legacy file: %v", err)
	}

	want := vm.DefaultCreateConfig().BaseName
	r, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, name := range []string{"claude", "web"} {
		if got := r.Base(name); got != want {
			t.Errorf("Base(%q) = %q, want %q", name, got, want)
		}
		cfg, _ := r.Config(name)
		if cfg.BaseName != want {
			t.Errorf("Config(%q).BaseName = %q, want %q", name, cfg.BaseName, want)
		}
	}
	// A VM cloned from a genuinely different base is not touched.
	if got := r.Base("custom"); got != "other-base" {
		t.Errorf("custom base rewritten: got %q, want other-base", got)
	}

	// The migration persisted: the file now carries the rewritten name and the
	// bumped version, so a reload does no further work.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if containsStr(string(raw), "claude-base") {
		t.Errorf("legacy base name still on disk after migration:\n%s", raw)
	}
	if !containsStr(string(raw), `"version": 4`) {
		t.Errorf("expected version 4 stamped after migration:\n%s", raw)
	}
}

// TestLoad_V1MigratesTwoEntriesToV2WithProviderTag is the load-bearing
// migration proof for LoadFrom: a pre-migration v1 file with TWO existing
// entries, loaded through the current code, must come back on disk
// rewritten with BOTH entries tagged as the
// local Lima provider and the schema version bumped to the current version
// (4, which folds the v1->v2 provider/base-rename step, the v2->v3
// (scope,name) re-keying, and the v3->v4 templates-array addition into one
// load) — no data loss, and the rewrite reuses save()'s existing atomic
// temp-file+rename path.
func TestLoad_V1MigratesTwoEntriesToV2WithProviderTag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")
	v1 := `{"version":1,"vms":{
		"claude":{"base":"claude-base","config":{"Name":"claude","BaseName":"claude-base","CPUs":8,"Memory":"32GiB"}},
		"web":{"base":"claude-base","config":{"Name":"web","BaseName":"claude-base","CPUs":2,"Memory":"8GiB"}}
	}}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatalf("seed v1 file: %v", err)
	}

	r, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load v1 file: %v", err)
	}

	// Both entries survived the migration with their configs intact.
	for _, tc := range []struct {
		name string
		cpus int
	}{{"claude", 8}, {"web", 2}} {
		if !r.IsManaged(tc.name) {
			t.Fatalf("%q should be managed after migration", tc.name)
		}
		cfg, ok := r.Config(tc.name)
		if !ok || cfg.CPUs != tc.cpus {
			t.Fatalf("%q config not preserved: %+v (ok=%v, want CPUs=%d)", tc.name, cfg, ok, tc.cpus)
		}
		// v2 migration also renames the legacy base (claude-base) to the current
		// default (sandbar-base), so BaseInScope reports the renamed base here.
		base, managed := r.BaseInScope(tc.name, LocalScope)
		if !managed || base != vm.DefaultCreateConfig().BaseName {
			t.Fatalf("%q should be managed under LocalScope with the renamed base after migration: managed=%v base=%q", tc.name, managed, base)
		}
	}

	// The on-disk file itself must be rewritten: version 4, both entries
	// tagged "lima", nothing truncated or lost.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migrated index: %v", err)
	}
	got := string(raw)
	if !containsStr(got, `"version": 4`) {
		t.Fatalf("expected version bumped to 4 on disk after migration:\n%s", got)
	}
	if n := strings.Count(got, `"provider": "lima"`); n != 2 {
		t.Fatalf(`expected both entries tagged "provider": "lima" (found %d), got:\n%s`, n, got)
	}
	if !containsStr(got, `"claude"`) || !containsStr(got, `"web"`) {
		t.Fatalf("expected both claude and web entries preserved on disk:\n%s", got)
	}
}

// TestLoad_FutureVersionRefused: a file whose version this binary does not
// understand must be refused, not misparsed.
func TestLoad_FutureVersionRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")
	future := `{"version":99,"vms":{"x":{"base":"sandbar-base","config":{"Name":"x","BaseName":"sandbar-base"}}}}`
	if err := os.WriteFile(path, []byte(future), 0o600); err != nil {
		t.Fatalf("seed future file: %v", err)
	}

	r, err := LoadFrom(path)
	if err == nil {
		t.Fatal("expected an error loading a future-versioned file")
	}
	if !containsStr(err.Error(), "upgrade sand") {
		t.Fatalf("error should tell the user to upgrade sand, got: %v", err)
	}
	if r == nil {
		t.Fatal("returned registry should be non-nil")
	}
	if r.IsManaged("x") {
		t.Fatal("nothing should be parsed out of a future-versioned file")
	}
}

// TestSave_WritesVersion: any save must stamp the current version, and the
// clone token must never appear in the persisted bytes.
func TestSave_WritesVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")
	r, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := r.Add(vm.CreateConfig{Name: "claude", BaseName: "sandbar-base", CloneToken: "SENTINEL_TOKEN_DO_NOT_PERSIST"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !containsStr(string(raw), `"version": 4`) {
		t.Fatalf("expected version 4 in saved file:\n%s", raw)
	}
	if containsStr(string(raw), "SENTINEL_TOKEN_DO_NOT_PERSIST") {
		t.Fatalf("clone token leaked into the index:\n%s", raw)
	}
}

// TestScopedSameNameCoexistsAcrossScopes is the load-bearing proof for this
// task: a VM named "web" under LocalScope and a VM ALSO named "web" under a
// remote scope must coexist as independent entries — AddScoped under one
// scope must never overwrite the other — survive a reload from disk (proving
// the on-disk shape actually holds both), and a delete under one scope
// (RemoveScoped) must leave the other scope's same-named entry untouched.
func TestScopedSameNameCoexistsAcrossScopes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")
	r, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	remote := Scope{Provider: "lima-ssh", RemoteTarget: "user@host:22"}

	if err := r.AddScoped(vm.CreateConfig{Name: "web", BaseName: "sandbar-base", CPUs: 2}, LocalScope); err != nil {
		t.Fatalf("add local: %v", err)
	}
	if err := r.AddScoped(vm.CreateConfig{Name: "web", BaseName: "sandbar-base", CPUs: 8}, remote); err != nil {
		t.Fatalf("add remote: %v", err)
	}

	localCfg, ok := r.ConfigInScope("web", LocalScope)
	if !ok || localCfg.CPUs != 2 {
		t.Fatalf("local web config = %+v (ok=%v), want CPUs=2", localCfg, ok)
	}
	remoteCfg, ok := r.ConfigInScope("web", remote)
	if !ok || remoteCfg.CPUs != 8 {
		t.Fatalf("remote web config = %+v (ok=%v), want CPUs=8", remoteCfg, ok)
	}

	// Reload from disk: the on-disk shape must actually hold BOTH same-named
	// entries (a flat {name: entry} object could not).
	r2, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !r2.IsManagedInScope("web", LocalScope) {
		t.Fatal("local web should survive a reload")
	}
	if !r2.IsManagedInScope("web", remote) {
		t.Fatal("remote web should survive a reload")
	}
	if got, ok := r2.ConfigInScope("web", LocalScope); !ok || got.CPUs != 2 {
		t.Fatalf("reloaded local web config = %+v (ok=%v), want CPUs=2", got, ok)
	}
	if got, ok := r2.ConfigInScope("web", remote); !ok || got.CPUs != 8 {
		t.Fatalf("reloaded remote web config = %+v (ok=%v), want CPUs=8", got, ok)
	}

	// Delete under LocalScope only: the remote same-named entry must survive.
	if err := r2.RemoveScoped(LocalScope, "web"); err != nil {
		t.Fatalf("removescoped: %v", err)
	}
	if r2.IsManagedInScope("web", LocalScope) {
		t.Fatal("local web should be gone after RemoveScoped(LocalScope, ...)")
	}
	if !r2.IsManagedInScope("web", remote) {
		t.Fatal("remote web must survive a LocalScope-only RemoveScoped")
	}
}

// TestLoad_V2FixtureMigratesToV3Array captures a v2 (object-keyed) fixture —
// an on-disk shape an old sand once wrote — and proves it loads back intact
// as LocalScope with its config preserved, then gets rewritten on disk as a
// JSON ARRAY (not the old flat object, which cannot hold two same-named
// entries) stamped with the current version (4 — the v1/v2 legacy migration
// always rewrites straight to currentVersion, not to the intermediate v3
// shape).
func TestLoad_V2FixtureMigratesToV3Array(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")
	v2 := `{"version":2,"vms":{
		"claude":{"base":"sandbar-base","config":{"Name":"claude","BaseName":"sandbar-base","CPUs":8,"Memory":"32GiB"},"provider":"lima"},
		"web":{"base":"sandbar-base","config":{"Name":"web","BaseName":"sandbar-base","CPUs":2,"Memory":"8GiB"},"provider":"lima"}
	}}`
	if err := os.WriteFile(path, []byte(v2), 0o600); err != nil {
		t.Fatalf("seed v2 file: %v", err)
	}

	r, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load v2 file: %v", err)
	}
	for _, tc := range []struct {
		name string
		cpus int
	}{{"claude", 8}, {"web", 2}} {
		if !r.IsManagedInScope(tc.name, LocalScope) {
			t.Fatalf("%q should be managed under LocalScope after v2->v3 migration", tc.name)
		}
		cfg, ok := r.ConfigInScope(tc.name, LocalScope)
		if !ok || cfg.CPUs != tc.cpus {
			t.Fatalf("%q config not preserved: %+v (ok=%v, want CPUs=%d)", tc.name, cfg, ok, tc.cpus)
		}
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migrated file: %v", err)
	}
	got := string(raw)
	if !containsStr(got, `"version": 4`) {
		t.Fatalf("expected version 4 stamped after v2->v4 migration:\n%s", got)
	}
	if !containsStr(got, `"vms": [`) {
		t.Fatalf("expected array shape for vms (not the old flat object):\n%s", got)
	}
}

func containsStr(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestScopedAccessorsDoNotCrossProviders pins the scope-isolation the UI relies
// on: a LOCAL entry must never be reported managed (nor have its recorded config
// returned) under a REMOTE scope. Without this, stop-all could stop an
// out-of-scope remote VM and secrets could be applied as the wrong guest user
// when a remote VM shares a name with a leftover local entry.
func TestScopedAccessorsDoNotCrossProviders(t *testing.T) {
	r := NewEmpty()
	if err := r.Add(vm.CreateConfig{Name: "web", BaseName: "b", User: "localuser"}); err != nil {
		t.Fatal(err)
	}
	remote := Scope{Provider: "lima-remote", RemoteTarget: "u@h:22"}

	if !r.IsManagedInScope("web", LocalScope) {
		t.Fatal("local entry should be managed under LocalScope")
	}
	if r.IsManagedInScope("web", remote) {
		t.Fatal("a LOCAL entry must NOT be managed under a remote scope")
	}
	if _, ok := r.ConfigInScope("web", remote); ok {
		t.Fatal("ConfigInScope must not return a local entry's config under a remote scope")
	}
	if cfg, ok := r.ConfigInScope("web", LocalScope); !ok || cfg.User != "localuser" {
		t.Fatalf("ConfigInScope under LocalScope = (%+v, %v), want the local entry", cfg, ok)
	}
}

// TestLoad_V3FixtureMigratesToV4 is the load-bearing migration proof this
// task adds: a REAL v3 fixture — the on-disk shape every sand wrote before
// this task, VMs present, no templates array at all — must load with every
// existing VM entry preserved unchanged, yield an empty template set (not an
// error, not dropped data), and get rewritten on disk as version 4. A reload
// of that rewritten file must still report the same VMs. This is the
// additive-migration guarantee the task's acceptance criteria call out
// explicitly.
func TestLoad_V3FixtureMigratesToV4(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")
	// A genuine v3 fixture: the array-of-entries shape, no "templates" key.
	v3 := `{"version":3,"vms":[
		{"name":"claude","provider":"lima","base":"sandbar-base","config":{"Name":"claude","BaseName":"sandbar-base","CPUs":8,"Memory":"32GiB"}},
		{"name":"web","provider":"lima","base":"sandbar-base","config":{"Name":"web","BaseName":"sandbar-base","CPUs":2,"Memory":"8GiB"}}
	]}`
	if err := os.WriteFile(path, []byte(v3), 0o600); err != nil {
		t.Fatalf("seed v3 file: %v", err)
	}

	r, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load v3 file: %v", err)
	}

	// Every existing VM entry survives, unchanged.
	for _, tc := range []struct {
		name string
		cpus int
	}{{"claude", 8}, {"web", 2}} {
		if !r.IsManagedInScope(tc.name, LocalScope) {
			t.Fatalf("%q should remain managed after v3->v4 migration", tc.name)
		}
		cfg, ok := r.ConfigInScope(tc.name, LocalScope)
		if !ok || cfg.CPUs != tc.cpus {
			t.Fatalf("%q config not preserved: %+v (ok=%v, want CPUs=%d)", tc.name, cfg, ok, tc.cpus)
		}
	}

	// No templates existed in the v3 file: the template set must be empty,
	// not an error and not repurposed data.
	if got := r.TemplatesInScope(LocalScope); len(got) != 0 {
		t.Fatalf("expected empty template set after v3->v4 migration, got %+v", got)
	}

	// LoadFrom must have already rewritten the file to v4 (best-effort
	// persist), not merely produced a migrated in-memory registry.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migrated file: %v", err)
	}
	if !containsStr(string(raw), `"version": 4`) {
		t.Fatalf("expected version 4 stamped on disk immediately after LoadFrom:\n%s", raw)
	}

	// Reload the rewritten file: both VMs are still intact, version is 4.
	r2, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload migrated file: %v", err)
	}
	if !r2.IsManagedInScope("claude", LocalScope) || !r2.IsManagedInScope("web", LocalScope) {
		t.Fatal("VMs should still be managed after reloading the migrated v4 file")
	}
	raw2, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file after reload: %v", err)
	}
	if !containsStr(string(raw2), `"version": 4`) {
		t.Fatalf("expected version to remain 4 after reload:\n%s", raw2)
	}
}

// TestTemplateRoundTrip proves the custom template persistence: AddTemplate
// writes a template record that survives a fresh LoadFrom (a separate
// process reading the same file), with every field intact, and it shows up
// in TemplatesInScope for its owning scope.
func TestTemplateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")
	r, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	created := time.Date(2026, 7, 17, 12, 30, 0, 0, time.UTC)
	instanceName := vm.TemplateInstanceName("golden")
	tmpl := Template{
		Name:            "golden",
		Scope:           LocalScope,
		Source:          "claude",
		CreatedAt:       created,
		PlaybookVersion: "v2:deadbeef:claude+ddev+go+java",
		ToolsetKey:      "claude+ddev+go+java",
		Config: vm.CreateConfig{
			Name:     instanceName,
			BaseName: instanceName,
			CPUs:     4,
			Memory:   "16GiB",
			Hostname: "golden",
		},
	}
	if err := r.AddTemplate(tmpl); err != nil {
		t.Fatalf("add template: %v", err)
	}

	// Reload from a fresh Registry value backed by the same path — proves the
	// record was actually persisted, not just held in memory.
	r2, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := r2.TemplateInScope("golden", LocalScope)
	if !ok {
		t.Fatal("expected the template to round-trip through save/load")
	}
	if got.Name != tmpl.Name || got.Scope != tmpl.Scope || got.Source != tmpl.Source ||
		got.PlaybookVersion != tmpl.PlaybookVersion || got.ToolsetKey != tmpl.ToolsetKey {
		t.Fatalf("template fields not preserved:\ngot:  %+v\nwant: %+v", got, tmpl)
	}
	if !got.CreatedAt.Equal(tmpl.CreatedAt) {
		t.Fatalf("CreatedAt = %v, want %v", got.CreatedAt, tmpl.CreatedAt)
	}
	if got.Config != tmpl.Config {
		t.Fatalf("Config not preserved:\ngot:  %+v\nwant: %+v", got.Config, tmpl.Config)
	}

	all := r2.TemplatesInScope(LocalScope)
	if len(all) != 1 || all[0].Name != "golden" {
		t.Fatalf("TemplatesInScope(LocalScope) = %+v, want exactly one entry named golden", all)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !containsStr(string(raw), `"templates"`) {
		t.Fatalf("expected a templates array in the persisted file:\n%s", raw)
	}
}

// TestDependentsOfTemplate proves DependentsOfTemplate returns exactly the
// managed VM names, in a given scope, whose TemplateSource matches the named
// template — and does not leak a same-named dependent recorded under a
// different scope. It constructs entries directly (white-box, same package)
// because wiring TemplateSource into AddScoped's public signature belongs to
// a later task that actually clones from a template.
func TestDependentsOfTemplate(t *testing.T) {
	r := NewEmpty()
	r.vms[scopedKey{scope: LocalScope, name: "a"}] = entry{Base: "sandbar-tmpl-golden", TemplateSource: "golden"}
	r.vms[scopedKey{scope: LocalScope, name: "b"}] = entry{Base: "sandbar-tmpl-golden", TemplateSource: "golden"}
	r.vms[scopedKey{scope: LocalScope, name: "c"}] = entry{Base: "sandbar-tmpl-other", TemplateSource: "other"}
	r.vms[scopedKey{scope: LocalScope, name: "d"}] = entry{Base: "sandbar-base"}

	remote := Scope{Provider: "lima-ssh", RemoteTarget: "user@host:22"}
	r.vms[scopedKey{scope: remote, name: "a"}] = entry{Base: "sandbar-tmpl-golden", TemplateSource: "golden"}

	got := r.DependentsOfTemplate(LocalScope, "golden")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("DependentsOfTemplate(LocalScope, %q) = %v, want [a b]", "golden", got)
	}

	if got := r.DependentsOfTemplate(LocalScope, "other"); len(got) != 1 || got[0] != "c" {
		t.Fatalf("DependentsOfTemplate(LocalScope, %q) = %v, want [c]", "other", got)
	}

	if got := r.DependentsOfTemplate(LocalScope, "nonexistent"); len(got) != 0 {
		t.Fatalf("DependentsOfTemplate for an unused template = %v, want none", got)
	}

	// The remote scope's same-named dependent must not leak into the local
	// scope's result (already covered above: len==2, not 3), and must be
	// visible under its OWN scope.
	if got := r.DependentsOfTemplate(remote, "golden"); len(got) != 1 || got[0] != "a" {
		t.Fatalf("DependentsOfTemplate(remote, golden) = %v, want [a]", got)
	}
}

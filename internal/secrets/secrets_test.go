package secrets

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/registry"
)

// TestRender_EscapesAdversarialValues is the security-critical test: every value
// is wrapped in POSIX single quotes so the guest sources it as literal text.
// Inside single quotes nothing is expanded, so the only character needing an
// escape is the single quote itself, which Render rewrites to a close-escape-
// reopen sequence. Metacharacters -- command substitutions, backticks,
// dollar-variables, backslashes, newlines -- must all survive verbatim and never
// execute.
func TestRender_EscapesAdversarialValues(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]string
		want string
	}{
		{
			name: "adversarial command substitution and backticks (acceptance-criterion case)",
			in:   map[string]string{"Q": "it's $(id) `whoami`"},
			// Verbatim from the acceptance criterion.
			want: `export Q='it'\''s $(id) ` + "`whoami`" + `'` + "\n",
		},
		{
			name: "bare single quote",
			in:   map[string]string{"A": "a'b"},
			want: "export A='a'\\''b'\n",
		},
		{
			name: "leading and trailing single quotes",
			in:   map[string]string{"A": "'x'"},
			want: "export A=''\\''x'\\'''\n",
		},
		{
			name: "embedded newline is literal",
			in:   map[string]string{"NL": "line1\nline2"},
			want: "export NL='line1\nline2'\n",
		},
		{
			name: "backslash is literal inside single quotes",
			in:   map[string]string{"BS": `a\b`},
			want: "export BS='a\\b'\n",
		},
		{
			name: "space",
			in:   map[string]string{"SP": "a b c"},
			want: "export SP='a b c'\n",
		},
		{
			name: "dollar variable is not expanded",
			in:   map[string]string{"D": "$HOME:$PATH"},
			want: "export D='$HOME:$PATH'\n",
		},
		{
			name: "empty value",
			in:   map[string]string{"E": ""},
			want: "export E=''\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Render(tc.in)
			if got != tc.want {
				t.Fatalf("Render(%#v):\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRender_StableOrder: equal input renders byte-identical output, and keys are
// emitted in ascending sort order regardless of map iteration order.
func TestRender_StableOrder(t *testing.T) {
	in := map[string]string{"B": "2", "A": "1", "C": "3", "AA": "4"}
	first := Render(in)
	for i := 0; i < 50; i++ {
		if got := Render(in); got != first {
			t.Fatalf("Render is not byte-stable across calls:\n%q\n%q", first, got)
		}
	}
	want := "export A='1'\nexport AA='4'\nexport B='2'\nexport C='3'\n"
	if first != want {
		t.Fatalf("Render not sorted ascending:\n got %q\nwant %q", first, want)
	}
}

// TestRender_SkipsInvalidKeys is defense in depth: keys are emitted UNQUOTED, so
// a key with shell metacharacters would be an injection. Set already rejects such
// keys, but Render must not emit them even if one slips through.
func TestRender_SkipsInvalidKeys(t *testing.T) {
	got := Render(map[string]string{"GOOD": "1", "bad key": "2", "2BAD": "3", "OK_2": "4"})
	want := "export GOOD='1'\nexport OK_2='4'\n"
	if got != want {
		t.Fatalf("Render must skip invalid keys:\n got %q\nwant %q", got, want)
	}
}

// TestValidKey is the accept/reject table from the acceptance criteria plus a few
// extra hardening cases (a trailing newline must not sneak past the anchors).
func TestValidKey(t *testing.T) {
	accept := []string{
		"A", "Z", "a", "z", "_", "_FOO", "FOO", "foo_bar", "A1", "a1b2",
		"_1", "camelCase", "SCREAMING_SNAKE", "__dunder__", "x9",
	}
	reject := []string{
		"", "2FOO", "A-B", "A B", "A=B", "A$B", "1", "9x", "-", ".", "A.B",
		"A/B", "FOO ", " FOO", "A\n", "\nA", "A\tB", "föö", "A'B",
	}
	for _, k := range accept {
		if !ValidKey(k) {
			t.Errorf("ValidKey(%q) = false, want true", k)
		}
	}
	for _, k := range reject {
		if ValidKey(k) {
			t.Errorf("ValidKey(%q) = true, want false", k)
		}
	}
}

// TestSet_FilePermissions: after Set the on-disk file is mode 0600 and its parent
// directory is 0700 — there must be no world-readable file holding a secret, and
// no leftover temp file.
func TestSet_FilePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sandbar")
	path := filepath.Join(dir, "secrets.json")

	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := s.Set("claude", registry.LocalScope, map[string]string{"TOK": "s3cr3t"}); err != nil {
		t.Fatalf("set: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("secrets.json mode = %04o, want 0600", got)
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("parent dir mode = %04o, want 0700", got)
	}

	// The atomic write must leave no temp files behind -- only the store itself
	// and the cross-process lock file this task introduces
	// ("<datadir>/secrets.json.lock") are expected.
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range ents {
		if e.Name() != "secrets.json" && e.Name() != "secrets.json.lock" {
			t.Errorf("unexpected leftover file in secrets dir: %q", e.Name())
		}
	}
}

// TestRoundTrip: Set -> reload -> Get must survive a separate LoadFrom (it is
// actually persisted), Remove must delete, and Get must return a defensive copy.
func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sandbar", "secrets.json")

	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := s.Set("claude", registry.LocalScope, map[string]string{"A": "1", "B": "two"}); err != nil {
		t.Fatalf("set: %v", err)
	}

	s2, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := s2.Get("claude", registry.LocalScope)
	want := map[string]string{"A": "1", "B": "two"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Get after reload = %v, want %v", got, want)
	}

	// Get returns a copy: mutating it must not affect the store.
	got["A"] = "mutated"
	got["NEW"] = "x"
	if again := s2.Get("claude", registry.LocalScope); again["A"] != "1" || len(again) != 2 {
		t.Fatalf("Get must return a defensive copy; store was mutated: %v", again)
	}

	if err := s2.Remove("claude", registry.LocalScope); err != nil {
		t.Fatalf("remove: %v", err)
	}
	s3, _ := LoadFrom(path)
	if len(s3.Get("claude", registry.LocalScope)) != 0 {
		t.Fatalf("claude secrets should be gone after remove, got %v", s3.Get("claude", registry.LocalScope))
	}
}

// TestSet_RejectsInvalidKey: an invalid key must be refused before it can reach
// the guest as an unquoted (injectable) shell token, and nothing is persisted.
func TestSet_RejectsInvalidKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sandbar", "secrets.json")
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := s.Set("claude", registry.LocalScope, map[string]string{"OK": "1", "BAD KEY": "x"}); err == nil {
		t.Fatal("Set must reject a map containing an invalid key")
	}
	// A rejected Set must not partially persist.
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("rejected Set must not write the store (stat err = %v)", statErr)
	}
	if len(s.Get("claude", registry.LocalScope)) != 0 {
		t.Fatalf("rejected Set must not mutate the in-memory store, got %v", s.Get("claude", registry.LocalScope))
	}
}

// TestSet_EmptyRemovesEntry: setting an empty map for a VM drops its entry rather
// than persisting an empty object.
func TestSet_EmptyRemovesEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sandbar", "secrets.json")
	s, _ := LoadFrom(path)
	if err := s.Set("claude", registry.LocalScope, map[string]string{"A": "1"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.Set("claude", registry.LocalScope, map[string]string{}); err != nil {
		t.Fatalf("set empty: %v", err)
	}
	if len(s.Get("claude", registry.LocalScope)) != 0 {
		t.Fatalf("empty Set should clear the entry, got %v", s.Get("claude", registry.LocalScope))
	}
}

// TestSet_TouchesOnlySecretsJSON: this task must not create or write
// managed-vms.json; secrets live in a distinct file.
func TestSet_TouchesOnlySecretsJSON(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sandbar")
	path := filepath.Join(dir, "secrets.json")
	s, _ := LoadFrom(path)
	if err := s.Set("claude", registry.LocalScope, map[string]string{"A": "1"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("secrets.json should exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "managed-vms.json")); !os.IsNotExist(err) {
		t.Fatalf("Set must not create managed-vms.json (stat err = %v)", err)
	}
}

// TestDefaultPath mirrors the registry's XDG derivation but for secrets.json.
func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/x/y")
	if got, want := defaultPath(), filepath.Join("/x/y", "sandbar", "secrets.json"); got != want {
		t.Fatalf("defaultPath = %q, want %q", got, want)
	}
}

// TestLoad_MissingFileIsEmptyNotError: a first run with no file is normal.
func TestLoad_MissingFileIsEmptyNotError(t *testing.T) {
	s, err := LoadFrom(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(s.Get("anything", registry.LocalScope)) != 0 {
		t.Fatal("expected empty store")
	}
}

// TestLoad_FutureVersionRefused: a file whose version this binary does not
// understand must be refused with an "upgrade sand" error and an empty store,
// mirroring the registry.
func TestLoad_FutureVersionRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	future := `{"version":99,"vms":{"x":{"K":"V"}}}`
	if err := os.WriteFile(path, []byte(future), 0o600); err != nil {
		t.Fatalf("seed future file: %v", err)
	}
	s, err := LoadFrom(path)
	if err == nil {
		t.Fatal("expected an error loading a future-versioned file")
	}
	if !strings.Contains(err.Error(), "upgrade sand") {
		t.Fatalf("error should tell the user to upgrade sand, got: %v", err)
	}
	if s == nil {
		t.Fatal("returned store must be non-nil")
	}
	if len(s.Get("x", registry.LocalScope)) != 0 {
		t.Fatal("nothing should be parsed out of a future-versioned file")
	}
}

// TestLoad_CorruptFileWarnsButReturnsUsableStore: a corrupt file yields a warning
// error AND a usable non-nil empty store, and the corrupt file is preserved so a
// later save cannot silently clobber it.
func TestLoad_CorruptFileWarnsButReturnsUsableStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	s, err := LoadFrom(path)
	if err == nil {
		t.Fatal("corrupt file should return an error")
	}
	if s == nil || len(s.Get("anything", registry.LocalScope)) != 0 {
		t.Fatal("should still return a usable empty store")
	}
	// The store must remain usable.
	if err := s.Set("claude", registry.LocalScope, map[string]string{"A": "1"}); err != nil {
		t.Fatalf("store should be usable after a corrupt load: %v", err)
	}
	// The corrupt file must be preserved for recovery.
	if _, statErr := os.Stat(path + ".corrupt"); statErr != nil {
		t.Fatalf("corrupt file should be preserved at %s.corrupt: %v", path, statErr)
	}
}

// TestValidScope is the accept/reject table from the acceptance criteria:
// "" (global) and a normal dir path are accepted; anything that could escape
// $HOME or inject into a shell/gitconfig pattern is rejected.
func TestValidScope(t *testing.T) {
	accept := []string{"", "github.com/acme", "a", "a-b_c.d", "a/b/c"}
	reject := []string{
		"/etc", "../x", "a/../b", "a//b", "a/", "/a", "$(id)", "a b", "a;rm",
		".", "a/.", "a/..", "..", "a/./b",
	}
	for _, sc := range accept {
		if !ValidScope(sc) {
			t.Errorf("ValidScope(%q) = false, want true", sc)
		}
	}
	for _, sc := range reject {
		if ValidScope(sc) {
			t.Errorf("ValidScope(%q) = true, want false", sc)
		}
	}
}

// TestLoadFrom_V1Migration: an unversioned/v1 flat file loads such that the
// VM's pairs live under the global scope "" AND registry.LocalScope (no
// connection scope existed when a v1 file could have been written), and the
// next save stamps version:3 on disk.
func TestLoadFrom_V1Migration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	v1 := `{"version":1,"vms":{"x":{"A":"1"}}}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatalf("seed v1 file: %v", err)
	}
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load v1: %v", err)
	}
	if got := s.Get("x", registry.LocalScope); !reflect.DeepEqual(got, map[string]string{"A": "1"}) {
		t.Fatalf("migrated global scope = %v, want {A:1}", got)
	}
	all := s.GetAll("x", registry.LocalScope)
	if got := all[""]; !reflect.DeepEqual(got, map[string]string{"A": "1"}) {
		t.Fatalf("GetAll()[\"\"] = %v, want {A:1}", got)
	}

	// Force a save and confirm the on-disk shape is stamped version 3.
	if err := s.Set("x", registry.LocalScope, s.Get("x", registry.LocalScope)); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(raw), `"version": 3`) {
		t.Fatalf("expected version 3 after re-save, got: %s", raw)
	}
}

// TestLoadFrom_UnversionedMigration: a file with no "version" field at all
// (Version == 0) is treated the same as v1, lifted under registry.LocalScope.
func TestLoadFrom_UnversionedMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	unversioned := `{"vms":{"x":{"A":"1"}}}`
	if err := os.WriteFile(path, []byte(unversioned), 0o600); err != nil {
		t.Fatalf("seed unversioned file: %v", err)
	}
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load unversioned: %v", err)
	}
	if got := s.Get("x", registry.LocalScope); !reflect.DeepEqual(got, map[string]string{"A": "1"}) {
		t.Fatalf("migrated global scope = %v, want {A:1}", got)
	}
}

// TestGetAllSetAll_RoundTrip: SetAll persists a scope->pairs map that
// survives a reload via GetAll, including a non-global scope.
func TestGetAllSetAll_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	scopes := map[string]map[string]string{
		"":                {"EDITOR": "vim"},
		"github.com/acme": {"GH_TOKEN": "ghp_x"},
	}
	if err := s.SetAll("claude", registry.LocalScope, scopes); err != nil {
		t.Fatalf("SetAll: %v", err)
	}

	s2, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := s2.GetAll("claude", registry.LocalScope)
	if !reflect.DeepEqual(got, scopes) {
		t.Fatalf("GetAll after reload = %v, want %v", got, scopes)
	}

	// GetAll returns a deep copy: mutating it must not affect the store.
	got[""]["EDITOR"] = "mutated"
	got["NEW"] = map[string]string{"X": "1"}
	again := s2.GetAll("claude", registry.LocalScope)
	if again[""]["EDITOR"] != "vim" {
		t.Fatalf("GetAll must return a defensive deep copy; store was mutated: %v", again)
	}
	if _, ok := again["NEW"]; ok {
		t.Fatalf("GetAll must return a defensive deep copy; top-level map was mutated: %v", again)
	}

	// Raw on-disk shape check: version 3, nested scope maps.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	if !strings.Contains(string(raw), `"version": 3`) {
		t.Fatalf("expected version 3, got: %s", raw)
	}
	if !strings.Contains(string(raw), `"github.com/acme"`) {
		t.Fatalf("expected scoped key in raw JSON, got: %s", raw)
	}
}

// TestSetAll_EmptyOrAllEmptyDropsEntry: an empty scopes map, or a map whose
// scopes are all empty, drops the VM's entry rather than persisting an empty
// object tree.
func TestSetAll_EmptyOrAllEmptyDropsEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	s, _ := LoadFrom(path)
	if err := s.SetAll("claude", registry.LocalScope, map[string]map[string]string{"": {"A": "1"}}); err != nil {
		t.Fatalf("seed SetAll: %v", err)
	}
	if err := s.SetAll("claude", registry.LocalScope, map[string]map[string]string{}); err != nil {
		t.Fatalf("SetAll empty: %v", err)
	}
	if got := s.GetAll("claude", registry.LocalScope); len(got) != 0 {
		t.Fatalf("empty SetAll should clear the entry, got %v", got)
	}

	if err := s.SetAll("claude", registry.LocalScope, map[string]map[string]string{"": {"A": "1"}}); err != nil {
		t.Fatalf("re-seed SetAll: %v", err)
	}
	if err := s.SetAll("claude", registry.LocalScope, map[string]map[string]string{"": {}, "scope": {}}); err != nil {
		t.Fatalf("SetAll all-empty scopes: %v", err)
	}
	if got := s.GetAll("claude", registry.LocalScope); len(got) != 0 {
		t.Fatalf("all-empty-scope SetAll should clear the entry, got %v", got)
	}
}

// TestSetAll_RejectsHostileScope: SetAll must reject a hostile scope string
// before it can be persisted, since a scope reaches the guest as a
// filesystem path and a gitdir: pattern.
func TestSetAll_RejectsHostileScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	s, _ := LoadFrom(path)
	if err := s.SetAll("claude", registry.LocalScope, map[string]map[string]string{"../etc": {"A": "1"}}); err == nil {
		t.Fatal("SetAll must reject a hostile scope")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("rejected SetAll must not write the store (stat err = %v)", statErr)
	}
	if got := s.GetAll("claude", registry.LocalScope); len(got) != 0 {
		t.Fatalf("rejected SetAll must not mutate the in-memory store, got %v", got)
	}
}

// TestSave_ChmodDirFailure_LeavesNoWorldReadableFile drives the
// os.Chmod(dir, 0o700) failure arm at secrets.go:348. The store's parent
// "directory" is actually a symlink pointing at /proc, an existing directory
// this (unprivileged) test process does not own: os.MkdirAll succeeds
// trivially (the stat sees an existing directory and returns immediately
// without touching it), but the subsequent os.Chmod follows the symlink and
// fails with EPERM, since only the owner (or a privileged process) may chmod
// a file. That lets us force the chmod arm via pure filesystem state -- no
// production-code seam -- without ever risking a mutation to /proc, since
// chmod is atomic: it either succeeds or leaves the target untouched.
func TestSave_ChmodDirFailure_LeavesNoWorldReadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission arms need a non-root euid")
	}

	before, statErr := os.Stat("/proc")
	if statErr != nil {
		t.Fatalf("stat /proc before: %v", statErr)
	}
	origMode := before.Mode().Perm()
	if origMode == 0o700 {
		t.Skip("/proc is already mode 0700 in this environment; the sanity check below can't distinguish success from a no-op")
	}

	base := t.TempDir()
	link := filepath.Join(base, "sandbar")
	if err := os.Symlink("/proc", link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	path := filepath.Join(link, "secrets.json")

	s := &Store{path: path, vms: map[registry.Scope]map[string]map[string]map[string]string{}}
	err := s.Set("claude", registry.LocalScope, map[string]string{"TOK": "s3cr3t"})
	if err == nil {
		t.Fatal("Set must fail when the parent directory cannot be chmod'd")
	}

	// No secrets.json (partial or otherwise) must have leaked into /proc.
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("save must not leave a file behind after a chmod failure (stat err = %v)", statErr)
	}

	// Sanity: /proc itself must be untouched by the failed chmod attempt --
	// the chmod call must have failed outright (EPERM), not partially
	// applied.
	after, statErr := os.Stat("/proc")
	if statErr != nil {
		t.Fatalf("stat /proc after: %v", statErr)
	}
	if got := after.Mode().Perm(); got != origMode {
		t.Fatalf("failed chmod must not have altered /proc's mode: was %04o, now %04o", origMode, got)
	}
}

// TestSave_RenameFailure_LeavesNoPartialFile drives the os.Rename(tmpName,
// s.path) failure arm at secrets.go:378 via a path collision: a directory
// pre-exists at the exact path save() would rename the temp file onto, so
// os.CreateTemp/Write/Sync/Close all succeed but the final os.Rename fails
// (the kernel refuses to rename a regular file onto an existing directory).
// save() must return that error, clean up its temp file, and leave the
// pre-existing directory at s.path untouched -- never a half-written temp
// file promoted over it, and no stray temp file left behind either.
//
// FOLLOW-UP: the sibling failure arms in this same block -- os.CreateTemp,
// tmp.Write, tmp.Sync, and tmp.Close (secrets.go:359-377) -- could not be
// forced from pure filesystem state as an unprivileged user (they need e.g.
// ENOSPC/EDQUOTA injection, which requires a quota-limited or size-limited
// filesystem mount that isn't available without elevated setup). Reaching
// them deterministically would need a production-code seam (an injectable
// io.Writer/Syncer or a filesystem abstraction) and is out of scope for this
// test-only task.
func TestSave_RenameFailure_LeavesNoPartialFile(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "sandbar")
	path := filepath.Join(dir, "secrets.json")

	// Place a directory exactly where save() expects to rename its temp
	// file onto.
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("seed directory at target path: %v", err)
	}

	s := &Store{path: path, vms: map[registry.Scope]map[string]map[string]map[string]string{}}
	err := s.Set("claude", registry.LocalScope, map[string]string{"TOK": "s3cr3t"})
	if err == nil {
		t.Fatal("Set must fail when os.Rename cannot replace an existing directory")
	}

	// The pre-existing directory must survive untouched -- never replaced by
	// a partial (or complete) file.
	fi, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("target path must still exist: %v", statErr)
	}
	if !fi.IsDir() {
		t.Fatalf("target path must still be a directory, not a file (got mode %v)", fi.Mode())
	}

	// No leftover temp file (or anything else) beside the original directory
	// entry: the failed save must have cleaned up after itself. The lock file
	// this task introduces ("secrets.json.lock") IS expected -- it is created
	// and released (not removed) before the reload/save ever runs.
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	sawSecretsDir := false
	for _, e := range ents {
		switch {
		case e.Name() == "secrets.json" && e.IsDir():
			sawSecretsDir = true
		case e.Name() == "secrets.json.lock":
			// expected: the lock file, not the target.
		default:
			t.Errorf("unexpected leftover entry in %s: %q", dir, e.Name())
		}
	}
	if !sawSecretsDir {
		t.Fatalf("expected the original secrets.json directory to survive in %s, got %v", dir, ents)
	}
}

// TestSetAll_AllOrNothingOnInvalidKey: a valid scope paired with an invalid
// key anywhere in the call rejects the whole SetAll, mirroring Set's
// all-or-nothing behavior.
func TestSetAll_AllOrNothingOnInvalidKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	s, _ := LoadFrom(path)
	scopes := map[string]map[string]string{
		"github.com/acme": {"OK": "1", "BAD KEY": "x"},
	}
	if err := s.SetAll("claude", registry.LocalScope, scopes); err == nil {
		t.Fatal("SetAll must reject a map containing an invalid key")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("rejected SetAll must not write the store (stat err = %v)", statErr)
	}
	if got := s.GetAll("claude", registry.LocalScope); len(got) != 0 {
		t.Fatalf("rejected SetAll must not mutate the in-memory store, got %v", got)
	}
}

// TestLoadFrom_V2FixtureMigratesToLocalScope is the risk-floor test for this
// task's data migration: a REAL captured pre-migration (v2) file — with both
// a global-scope secret and a directory-scoped one, across two VMs — must
// load with every secret intact, entirely under registry.LocalScope. A v2
// file predates connection scopes (no remote provider existed when it was
// written), so anything it recorded could only ever have been local; a botched
// read path here would silently lose a host's secrets on upgrade.
func TestLoadFrom_V2FixtureMigratesToLocalScope(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "v2_fixture.json"))
	if err != nil {
		t.Fatalf("read v2 fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("seed v2 fixture: %v", err)
	}

	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load v2 fixture: %v", err)
	}

	if got, want := s.Get("web", registry.LocalScope), (map[string]string{"TOKEN": "abc123"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("Get(web, LocalScope) = %v, want %v", got, want)
	}
	if got, want := s.Get("db", registry.LocalScope), (map[string]string{"DB_PASS": "hunter2"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("Get(db, LocalScope) = %v, want %v", got, want)
	}

	all := s.GetAll("web", registry.LocalScope)
	if got, want := all["github.com/acme"], (map[string]string{"GH_TOKEN": "ghp_xyz"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("directory-scoped secret lost migrating v2->v3: GetAll(web, LocalScope)[\"github.com/acme\"] = %v, want %v", got, want)
	}

	// A v2 file predates connection scopes: its VMs must NOT be reachable
	// under any OTHER connection scope, only under LocalScope.
	remote := registry.Scope{Provider: "lima-remote", RemoteTarget: "user@host:22"}
	if got := s.Get("web", remote); len(got) != 0 {
		t.Fatalf("v2-migrated secrets leaked into a non-local connection scope: %v", got)
	}

	// The next save must stamp the file version 3.
	if err := s.Set("web", registry.LocalScope, s.Get("web", registry.LocalScope)); err != nil {
		t.Fatalf("re-save after migration: %v", err)
	}
	raw2, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back after re-save: %v", err)
	}
	if !strings.Contains(string(raw2), `"version": 3`) {
		t.Fatalf("expected version 3 after re-save, got: %s", raw2)
	}
}

// TestConnectionScope_IsolatesSameNamedVM is the v3 round-trip test: two VMs
// that share a bare NAME ("web") but live under different connection scopes
// (registry.Scope{Provider, RemoteTarget}) must hold entirely independent
// secrets — this is the whole point of the migration (a `web` on local and a
// `web` on a remote host must never share, or clobber, each other's secrets).
func TestConnectionScope_IsolatesSameNamedVM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	remote := registry.Scope{Provider: "lima-remote", RemoteTarget: "user@example.com:22"}

	if err := s.Set("web", registry.LocalScope, map[string]string{"TOKEN": "local-secret"}); err != nil {
		t.Fatalf("set local-scoped web: %v", err)
	}
	if err := s.Set("web", remote, map[string]string{"TOKEN": "remote-secret"}); err != nil {
		t.Fatalf("set remote-scoped web: %v", err)
	}

	s2, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	if got := s2.Get("web", registry.LocalScope); got["TOKEN"] != "local-secret" {
		t.Fatalf("local-scoped web.TOKEN = %v, want local-secret", got)
	}
	if got := s2.Get("web", remote); got["TOKEN"] != "remote-secret" {
		t.Fatalf("remote-scoped web.TOKEN = %v, want remote-secret", got)
	}

	// Removing one connection scope's VM must not touch the other's.
	if err := s2.Remove("web", remote); err != nil {
		t.Fatalf("remove remote-scoped web: %v", err)
	}
	if got := s2.Get("web", registry.LocalScope); got["TOKEN"] != "local-secret" {
		t.Fatalf("Remove under one connection scope affected another scope's same-named VM: %v", got)
	}
	if got := s2.Get("web", remote); len(got) != 0 {
		t.Fatalf("expected remote-scoped web secrets gone after Remove, got %v", got)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	if !strings.Contains(string(raw), `"version": 3`) {
		t.Fatalf("expected version 3, got: %s", raw)
	}
}

// TestConcurrentMutations_DifferentVMsBothSurvive is the lost-update
// regression test this task fixes: two *Store instances loaded from the SAME
// file (as two separate `sand` processes sharing a data dir would) must not
// clobber each other's writes to DIFFERENT VMs. s2 loads the file BEFORE s1's
// Set(vm-a) commits, so s2's own in-memory view has no idea vm-a exists; if
// Remove(vm-b) persisted from that stale snapshot (the pre-task blind
// save-of-s.vms behavior), it would silently erase vm-a along with vm-b. The
// locked reload-merge must instead re-read the CURRENT on-disk state before
// applying its own (connScope, vm) delta, so vm-a survives.
func TestConcurrentMutations_DifferentVMsBothSurvive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")

	seed, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("seed load: %v", err)
	}
	if err := seed.Set("vm-b", registry.LocalScope, map[string]string{"B": "orig"}); err != nil {
		t.Fatalf("seed vm-b: %v", err)
	}

	// Two independent Store instances, both loaded from the file as it stood
	// right after the seed -- neither has yet observed the other's write.
	s1, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("s1 load: %v", err)
	}
	s2, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("s2 load: %v", err)
	}

	if err := s1.Set("vm-a", registry.LocalScope, map[string]string{"A": "1"}); err != nil {
		t.Fatalf("s1 set vm-a: %v", err)
	}
	// s2's in-memory snapshot predates s1's write and knows nothing of vm-a.
	if err := s2.Remove("vm-b", registry.LocalScope); err != nil {
		t.Fatalf("s2 remove vm-b: %v", err)
	}

	final, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("final load: %v", err)
	}
	if got := final.Get("vm-a", registry.LocalScope); got["A"] != "1" {
		t.Fatalf("s1's Set(vm-a) was lost by a concurrent Remove(vm-b) against a stale snapshot: %v", got)
	}
	if got := final.Get("vm-b", registry.LocalScope); len(got) != 0 {
		t.Fatalf("vm-b should have been removed, got %v", got)
	}
}

// TestLockedWrite_SecurityInvariantsAndLockFile extends TestSet_FilePermissions
// to the new locked write path: the secrets file is still 0600, the parent
// directory is still forced 0700, and the cross-process lock file this task
// introduces lives at exactly "<datadir>/secrets.json.lock" (per the
// acceptance criteria) at mode 0600 -- it must never itself be a
// world-readable artifact beside the secrets it protects. No other stray file
// is left behind.
func TestLockedWrite_SecurityInvariantsAndLockFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sandbar")
	path := filepath.Join(dir, "secrets.json")
	lockPath := path + ".lock"

	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := s.Set("claude", registry.LocalScope, map[string]string{"TOK": "s3cr3t"}); err != nil {
		t.Fatalf("set: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat secrets file: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("secrets.json mode = %04o, want 0600", got)
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("parent dir mode = %04o, want 0700", got)
	}

	li, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("expected a lock file at %s: %v", lockPath, err)
	}
	if got := li.Mode().Perm(); got != 0o600 {
		t.Errorf("lock file mode = %04o, want 0600", got)
	}

	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range ents {
		if e.Name() != "secrets.json" && e.Name() != "secrets.json.lock" {
			t.Errorf("unexpected leftover file in secrets dir: %q", e.Name())
		}
	}
}

// TestSet_MigratingV1File_NoDeadlockNoDoubleSave is the re-entrancy test: a
// mutation against a v1 file that must migrate in memory has to reload the
// current on-disk bytes and re-run the SAME version detection/migration used
// at process start, ALL while still holding the one lock taken at the
// mutation boundary. If the reload path (reloadUnlocked/parseTree) ever tried
// to acquire the lock again, or called save() itself, this would either
// self-deadlock (a second flock on the same path from the same process blocks
// exactly as it would against a different process) or double-write the file.
// Running the call on a goroutine with a bounded timeout turns a deadlock into
// a fast, deterministic test failure instead of an indefinite hang.
func TestSet_MigratingV1File_NoDeadlockNoDoubleSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	v1 := `{"version":1,"vms":{"x":{"A":"1"}}}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatalf("seed v1 file: %v", err)
	}

	s, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load v1: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- s.Set("x", registry.LocalScope, map[string]string{"A": "1", "B": "2"})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("set against a migrating v1 file: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Set deadlocked while reloading/migrating a v1 file under its own lock")
	}

	// The migration must have completed correctly (both keys present) and the
	// merged result persisted exactly once, stamped at the current version --
	// not left at v1, and not corrupted by a double write.
	got := s.Get("x", registry.LocalScope)
	want := map[string]string{"A": "1", "B": "2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Get after migrating Set = %v, want %v", got, want)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(raw), `"version": 3`) {
		t.Fatalf("expected version 3 after migrating Set, got: %s", raw)
	}
}

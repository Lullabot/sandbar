package secrets

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
	if err := s.Set("claude", map[string]string{"TOK": "s3cr3t"}); err != nil {
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

	// The atomic write must leave no temp files behind.
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range ents {
		if e.Name() != "secrets.json" {
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
	if err := s.Set("claude", map[string]string{"A": "1", "B": "two"}); err != nil {
		t.Fatalf("set: %v", err)
	}

	s2, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := s2.Get("claude")
	want := map[string]string{"A": "1", "B": "two"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Get after reload = %v, want %v", got, want)
	}

	// Get returns a copy: mutating it must not affect the store.
	got["A"] = "mutated"
	got["NEW"] = "x"
	if again := s2.Get("claude"); again["A"] != "1" || len(again) != 2 {
		t.Fatalf("Get must return a defensive copy; store was mutated: %v", again)
	}

	if err := s2.Remove("claude"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	s3, _ := LoadFrom(path)
	if len(s3.Get("claude")) != 0 {
		t.Fatalf("claude secrets should be gone after remove, got %v", s3.Get("claude"))
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
	if err := s.Set("claude", map[string]string{"OK": "1", "BAD KEY": "x"}); err == nil {
		t.Fatal("Set must reject a map containing an invalid key")
	}
	// A rejected Set must not partially persist.
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("rejected Set must not write the store (stat err = %v)", statErr)
	}
	if len(s.Get("claude")) != 0 {
		t.Fatalf("rejected Set must not mutate the in-memory store, got %v", s.Get("claude"))
	}
}

// TestSet_EmptyRemovesEntry: setting an empty map for a VM drops its entry rather
// than persisting an empty object.
func TestSet_EmptyRemovesEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sandbar", "secrets.json")
	s, _ := LoadFrom(path)
	if err := s.Set("claude", map[string]string{"A": "1"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.Set("claude", map[string]string{}); err != nil {
		t.Fatalf("set empty: %v", err)
	}
	if len(s.Get("claude")) != 0 {
		t.Fatalf("empty Set should clear the entry, got %v", s.Get("claude"))
	}
}

// TestSet_TouchesOnlySecretsJSON: this task must not create or write
// managed-vms.json; secrets live in a distinct file.
func TestSet_TouchesOnlySecretsJSON(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sandbar")
	path := filepath.Join(dir, "secrets.json")
	s, _ := LoadFrom(path)
	if err := s.Set("claude", map[string]string{"A": "1"}); err != nil {
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
	if len(s.Get("anything")) != 0 {
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
	if len(s.Get("x")) != 0 {
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
	if s == nil || len(s.Get("anything")) != 0 {
		t.Fatal("should still return a usable empty store")
	}
	// The store must remain usable.
	if err := s.Set("claude", map[string]string{"A": "1"}); err != nil {
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
// VM's pairs live under the global scope "", and the next save stamps
// version:2 on disk.
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
	if got := s.Get("x"); !reflect.DeepEqual(got, map[string]string{"A": "1"}) {
		t.Fatalf("migrated global scope = %v, want {A:1}", got)
	}
	all := s.GetAll("x")
	if got := all[""]; !reflect.DeepEqual(got, map[string]string{"A": "1"}) {
		t.Fatalf("GetAll()[\"\"] = %v, want {A:1}", got)
	}

	// Force a save and confirm the on-disk shape is stamped version 2.
	if err := s.Set("x", s.Get("x")); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(raw), `"version": 2`) {
		t.Fatalf("expected version 2 after re-save, got: %s", raw)
	}
}

// TestLoadFrom_UnversionedMigration: a file with no "version" field at all
// (Version == 0) is treated the same as v1.
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
	if got := s.Get("x"); !reflect.DeepEqual(got, map[string]string{"A": "1"}) {
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
	if err := s.SetAll("claude", scopes); err != nil {
		t.Fatalf("SetAll: %v", err)
	}

	s2, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := s2.GetAll("claude")
	if !reflect.DeepEqual(got, scopes) {
		t.Fatalf("GetAll after reload = %v, want %v", got, scopes)
	}

	// GetAll returns a deep copy: mutating it must not affect the store.
	got[""]["EDITOR"] = "mutated"
	got["NEW"] = map[string]string{"X": "1"}
	again := s2.GetAll("claude")
	if again[""]["EDITOR"] != "vim" {
		t.Fatalf("GetAll must return a defensive deep copy; store was mutated: %v", again)
	}
	if _, ok := again["NEW"]; ok {
		t.Fatalf("GetAll must return a defensive deep copy; top-level map was mutated: %v", again)
	}

	// Raw on-disk shape check: version 2, nested scope maps.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	if !strings.Contains(string(raw), `"version": 2`) {
		t.Fatalf("expected version 2, got: %s", raw)
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
	if err := s.SetAll("claude", map[string]map[string]string{"": {"A": "1"}}); err != nil {
		t.Fatalf("seed SetAll: %v", err)
	}
	if err := s.SetAll("claude", map[string]map[string]string{}); err != nil {
		t.Fatalf("SetAll empty: %v", err)
	}
	if got := s.GetAll("claude"); len(got) != 0 {
		t.Fatalf("empty SetAll should clear the entry, got %v", got)
	}

	if err := s.SetAll("claude", map[string]map[string]string{"": {"A": "1"}}); err != nil {
		t.Fatalf("re-seed SetAll: %v", err)
	}
	if err := s.SetAll("claude", map[string]map[string]string{"": {}, "scope": {}}); err != nil {
		t.Fatalf("SetAll all-empty scopes: %v", err)
	}
	if got := s.GetAll("claude"); len(got) != 0 {
		t.Fatalf("all-empty-scope SetAll should clear the entry, got %v", got)
	}
}

// TestSetAll_RejectsHostileScope: SetAll must reject a hostile scope string
// before it can be persisted, since a scope reaches the guest as a
// filesystem path and a gitdir: pattern.
func TestSetAll_RejectsHostileScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	s, _ := LoadFrom(path)
	if err := s.SetAll("claude", map[string]map[string]string{"../etc": {"A": "1"}}); err == nil {
		t.Fatal("SetAll must reject a hostile scope")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("rejected SetAll must not write the store (stat err = %v)", statErr)
	}
	if got := s.GetAll("claude"); len(got) != 0 {
		t.Fatalf("rejected SetAll must not mutate the in-memory store, got %v", got)
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
	if err := s.SetAll("claude", scopes); err == nil {
		t.Fatal("SetAll must reject a map containing an invalid key")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("rejected SetAll must not write the store (stat err = %v)", statErr)
	}
	if got := s.GetAll("claude"); len(got) != 0 {
		t.Fatalf("rejected SetAll must not mutate the in-memory store, got %v", got)
	}
}

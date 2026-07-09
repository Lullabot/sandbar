package secrets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withXDGDataHome points XDG_DATA_HOME at a fresh temp dir for the duration
// of the test so we never touch the real user data dir.
func withXDGDataHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	return dir
}

func TestPath_ResolvesUnderXDGDataHome(t *testing.T) {
	xdg := withXDGDataHome(t)

	got := Path("my-vm")
	want := filepath.Join(xdg, "sandbar", "secrets", "my-vm.json")
	if got != want {
		t.Fatalf("Path(%q) = %q, want %q", "my-vm", got, want)
	}
}

func TestLoad_MissingFileReturnsEmptyStoreNotError(t *testing.T) {
	withXDGDataHome(t)

	s, err := Load("does-not-exist")
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if s == nil {
		t.Fatal("Load() returned nil store")
	}
	if len(s.Global) != 0 || len(s.GitHub) != 0 || len(s.DirEnv) != 0 {
		t.Fatalf("Load() on missing file should be empty, got %+v", s)
	}
}

func TestSave_SetsFileMode0600AndDirMode0700(t *testing.T) {
	withXDGDataHome(t)

	s := &Store{Version: 1}
	s.SetSecret(CategoryGlobal, "", "MY_VAR", "super-secret")

	if err := s.Save("perm-vm"); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	path := Path("perm-vm")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("os.Stat(%q) error: %v", path, err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("file mode = %o, want %o", mode, 0o600)
	}

	dirFi, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("os.Stat(dir) error: %v", err)
	}
	if mode := dirFi.Mode().Perm(); mode != 0o700 {
		t.Fatalf("dir mode = %o, want %o", mode, 0o700)
	}
}

func TestRoundTrip_SaveThenLoadDeepEquality(t *testing.T) {
	withXDGDataHome(t)

	s := &Store{Version: 1}
	s.SetSecret(CategoryGlobal, "", "MY_VAR", "global-value")
	s.SetSecret(CategoryGitHub, "github.com/acme", "", "gh-token-value")
	s.SetSecret(CategoryDirEnv, "github.com/acme", "SOME_VAR", "dir-env-value")

	if err := s.Save("round-trip-vm"); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	loaded, err := Load("round-trip-vm")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if loaded.Version != s.Version {
		t.Fatalf("Version = %d, want %d", loaded.Version, s.Version)
	}
	if len(loaded.Global) != 1 || loaded.Global[0] != (GlobalSecret{Name: "MY_VAR", Value: "global-value"}) {
		t.Fatalf("Global round-trip mismatch: %+v", loaded.Global)
	}
	if len(loaded.GitHub) != 1 || loaded.GitHub[0] != (GitHubSecret{Scope: "github.com/acme", Token: "gh-token-value"}) {
		t.Fatalf("GitHub round-trip mismatch: %+v", loaded.GitHub)
	}
	if len(loaded.DirEnv) != 1 || loaded.DirEnv[0] != (DirEnvSecret{Scope: "github.com/acme", Name: "SOME_VAR", Value: "dir-env-value"}) {
		t.Fatalf("DirEnv round-trip mismatch: %+v", loaded.DirEnv)
	}
}

func TestOnDiskSchema_MatchesContract(t *testing.T) {
	xdg := withXDGDataHome(t)

	s := &Store{Version: 1}
	s.SetSecret(CategoryGlobal, "", "", "")
	s.SetSecret(CategoryGitHub, "github.com/acme", "", "")
	s.SetSecret(CategoryDirEnv, "", "", "")

	if err := s.Save("schema-vm"); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(xdg, "sandbar", "secrets", "schema-vm.json"))
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}

	wantJSON := `{"version":1,"global":[{"name":"","value":""}],"github":[{"scope":"github.com/acme","token":""}],"dir_env":[{"scope":"","name":"","value":""}]}`
	var want map[string]any
	if err := json.Unmarshal([]byte(wantJSON), &want); err != nil {
		t.Fatalf("Unmarshal(want) error: %v", err)
	}

	gotBytes, _ := json.Marshal(got)
	wantBytes, _ := json.Marshal(want)
	if string(gotBytes) != string(wantBytes) {
		t.Fatalf("schema mismatch:\n got: %s\nwant: %s", gotBytes, wantBytes)
	}
}

func TestRemoveSecret(t *testing.T) {
	s := &Store{Version: 1}
	s.SetSecret(CategoryGlobal, "", "MY_VAR", "v")
	s.SetSecret(CategoryGitHub, "github.com/acme", "", "t")
	s.SetSecret(CategoryDirEnv, "github.com/acme", "SOME_VAR", "v")

	if !s.RemoveSecret(CategoryGlobal, "", "MY_VAR") {
		t.Fatal("RemoveSecret(global) should report removed")
	}
	if len(s.Global) != 0 {
		t.Fatalf("Global should be empty after removal, got %+v", s.Global)
	}

	if !s.RemoveSecret(CategoryGitHub, "github.com/acme", "") {
		t.Fatal("RemoveSecret(github) should report removed")
	}
	if len(s.GitHub) != 0 {
		t.Fatalf("GitHub should be empty after removal, got %+v", s.GitHub)
	}

	if !s.RemoveSecret(CategoryDirEnv, "github.com/acme", "SOME_VAR") {
		t.Fatal("RemoveSecret(dir_env) should report removed")
	}
	if len(s.DirEnv) != 0 {
		t.Fatalf("DirEnv should be empty after removal, got %+v", s.DirEnv)
	}

	if s.RemoveSecret(CategoryGlobal, "", "NOT_THERE") {
		t.Fatal("RemoveSecret on missing key should report false")
	}
}

func TestValue_ReturnsCleartextByKey(t *testing.T) {
	s := &Store{Version: 1}
	s.SetSecret(CategoryGlobal, "", "MY_VAR", "gv")
	s.SetSecret(CategoryGitHub, "github.com/acme", "", "tok")
	s.SetSecret(CategoryDirEnv, "some/dir", "DV", "dv")

	if v, ok := s.Value(CategoryGlobal, "", "MY_VAR"); !ok || v != "gv" {
		t.Fatalf("Value(global) = %q,%v want gv,true", v, ok)
	}
	if v, ok := s.Value(CategoryGitHub, "github.com/acme", ""); !ok || v != "tok" {
		t.Fatalf("Value(github) = %q,%v want tok,true", v, ok)
	}
	if v, ok := s.Value(CategoryDirEnv, "some/dir", "DV"); !ok || v != "dv" {
		t.Fatalf("Value(dir_env) = %q,%v want dv,true", v, ok)
	}
	if _, ok := s.Value(CategoryGlobal, "", "MISSING"); ok {
		t.Fatal("Value on a missing key should report false")
	}
}

func TestSetSecret_UpdatesExistingInPlace(t *testing.T) {
	s := &Store{Version: 1}
	s.SetSecret(CategoryGlobal, "", "MY_VAR", "first")
	s.SetSecret(CategoryGlobal, "", "MY_VAR", "second")

	if len(s.Global) != 1 {
		t.Fatalf("expected a single entry after update, got %+v", s.Global)
	}
	if s.Global[0].Value != "second" {
		t.Fatalf("Global[0].Value = %q, want %q", s.Global[0].Value, "second")
	}
}

func TestRedacted_MasksValuesAndNeverReturnsCleartext(t *testing.T) {
	const secretGlobalValue = "super-secret-global"
	const secretGitHubToken = "ghp_supersecrettoken"
	const secretDirEnvValue = "dir-env-cleartext"

	s := &Store{Version: 1}
	s.SetSecret(CategoryGlobal, "", "MY_VAR", secretGlobalValue)
	s.SetSecret(CategoryGitHub, "github.com/acme", "", secretGitHubToken)
	s.SetSecret(CategoryDirEnv, "github.com/acme", "SOME_VAR", secretDirEnvValue)

	entries := s.Redacted()
	if len(entries) != 3 {
		t.Fatalf("Redacted() returned %d entries, want 3", len(entries))
	}

	for _, e := range entries {
		if e.Masked == "" {
			t.Fatalf("entry %+v has empty masked value", e)
		}
		if e.Masked == secretGlobalValue || e.Masked == secretGitHubToken || e.Masked == secretDirEnvValue {
			t.Fatalf("Redacted() leaked cleartext in entry %+v", e)
		}
		if strings.Contains(e.Masked, "secret") || strings.Contains(e.Masked, "token") {
			t.Fatalf("Redacted() masked value looks like it contains cleartext: %+v", e)
		}
	}

	// Also assert the whole struct, marshaled, never contains any of the
	// cleartext secret substrings anywhere (defence in depth for the
	// "never returns cleartext" contract).
	blob, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	for _, secret := range []string{secretGlobalValue, secretGitHubToken, secretDirEnvValue} {
		if strings.Contains(string(blob), secret) {
			t.Fatalf("Redacted() output contains cleartext secret %q: %s", secret, blob)
		}
	}
}

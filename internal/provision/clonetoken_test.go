package provision

import (
	"testing"

	"github.com/lullabot/sandbar/internal/secrets"
	"github.com/lullabot/sandbar/internal/vm"
)

// TestRecordCloneTokenSecret_GitHubURLRecordsScopedSecret is the AC3
// (`--clone-token` reshape) test: a github.com clone URL + token records
// {scope: "github.com/<org>", token} as a CategoryGitHub secret in the host
// store for cfg.Name.
func TestRecordCloneTokenSecret_GitHubURLRecordsScopedSecret(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cfg := vm.CreateConfig{Name: "claude", CloneURL: "https://github.com/acme/repo.git", CloneToken: "ghp_tok123"}
	if err := RecordCloneTokenSecret(cfg); err != nil {
		t.Fatalf("RecordCloneTokenSecret: %v", err)
	}

	store, err := secrets.Load(cfg.Name)
	if err != nil {
		t.Fatalf("secrets.Load: %v", err)
	}
	if len(store.GitHub) != 1 || store.GitHub[0] != (secrets.GitHubSecret{Scope: "github.com/acme", Token: "ghp_tok123"}) {
		t.Fatalf("store.GitHub = %+v, want a single {github.com/acme ghp_tok123} entry", store.GitHub)
	}
	// No other category is touched.
	if len(store.Global) != 0 || len(store.DirEnv) != 0 {
		t.Fatalf("RecordCloneTokenSecret must only touch the github category: %+v", store)
	}
}

// TestRecordCloneTokenSecret_NonGitHubHostIsNoop mirrors the old code's
// GitHub-only gating: a token supplied for a non-github.com clone URL is not
// recorded anywhere (no host currently renders credentials for it).
func TestRecordCloneTokenSecret_NonGitHubHostIsNoop(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cfg := vm.CreateConfig{Name: "claude", CloneURL: "https://gitlab.com/acme/repo.git", CloneToken: "tok"}
	if err := RecordCloneTokenSecret(cfg); err != nil {
		t.Fatalf("RecordCloneTokenSecret: %v", err)
	}

	store, err := secrets.Load(cfg.Name)
	if err != nil {
		t.Fatalf("secrets.Load: %v", err)
	}
	if len(store.GitHub) != 0 {
		t.Fatalf("non-github.com URL must not record a secret, got %+v", store.GitHub)
	}
}

// TestRecordCloneTokenSecret_NoTokenIsNoop: an empty token records nothing,
// even for a github.com URL.
func TestRecordCloneTokenSecret_NoTokenIsNoop(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cfg := vm.CreateConfig{Name: "claude", CloneURL: "https://github.com/acme/repo.git", CloneToken: ""}
	if err := RecordCloneTokenSecret(cfg); err != nil {
		t.Fatalf("RecordCloneTokenSecret: %v", err)
	}
	store, err := secrets.Load(cfg.Name)
	if err != nil {
		t.Fatalf("secrets.Load: %v", err)
	}
	if len(store.GitHub) != 0 {
		t.Fatalf("empty token must not record a secret, got %+v", store.GitHub)
	}
}

// TestRecordCloneTokenSecret_NoOrgSegmentIsNoop: a bare github.com/repo URL
// (no org component) has nothing for cloneOrgRelDir to scope to, so nothing
// is recorded — mirrors Reset's existing "no org URL" handling.
func TestRecordCloneTokenSecret_NoOrgSegmentIsNoop(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cfg := vm.CreateConfig{Name: "claude", CloneURL: "https://github.com/justrepo", CloneToken: "tok"}
	if err := RecordCloneTokenSecret(cfg); err != nil {
		t.Fatalf("RecordCloneTokenSecret: %v", err)
	}
	store, err := secrets.Load(cfg.Name)
	if err != nil {
		t.Fatalf("secrets.Load: %v", err)
	}
	if len(store.GitHub) != 0 {
		t.Fatalf("no-org URL must not record a secret, got %+v", store.GitHub)
	}
}

// TestRecordCloneTokenSecret_UpdatesExistingScopeInPlace: recording twice for
// the same org scope updates the token in place (SetSecret's update
// semantics) rather than accumulating duplicate entries — important for a
// re-run `sand create --clone-token <new>` against an existing store.
func TestRecordCloneTokenSecret_UpdatesExistingScopeInPlace(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cfg := vm.CreateConfig{Name: "claude", CloneURL: "https://github.com/acme/repo.git", CloneToken: "first-token"}
	if err := RecordCloneTokenSecret(cfg); err != nil {
		t.Fatalf("RecordCloneTokenSecret (1st): %v", err)
	}
	cfg.CloneToken = "second-token"
	if err := RecordCloneTokenSecret(cfg); err != nil {
		t.Fatalf("RecordCloneTokenSecret (2nd): %v", err)
	}

	store, err := secrets.Load(cfg.Name)
	if err != nil {
		t.Fatalf("secrets.Load: %v", err)
	}
	if len(store.GitHub) != 1 || store.GitHub[0].Token != "second-token" {
		t.Fatalf("store.GitHub = %+v, want a single entry updated to second-token", store.GitHub)
	}
}

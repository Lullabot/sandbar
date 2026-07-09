package main

import (
	"context"
	"io"
	"testing"

	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/secrets"
	"github.com/lullabot/sandbar/internal/vm"
)

// stubProvisioner is a no-op headlessProvisioner double: CreateVM/Recreate
// both "succeed" without touching lima, so TestHeadlessCreateRecordsManagedVM
// exercises only the managed-registry bookkeeping doHeadlessCreate performs
// after a successful run (the parity guarantee with the TUI).
type stubProvisioner struct{}

func (stubProvisioner) CreateVM(_ context.Context, _ vm.CreateConfig, _ io.Writer) error {
	return nil
}

func (stubProvisioner) Recreate(_ context.Context, _ vm.CreateConfig, _ io.Writer) error {
	return nil
}

// stubBaseDeleter is a no-op limaBaseDeleter double for the --rebuild path.
type stubBaseDeleter struct{}

func (stubBaseDeleter) Status(_ string) (string, error) { return "", nil }
func (stubBaseDeleter) Delete(_ string, _ bool) error   { return nil }

// TestHeadlessCreateRecordsManagedVM is the load-bearing parity guarantee
// called out in task 3: a headless `sand create` must record the VM as
// managed with its CreateConfig, exactly like the interactive TUI does on a
// successful provision (internal/ui/model.go's provisionDoneMsg handling,
// shared via internal/manage), so a headless-created VM is flagged managed
// and stays recreate-able just like one made through the TUI.
func TestHeadlessCreateRecordsManagedVM(t *testing.T) {
	reg := registry.NewEmpty()
	cfg := vm.CreateConfig{
		Name:     "claude",
		BaseName: "claude-base",
		GitName:  "Ada Lovelace",
		GitEmail: "ada@example.com",
		CPUs:     4,
		Memory:   "8GiB",
		Disk:     "100GiB",
	}

	err := doHeadlessCreate(context.Background(), reg, stubBaseDeleter{}, stubProvisioner{}, cfg, false, false, io.Discard)
	if err != nil {
		t.Fatalf("doHeadlessCreate: %v", err)
	}

	if !reg.IsManaged(cfg.Name) {
		t.Fatalf("headless create did not record %q as managed", cfg.Name)
	}
	got, ok := reg.Config(cfg.Name)
	if !ok {
		t.Fatalf("registry has no config recorded for %q", cfg.Name)
	}
	if got != cfg {
		t.Fatalf("recorded config = %+v, want %+v (round-trip mismatch)", got, cfg)
	}
}

// TestDoHeadlessCreate_RecordsCloneTokenAsGitHubSecret is the AC3
// (`--clone-token` reshape) test at the doHeadlessCreate seam: a headless
// `sand create --clone-url <github URL> --clone-token <T>` must record
// {scope: "github.com/<org>", token: T} in the VM's host secrets store
// BEFORE either the create or recreate provisioning path runs, so the very
// first finalize pass renders a working credential (see
// provision.RecordCloneTokenSecret, called from doHeadlessCreate).
func TestDoHeadlessCreate_RecordsCloneTokenAsGitHubSecret(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	reg := registry.NewEmpty()
	cfg := vm.CreateConfig{
		Name:       "claude",
		BaseName:   "claude-base",
		GitName:    "Ada Lovelace",
		GitEmail:   "ada@example.com",
		CPUs:       4,
		Memory:     "8GiB",
		Disk:       "100GiB",
		CloneURL:   "https://github.com/acme/repo.git",
		CloneToken: "ghp_supersecrettoken",
	}

	if err := doHeadlessCreate(context.Background(), reg, stubBaseDeleter{}, stubProvisioner{}, cfg, false, false, io.Discard); err != nil {
		t.Fatalf("doHeadlessCreate: %v", err)
	}

	store, err := secrets.Load(cfg.Name)
	if err != nil {
		t.Fatalf("secrets.Load: %v", err)
	}
	if len(store.GitHub) != 1 || store.GitHub[0] != (secrets.GitHubSecret{Scope: "github.com/acme", Token: "ghp_supersecrettoken"}) {
		t.Fatalf("store.GitHub = %+v, want a single {github.com/acme ghp_supersecrettoken} entry", store.GitHub)
	}

	// The registry's recorded config must still have the token stripped
	// (internal/registry.Add's existing secret-hygiene behavior) — the
	// clone token lives only in the secrets store, never the managed index.
	got, ok := reg.Config(cfg.Name)
	if !ok {
		t.Fatalf("registry has no config recorded for %q", cfg.Name)
	}
	if got.CloneToken != "" {
		t.Fatalf("registry config retained a clone token: %q", got.CloneToken)
	}
}

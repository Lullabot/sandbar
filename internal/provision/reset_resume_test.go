package provision

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
)

// A preserving reset records a manifest beside the archives while the guest is
// still intact, and it never contains the clone token.
func TestReset_WritesManifestWithoutTheToken(t *testing.T) {
	f := &fakeRunner{
		status:  map[string][]byte{"sandbar-base": []byte("Stopped\n")},
		failOn:  func(c []string) bool { return c[0] == "clone" },
		failErr: errors.New("no space left on device"),
	}
	t.Setenv("TMPDIR", t.TempDir())
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig()
	cfg.CloneToken = "ghp_supersecret"
	err := p.Reset(context.Background(), cfg, ResetOptions{PreserveAgents: true}, io.Discard)
	dir := stageRecoveryDir(err)
	if dir == "" {
		t.Fatalf("expected a kept staging dir, got %v", err)
	}
	if want := "sand reset claude --from-backup " + dir; !strings.Contains(err.Error(), want) {
		t.Errorf("the error does not say how to resume (want %q):\n%v", want, err)
	}
	data, rerr := os.ReadFile(filepath.Join(dir, resetManifestName))
	if rerr != nil {
		t.Fatalf("no manifest beside the archives: %v", rerr)
	}
	if strings.Contains(string(data), "ghp_supersecret") {
		t.Errorf("the manifest contains the clone token:\n%s", data)
	}
	var m ResetManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m.Config.Name != cfg.Name || !m.Plan.Agents {
		t.Errorf("manifest = %+v, want config %q with agents staged", m, cfg.Name)
	}
}

func writeBackup(t *testing.T, m *ResetManifest, archives ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, a := range archives {
		if err := os.WriteFile(filepath.Join(dir, a), []byte("archive"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if m != nil {
		data, _ := json.Marshal(m)
		if err := os.WriteFile(filepath.Join(dir, resetManifestName), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Resuming rebuilds the VM without staging or deleting anything, restores the
// archive before finalize, and removes the directory once everything is back.
func TestReset_RestoreFromRebuildsAndRestores(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig()
	cfg.CloneURL = "https://github.com/lullabot/sandbar"
	m := NewResetManifest(cfg, PreservePlan{WholeHome: true, Project: ProjectPlan{OrgRel: "lullabot", RestoresCheckout: true}}, ResetOptions{})
	dir := writeBackup(t, &m, homeArchive)

	if err := p.Reset(context.Background(), cfg, ResetOptions{RestoreFrom: dir}, io.Discard); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	for _, c := range f.calls {
		if c[0] == "delete" || isTarOut(c) {
			t.Fatalf("a resume must not stage or delete anything: %v", c)
		}
	}
	restore := findCall(t, f.calls, 0, "the home restore", isTarIn)
	finalize := findCall(t, f.calls, 0, "the finalize playbook", func(c []string) bool {
		return hasTok(c, "shell") && hasTok(c, "bash")
	})
	if restore > finalize {
		t.Errorf("home restored at call %d, after finalize at %d", restore, finalize)
	}
	if strings.Contains(finalizeStream(t, f.streams), "project_clone_url") {
		t.Error("finalize would clone over the restored checkout")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the backup directory should be removed after a full restore: %v", err)
	}
}

// A resume that fails again keeps the archives, exactly like the reset it is
// finishing.
func TestReset_RestoreFromKeepsTheBackupOnFailure(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	f := &fakeRunner{
		status:  map[string][]byte{"sandbar-base": []byte("Stopped\n")},
		failOn:  func(c []string) bool { return c[0] == "clone" },
		failErr: errors.New("no space left on device"),
	}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
	cfg := testConfig()
	m := NewResetManifest(cfg, PreservePlan{WholeHome: true}, ResetOptions{})
	dir := writeBackup(t, &m, homeArchive)

	err := p.Reset(context.Background(), cfg, ResetOptions{RestoreFrom: dir}, io.Discard)
	if err == nil {
		t.Fatal("expected the clone failure")
	}
	if got := stageRecoveryDir(err); got != dir {
		t.Errorf("error names %q, want the backup dir %q", got, dir)
	}
	if _, serr := os.Stat(filepath.Join(dir, homeArchive)); serr != nil {
		t.Errorf("the archive was removed by a failed resume: %v", serr)
	}
}

// A resume never overwrites a live VM.
func TestReset_RestoreFromRefusesAnExistingInstance(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	f := &fakeRunner{status: map[string][]byte{
		"sandbar-base": []byte("Stopped\n"),
		"claude":       []byte("Running\n"),
	}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
	cfg := testConfig()
	m := NewResetManifest(cfg, PreservePlan{WholeHome: true}, ResetOptions{})
	dir := writeBackup(t, &m, homeArchive)

	err := p.Reset(context.Background(), cfg, ResetOptions{RestoreFrom: dir}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("want an already-exists refusal, got %v", err)
	}
	for _, c := range f.calls {
		if c[0] == "delete" || c[0] == "clone" {
			t.Fatalf("a refused resume touched the host: %v", c)
		}
	}
	if _, serr := os.Stat(filepath.Join(dir, homeArchive)); serr != nil {
		t.Errorf("a refused resume removed the backup: %v", serr)
	}
}

func TestResumeStage(t *testing.T) {
	t.Run("legacy whole home", func(t *testing.T) {
		g, m, legacy, err := ResumeStage(writeBackup(t, nil, homeArchive))
		if err != nil || !legacy || !m.Plan.WholeHome || g == nil {
			t.Fatalf("got (%v, %+v, legacy=%v, %v)", g, m, legacy, err)
		}
	})
	t.Run("legacy agents", func(t *testing.T) {
		_, m, legacy, err := ResumeStage(writeBackup(t, nil, agentsArchive))
		if err != nil || !legacy || !m.Plan.Agents {
			t.Fatalf("got (%+v, legacy=%v, %v)", m, legacy, err)
		}
	})
	t.Run("legacy project archive refused", func(t *testing.T) {
		_, _, _, err := ResumeStage(writeBackup(t, nil, projectArchive))
		if err == nil || !strings.Contains(err.Error(), resetManifestName) {
			t.Fatalf("want a refusal naming the missing manifest, got %v", err)
		}
	})
	t.Run("empty directory", func(t *testing.T) {
		if _, _, _, err := ResumeStage(t.TempDir()); err == nil {
			t.Fatal("an empty directory has nothing to restore")
		}
	})
	t.Run("not a directory", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "x")
		_ = os.WriteFile(f, nil, 0o600)
		if _, _, _, err := ResumeStage(f); err == nil {
			t.Fatal("a file is not a backup directory")
		}
	})
	t.Run("missing", func(t *testing.T) {
		if _, _, _, err := ResumeStage(filepath.Join(t.TempDir(), "nope")); err == nil {
			t.Fatal("a missing directory must be an error")
		}
	})
	t.Run("newer schema", func(t *testing.T) {
		m := ResetManifest{Schema: resetManifestSchema + 1, Plan: PreservePlan{WholeHome: true}}
		_, _, _, err := ResumeStage(writeBackup(t, &m, homeArchive))
		if err == nil || !strings.Contains(err.Error(), "newer sand") {
			t.Fatalf("want a newer-schema refusal, got %v", err)
		}
	})
	t.Run("manifest names an archive that is gone", func(t *testing.T) {
		m := ResetManifest{Schema: resetManifestSchema, Plan: PreservePlan{Extras: []string{"src/app"}}}
		_, _, _, err := ResumeStage(writeBackup(t, &m, homeArchive))
		if err == nil || !strings.Contains(err.Error(), extrasArchive) {
			t.Fatalf("want a missing-archive error naming %s, got %v", extrasArchive, err)
		}
	})
	t.Run("corrupt manifest", func(t *testing.T) {
		dir := writeBackup(t, nil, homeArchive)
		_ = os.WriteFile(filepath.Join(dir, resetManifestName), []byte("{"), 0o600)
		if _, _, _, err := ResumeStage(dir); err == nil {
			t.Fatal("a corrupt manifest must not fall back to guessing")
		}
	})
}

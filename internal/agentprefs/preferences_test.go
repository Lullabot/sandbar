package agentprefs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/vm"
)

func TestMigrateLegacyAndRememberExplicitOptOut(t *testing.T) {
	for _, tc := range []struct {
		stamp         string
		claude, codex bool
	}{
		{"v2:hash:none", false, false}, {"v2:hash:codex+go", false, true}, {"v3:hash:none", false, false},
		{"v2:hash:codex:template-gen2", false, true},
	} {
		t.Run(tc.stamp, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv("LIMA_HOME", t.TempDir())
			dir := filepath.Join(os.Getenv("LIMA_HOME"), "_sand")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "base.playbook-version"), []byte(tc.stamp), 0600); err != nil {
				t.Fatal(err)
			}
			s, err := LoadOrMigrate(lima.LocalFiles(), "base")
			if err != nil {
				t.Fatal(err)
			}
			if s.Claude != tc.claude || s.Codex != tc.codex {
				t.Fatalf("migrated %+v", s)
			}
			if err := Save(Selection{}); err != nil {
				t.Fatal(err)
			}
			s, err = LoadOrMigrate(lima.LocalFiles(), "base")
			if err != nil {
				t.Fatal(err)
			}
			c := vm.DefaultCreateConfig()
			s.Apply(&c)
			if c.WithClaude || c.WithCodex || c.WithOpenCode || c.WithPi {
				t.Fatal("explicit opt out lost")
			}
		})
	}
}

type migrationReadFiles struct {
	lima.HostFiles
	read func(string) ([]byte, error)
}

func (f migrationReadFiles) ReadFile(path string) ([]byte, error) { return f.read(path) }

// A remote read may finish after another create has saved the user's answer.
// Inject that submission at the I/O boundary rather than relying on a sleep.
func TestMigrationDoesNotReplaceSelectionSavedDuringRemoteRead(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	want := Selection{OpenCode: true, Pi: true}
	hf := migrationReadFiles{HostFiles: lima.LocalFiles(), read: func(string) ([]byte, error) {
		if err := Save(want); err != nil {
			t.Fatal(err)
		}
		return []byte("v2:old:claude+codex+go"), nil
	}}
	got, err := LoadOrMigrate(hf, "base")
	if err != nil || got != want {
		t.Fatalf("migration returned %+v, %v; want submitted %+v", got, err, want)
	}
	stored, found, err := Load()
	if err != nil || !found || stored != want {
		t.Fatalf("migration replaced disk selection: %+v, found=%v, err=%v", stored, found, err)
	}
}

func TestMigrationLeavesUnreadablePreferencesIntact(t *testing.T) {
	for _, original := range []string{`{"version":`, `{"version":2,"selection":{"pi":true}}`} {
		t.Run(original, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			p, err := preferencesPath()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadOrMigrate(nil, "base"); err == nil {
				t.Fatal("unreadable preferences silently replaced by defaults")
			}
			b, err := os.ReadFile(p)
			if err != nil || string(b) != original {
				t.Fatalf("original configuration changed: %q, %v", b, err)
			}
		})
	}
}

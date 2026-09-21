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
		{"v2:hash:none", false, false}, {"v2:hash:codex+go", false, true}, {"v3:hash:none", true, false},
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

package registry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyAgentConfigSurvivesDiskMigration(t *testing.T) {
	p := filepath.Join(t.TempDir(), "managed-vms.json")
	legacy := `{"version":2,"vms":{"old":{"base":"sandbar-base","config":{"Name":"old","BaseName":"sandbar-base","WithClaude":false,"WithCodex":true,"WithGo":true}}}}`
	if err := os.WriteFile(p, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := LoadFrom(p)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := r.Config("old")
	if !ok || c.WithClaude || !c.WithCodex || c.WithOpenCode || c.WithPi || !c.WithGo {
		t.Fatalf("legacy selection changed: %+v found %v", c, ok)
	}
	c.WithOpenCode = true
	c.WithPi = true
	if err := r.Add(c); err != nil {
		t.Fatal(err)
	}
	r, err = LoadFrom(p)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := r.Config("old")
	if !ok || got != c {
		t.Fatalf("roundtrip changed selection: %+v", got)
	}
}

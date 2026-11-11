package provision

import (
	"os"
	"path/filepath"
	"testing"
)

// The profile the reset ran on reaches the manifest, so a resume can find it.
func TestNewResetManifest_RecordsTheProfile(t *testing.T) {
	m := NewResetManifest(testConfig(), PreservePlan{}, ResetOptions{Profile: ProfileRef{ID: "p-123", Name: "mini"}})
	if m.ProfileID != "p-123" || m.ProfileName != "mini" {
		t.Errorf("manifest profile = %q/%q, want p-123/mini", m.ProfileID, m.ProfileName)
	}
	dir := writeBackup(t, &m, homeArchive)
	back, ok, err := ReadResetManifest(dir)
	if err != nil || !ok || back.ProfileID != "p-123" {
		t.Errorf("ReadResetManifest = %+v, %v, %v", back, ok, err)
	}
	if _, ok, err := ReadResetManifest(t.TempDir()); ok || err != nil {
		t.Errorf("a directory with no manifest read as ok=%v err=%v", ok, err)
	}
}

// A corrupt or unreadable manifest is an error, never "no profile recorded".
func TestReadResetManifestRejectsCorruption(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, resetManifestName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadResetManifest(dir); err == nil {
		t.Error("ReadResetManifest accepted a corrupt reset.json")
	}
	notADir := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadResetManifest(notADir); err == nil {
		t.Error("ReadResetManifest treated an unreadable directory as having no manifest")
	}
}

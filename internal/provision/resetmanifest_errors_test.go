package provision

import (
	"os"
	"path/filepath"
	"testing"
)

// A corrupt rebuild record is an error, never an empty answer: an empty
// RebuildID would read as "this reset never cloned".
func TestRebuildRecordRejectsCorruption(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, rebuildRecordName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RebuildID(dir); err == nil {
		t.Error("RebuildID accepted a corrupt rebuild.json")
	}
	// A path that cannot be read as a directory is an error too, not "no
	// record".
	notADir := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RebuildID(notADir); err == nil {
		t.Error("RebuildID treated an unreadable directory as having no record")
	}
}

// StagedResetError without a VM name still names the backup, and unwraps.
func TestStagedResetErrorWithoutAName(t *testing.T) {
	cause := os.ErrPermission
	e := &StagedResetError{Dir: "/tmp/x", Err: cause}
	if got := e.Error(); got != "reset failed after staging; your data is preserved at /tmp/x: "+cause.Error() {
		t.Errorf("Error() = %q", got)
	}
	if e.Unwrap() != cause {
		t.Error("Unwrap lost the cause")
	}
}

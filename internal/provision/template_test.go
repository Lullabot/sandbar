package provision

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
)

// stubVersionSeams overrides the three package-level version-stamp seams for the
// duration of a test (see baseversion.go), restoring the originals on cleanup —
// the same pattern provision_test.go and baseversion_test.go already use to keep
// SnapshotTemplate's stamping deterministic without touching a real playbook
// checkout.
func stubVersionSeams(t *testing.T, version string, writes map[string]string) {
	t.Helper()
	origVer, origRead, origWrite := playbookVersionFn, readBaseVersionFn, writeBaseVersionFn
	playbookVersionFn = func(string, string) (string, error) { return version, nil }
	readBaseVersionFn = func(lima.HostFiles, string) string { return "" } // source carries no stamp of its own
	writeBaseVersionFn = func(_ lima.HostFiles, name, v string, _ time.Time) error {
		if writes != nil {
			writes[name] = v
		}
		return nil
	}
	t.Cleanup(func() { playbookVersionFn, readBaseVersionFn, writeBaseVersionFn = origVer, origRead, origWrite })
}

// callIndex returns the index of the first recorded call whose argv starts with
// the given tokens, or -1 if none matches.
func callIndex(calls [][]string, tokens ...string) int {
	for i, c := range calls {
		if len(c) < len(tokens) {
			continue
		}
		match := true
		for j, tok := range tokens {
			if c[j] != tok {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// TestSnapshotTemplate_RunningSource is the first leg of the power-state matrix:
// a running source must be stopped before the clone and restarted afterwards, in
// that exact order, and the template instance must come away stamped with the
// captured playbook version and tool-set.
func TestSnapshotTemplate_RunningSource(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"claude": []byte("Running\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	writes := map[string]string{}
	stubVersionSeams(t, "v2:abc123:go", writes)

	res, err := p.SnapshotTemplate(context.Background(), "claude", "sandbar-tmpl-golden", io.Discard)
	if err != nil {
		t.Fatalf("SnapshotTemplate: %v", err)
	}
	if res.PlaybookVersion != "v2:abc123:go" {
		t.Errorf("PlaybookVersion = %q, want %q", res.PlaybookVersion, "v2:abc123:go")
	}
	if res.ToolsetKey != "go" {
		t.Errorf("ToolsetKey = %q, want %q", res.ToolsetKey, "go")
	}
	if writes["sandbar-tmpl-golden"] != "v2:abc123:go" {
		t.Errorf("template instance stamp not written; writes=%v", writes)
	}

	calls := f.snapshot()
	idxStop := callIndex(calls, "stop", "claude")
	idxClone := callIndex(calls, "clone", "claude", "sandbar-tmpl-golden")
	idxStart := callIndex(calls, "start", "claude")
	if idxStop == -1 || idxClone == -1 || idxStart == -1 {
		t.Fatalf("expected stop, clone, and start all recorded; got %v", calls)
	}
	if !(idxStop < idxClone && idxClone < idxStart) {
		t.Errorf("expected stop -> clone -> start order; got stop=%d clone=%d start=%d (calls=%v)", idxStop, idxClone, idxStart, calls)
	}
}

// TestSnapshotTemplate_StoppedSource is the second leg: an already-stopped
// source must be left alone (no stop, no start) while the clone still happens.
func TestSnapshotTemplate_StoppedSource(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"claude": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	stubVersionSeams(t, "v2:abc123:go", nil)

	if _, err := p.SnapshotTemplate(context.Background(), "claude", "sandbar-tmpl-golden", io.Discard); err != nil {
		t.Fatalf("SnapshotTemplate: %v", err)
	}

	calls := f.snapshot()
	if idx := callIndex(calls, "stop", "claude"); idx != -1 {
		t.Errorf("an already-stopped source must not be stopped; recorded at %d: %v", idx, calls)
	}
	if idx := callIndex(calls, "start", "claude"); idx != -1 {
		t.Errorf("an already-stopped source must not be started; recorded at %d: %v", idx, calls)
	}
	if callIndex(calls, "clone", "claude", "sandbar-tmpl-golden") == -1 {
		t.Fatalf("expected a clone call; got %v", calls)
	}
}

// TestSnapshotTemplate_CloneFailureRestoresAndCleansUp is the third leg: a
// failed clone must still restore a running source to running, must clean up
// the partial template instance (a Delete, mirroring the create path's own
// failed-clone cleanup), and must NOT stamp the template.
func TestSnapshotTemplate_CloneFailureRestoresAndCleansUp(t *testing.T) {
	f := &fakeRunner{
		status: map[string][]byte{"claude": []byte("Running\n")},
		failOn: func(args []string) bool { return len(args) > 0 && args[0] == "clone" },
	}
	var log []string
	hf := &fakeHostFiles{id: "h", home: t.TempDir(), log: &log}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook", HostFiles: hf}

	wroteStamp := false
	origVer, origRead, origWrite := playbookVersionFn, readBaseVersionFn, writeBaseVersionFn
	playbookVersionFn = func(string, string) (string, error) { return "v2:abc123:go", nil }
	readBaseVersionFn = func(lima.HostFiles, string) string { return "" }
	writeBaseVersionFn = func(lima.HostFiles, string, string, time.Time) error {
		wroteStamp = true
		return nil
	}
	t.Cleanup(func() { playbookVersionFn, readBaseVersionFn, writeBaseVersionFn = origVer, origRead, origWrite })

	_, err := p.SnapshotTemplate(context.Background(), "claude", "sandbar-tmpl-golden", io.Discard)
	if err == nil {
		t.Fatal("expected an error from a failed clone")
	}
	if wroteStamp {
		t.Error("the template must not be stamped when the clone itself failed")
	}

	calls := f.snapshot()
	if callIndex(calls, "stop", "claude") == -1 {
		t.Errorf("expected the running source to be stopped before the (failed) clone; got %v", calls)
	}
	if callIndex(calls, "start", "claude") == -1 {
		t.Errorf("expected the running source to be restarted despite the clone failure; got %v", calls)
	}
	if callIndex(calls, "delete", "sandbar-tmpl-golden") == -1 {
		t.Errorf("expected cleanup to delete the partial template instance; got %v", calls)
	}
}

// TestDeleteTemplate proves the locked delete reaches limactl with force.
func TestDeleteTemplate(t *testing.T) {
	f := &fakeRunner{}
	p := &Provisioner{Lima: lima.New(f)}

	if err := p.DeleteTemplate(context.Background(), "sandbar-tmpl-golden", io.Discard); err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}
	calls := f.snapshot()
	if callIndex(calls, "delete", "sandbar-tmpl-golden", "-f") == -1 {
		t.Errorf("expected a forced delete call; got %v", calls)
	}
}

// TestTemplateDiskBytes proves the disk-size lookup joins the instance's `disk`
// file under this provisioner's own host-access handle, mirroring
// internal/ui/diskusage.go's diskUsedBytes join.
func TestTemplateDiskBytes(t *testing.T) {
	var log []string
	hf := &fakeHostFiles{id: "h", home: "/lima-home", log: &log}
	p := &Provisioner{Lima: lima.New(&fakeRunner{}), HostFiles: hf}

	got := p.TemplateDiskBytes("sandbar-tmpl-golden")
	if got != -1 {
		t.Errorf("TemplateDiskBytes = %d, want -1 (fakeHostFiles.DiskAllocBytes's canned value)", got)
	}
	want := "h:DiskAllocBytes:/lima-home/sandbar-tmpl-golden/disk"
	found := false
	for _, entry := range log {
		if entry == want {
			found = true
		}
	}
	if !found {
		t.Errorf("expected log entry %q, got %v", want, log)
	}
}

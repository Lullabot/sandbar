package provision

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
)

// keptDirRe pulls the preserved-logs directory back out of the streamed output.
// The path is how the user finds the logs, so a test that did not read it from
// the message the user reads would not be testing the feature.
var keptDirRe = regexp.MustCompile(`in (\S+) —`)

// failingCleanupProvisioner wires a Provisioner whose limactl delete always
// fails, so cleanupInstance takes the RemoveAll fallback — the path that
// destroys the directory outright, and the one this behaviour exists for.
func failingCleanupProvisioner(hf lima.HostFiles) *Provisioner {
	return &Provisioner{
		Lima:      lima.New(&fakeRunner{err: errors.New("delete unsupported by this fake")}),
		HostFiles: hf,
	}
}

// TestCleanupInstance_PreservesLogsBeforeRemoval is the acceptance test for the
// evidence outliving the directory: a create fails, cleanup removes the instance
// directory Lima's own FATA line points into, and the logs it pointed at are
// still readable afterwards at a path the user was told.
func TestCleanupInstance_PreservesLogsBeforeRemoval(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()) // keep the preserved copy inside the test's sandbox

	var log []string
	dir := filepath.Join("/home/one/.lima", "web")
	hf := &fakeHostFiles{id: "host", home: "/home/one/.lima", log: &log, files: map[string][]byte{
		filepath.Join(dir, "ha.stderr.log"): []byte("fatal: host agent gave up\n"),
		filepath.Join(dir, "serial.log"):    []byte("BdsDxe: failed to load Boot0001\n"),
	}}

	var out bytes.Buffer
	failingCleanupProvisioner(hf).cleanupInstance("web", &out)

	m := keptDirRe.FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("cleanup never reported where it kept the logs; output was:\n%s", out.String())
	}
	kept := m[1]

	for name, want := range map[string]string{
		"ha.stderr.log": "fatal: host agent gave up\n",
		"serial.log":    "BdsDxe: failed to load Boot0001\n",
	} {
		got, err := os.ReadFile(filepath.Join(kept, name))
		if err != nil {
			t.Errorf("%s was not preserved: %v", name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q, want the original bytes %q", name, got, want)
		}
	}

	// A log absent from the instance directory must not produce an empty file.
	if _, err := os.Stat(filepath.Join(kept, "ha.stdout.log")); err == nil {
		t.Error("preserved a file Lima never wrote")
	}

	// Ordering is the whole point: reading after the removal reads nothing.
	firstRemove, lastRead := -1, -1
	for i, entry := range log {
		if strings.HasPrefix(entry, "host:RemoveAll:") && firstRemove < 0 {
			firstRemove = i
		}
		if strings.Contains(entry, ":ReadFile:") && strings.Contains(entry, ".log") {
			lastRead = i
		}
	}
	if firstRemove < 0 {
		t.Fatalf("cleanup never removed the instance directory: %v", log)
	}
	if lastRead < 0 || lastRead > firstRemove {
		t.Errorf("logs were not read before RemoveAll: %v", log)
	}
}

// TestCleanupInstance_NoLogsKeepsQuiet pins the common case — a cleanup so early
// that Lima wrote nothing — leaving neither a message pointing at an empty
// directory nor the empty directory itself.
func TestCleanupInstance_NoLogsKeepsQuiet(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	var log []string
	hf := &fakeHostFiles{id: "host", home: "/home/one/.lima", log: &log}

	var out bytes.Buffer
	failingCleanupProvisioner(hf).cleanupInstance("web", &out)

	if strings.Contains(out.String(), "Kept") {
		t.Errorf("claimed to keep logs when there were none:\n%s", out.String())
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("left %d empty temporary director(ies) behind: %v", len(entries), entries)
	}
}

// TestTailBytes covers the cap that keeps a boot-looping VM's serial log from
// being copied wholesale: under it, a byte-for-byte copy; over it, an announced
// tail starting on a line boundary.
func TestTailBytes(t *testing.T) {
	small := []byte("one\ntwo\n")
	if got := tailBytes(small, 1024); !bytes.Equal(got, small) {
		t.Errorf("a log under the cap was altered: %q", got)
	}

	big := []byte(strings.Repeat("boot loop\n", 200)) // 2000 bytes
	got := tailBytes(big, 100)
	if len(got) > 100+len("[sand: truncated — this is the last ~100 of 2000 bytes]\n") {
		t.Errorf("tail is longer than the cap plus its header: %d bytes", len(got))
	}
	if !bytes.HasPrefix(got, []byte("[sand: truncated")) {
		t.Errorf("truncation was not announced: %q", got)
	}
	body := got[bytes.IndexByte(got, '\n')+1:]
	if !bytes.HasPrefix(body, []byte("boot loop\n")) {
		t.Errorf("tail does not start on a line boundary: %q", body)
	}
	if !bytes.HasSuffix(got, []byte("boot loop\n")) {
		t.Errorf("tail is not the END of the log: %q", got)
	}
}

// TestTempNameSuffix pins that an unusual instance name costs the preserved logs
// their label and nothing more — os.MkdirTemp rejects a pattern containing a
// path separator, so an unsanitised name would cost the logs entirely.
func TestTempNameSuffix(t *testing.T) {
	for name, want := range map[string]string{
		"web":        "web",
		"my-vm_2.0":  "my-vm_2.0",
		"a/../b":     "a-..-b", // dots are kept; only the separators go
		"":           "vm",
		"sp ce":      "sp-ce",
		string('\n'): "-",
	} {
		if got := tempNameSuffix(name); got != want {
			t.Errorf("tempNameSuffix(%q) = %q, want %q", name, got, want)
		}
		if strings.ContainsRune(tempNameSuffix(name), os.PathSeparator) {
			t.Errorf("tempNameSuffix(%q) still contains a path separator", name)
		}
	}
}

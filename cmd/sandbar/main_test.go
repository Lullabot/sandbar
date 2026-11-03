package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the actual symlink and process boundary: version output must remain
// usable by scripts while the legacy invocation warns separately on stderr.
func TestLegacySymlink(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "sandbar")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	legacy := filepath.Join(dir, "sand")
	if err := os.Symlink("sandbar", legacy); err != nil {
		t.Fatal(err)
	}
	var versionOutput string
	for _, path := range []string{binary, legacy} {
		cmd := exec.Command(path, "version")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v: %s", path, err, stderr.String())
		}
		if path == binary {
			versionOutput = string(out)
			if stderr.Len() != 0 {
				t.Fatalf("sandbar warned: %s", stderr.String())
			}
		} else {
			if string(out) != versionOutput {
				t.Fatalf("legacy version output = %q, want %q", out, versionOutput)
			}
			if !strings.Contains(stderr.String(), "use sandbar instead") {
				t.Fatalf("missing legacy warning: %s", stderr.String())
			}
		}
	}
}

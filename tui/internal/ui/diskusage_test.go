package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// diskUsedBytes reports the allocated on-disk size of <dir>/disk, or -1 when it
// can't be measured: an empty dir arg, or a dir with no `disk` file. A present
// file reports a positive allocated size. Measuring allocated blocks is
// unix-specific, so the positive assertion is guarded to linux/darwin.
func TestDiskUsedBytes(t *testing.T) {
	dir := t.TempDir()

	// No `disk` file in the dir → unmeasurable → -1 (so the cell renders blank).
	if got := diskUsedBytes(dir); got != -1 {
		t.Fatalf("missing disk file: got %d, want -1", got)
	}

	// A 1 MiB `disk` file should report a positive allocated size.
	f := filepath.Join(dir, "disk")
	if err := os.WriteFile(f, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		if got := diskUsedBytes(dir); got <= 0 {
			t.Fatalf("present disk file: got %d, want > 0", got)
		}
	}

	// An empty dir argument is always unmeasurable → -1.
	if got := diskUsedBytes(""); got != -1 {
		t.Fatalf("empty dir arg: got %d, want -1", got)
	}
}

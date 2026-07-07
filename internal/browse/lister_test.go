package browse

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
)

// TestLocalListerMapsEntries checks the os.ReadDir -> DirEntry mapping against a
// real temp directory: names, the IsDir flag, and the file size.
func TestLocalListerMapsEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("seed subdir: %v", err)
	}

	entries, err := NewLocalLister().List(context.Background(), dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
	}
	byName := map[string]DirEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	if sub, ok := byName["sub"]; !ok || !sub.IsDir {
		t.Errorf("sub = %+v, want a directory", sub)
	}
	if f, ok := byName["file.txt"]; !ok || f.IsDir || f.Size != 5 {
		t.Errorf("file.txt = %+v, want a 5-byte file", f)
	}
}

// fakeRunner is a lima.Runner whose Stream writes canned find output, so the
// guest lister can be exercised without a real VM.
type fakeRunner struct{ out []byte }

func (fakeRunner) Output(context.Context, ...string) ([]byte, error) { return nil, nil }
func (f fakeRunner) Stream(_ context.Context, _ io.Reader, w io.Writer, _ ...string) error {
	_, err := w.Write(f.out)
	return err
}

// TestGuestListerParsesFindOutput checks the tab-separated find -printf parsing:
// type letter -> IsDir, size, and name.
func TestGuestListerParsesFindOutput(t *testing.T) {
	cli := lima.New(fakeRunner{out: []byte("d\t4096\tsrc\nf\t12\tfile.txt\n")})
	entries, err := NewGuestLister(cli, "vm1").List(context.Background(), "/home/u")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
	}
	if got := entries[0]; got.Name != "src" || !got.IsDir || got.Size != 4096 {
		t.Errorf("entries[0] = %+v, want {src dir 4096}", got)
	}
	if got := entries[1]; got.Name != "file.txt" || got.IsDir || got.Size != 12 {
		t.Errorf("entries[1] = %+v, want {file.txt file 12}", got)
	}
}

// TestGuestListerSkipsMalformedLines ensures a short/garbled line is dropped
// rather than aborting the listing.
func TestGuestListerSkipsMalformedLines(t *testing.T) {
	cli := lima.New(fakeRunner{out: []byte("d\t4096\tsrc\ngarbage-line\nf\t3\ta.txt\n")})
	entries, err := NewGuestLister(cli, "vm1").List(context.Background(), "/x")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("malformed line should be skipped: got %d entries %+v", len(entries), entries)
	}
}

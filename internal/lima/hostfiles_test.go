package lima

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestLocalFilesProvenanceMarkerRoundTrip proves MarkerPath + WriteFile/ReadFile
// round-trip a marker through localFiles exactly as the provider package's
// Provenancer implementation will drive them, and that RemoveAll (the unmark
// primitive) makes ReadFile report fs.ErrNotExist again.
func TestLocalFilesProvenanceMarkerRoundTrip(t *testing.T) {
	home := t.TempDir()
	hf := LocalFiles()
	t.Setenv("LIMA_HOME", home)

	path := MarkerPath(hf, "web")
	wantPath := filepath.Join(home, "web", "sandbar.json")
	if path != wantPath {
		t.Fatalf("MarkerPath = %q, want %q", path, wantPath)
	}

	data := []byte(`{"schema":1,"base":"base"}`)
	if err := hf.WriteFile(path, data, 0o700, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := hf.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("ReadFile = %q, want %q", got, data)
	}

	if err := hf.RemoveAll(filepath.Dir(path)); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := hf.ReadFile(path); !os.IsNotExist(err) {
		t.Fatalf("ReadFile after RemoveAll err = %v, want fs.ErrNotExist", err)
	}
}

// TestLocalFilesReadInstanceMarkersProvenance covers the batched read's
// directory-walk logic directly: it must return every instance directory's
// marker in one map, skip a directory that has none, skip a non-directory
// entry sitting at the top level, and — for a completely missing limaHome —
// return an empty map rather than an error.
func TestLocalFilesReadInstanceMarkersProvenance(t *testing.T) {
	hf := LocalFiles()

	t.Run("multiple markers, one missing, one non-dir entry", func(t *testing.T) {
		home := t.TempDir()
		mustWriteMarker := func(name, content string) {
			t.Helper()
			dir := filepath.Join(home, name)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatalf("mkdir %s: %v", name, err)
			}
			if err := os.WriteFile(filepath.Join(dir, "sandbar.json"), []byte(content), 0o600); err != nil {
				t.Fatalf("write marker %s: %v", name, err)
			}
		}
		mustWriteMarker("web", `{"schema":1,"base":"web-base"}`)
		mustWriteMarker("api", `{"schema":1,"base":"api-base"}`)
		// An instance dir with no marker at all.
		if err := os.MkdirAll(filepath.Join(home, "bare"), 0o700); err != nil {
			t.Fatalf("mkdir bare: %v", err)
		}
		// A stray non-directory entry at the top level — must not be treated as
		// an instance.
		if err := os.WriteFile(filepath.Join(home, "README"), []byte("not an instance"), 0o600); err != nil {
			t.Fatalf("write README: %v", err)
		}

		got, err := hf.ReadInstanceMarkers(context.Background(), home, MarkerFilename)
		if err != nil {
			t.Fatalf("ReadInstanceMarkers: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("ReadInstanceMarkers returned %d entries, want 2: %v", len(got), got)
		}
		if string(got["web"]) != `{"schema":1,"base":"web-base"}` {
			t.Fatalf("got[web] = %q", got["web"])
		}
		if string(got["api"]) != `{"schema":1,"base":"api-base"}` {
			t.Fatalf("got[api] = %q", got["api"])
		}
		if _, present := got["bare"]; present {
			t.Fatalf("ReadInstanceMarkers included a dir with no marker: %v", got)
		}
		if _, present := got["README"]; present {
			t.Fatalf("ReadInstanceMarkers included a non-directory top-level entry: %v", got)
		}
	})

	t.Run("missing limaHome is an empty map, not an error", func(t *testing.T) {
		got, err := hf.ReadInstanceMarkers(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"), MarkerFilename)
		if err != nil {
			t.Fatalf("ReadInstanceMarkers(missing home): %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("ReadInstanceMarkers(missing home) = %v, want empty", got)
		}
	})
}

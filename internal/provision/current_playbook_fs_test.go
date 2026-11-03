package provision

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	sandbar "github.com/lullabot/sandbar"
)

func TestCurrentPlaybookFSReadsCheckoutWithoutExtraction(t *testing.T) {
	checkout, err := gitCheckoutPlaybookDir()
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	t.Setenv("TMPDIR", cache)
	got, err := fs.ReadFile(CurrentPlaybookFS(), "site.yml")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(checkout, "site.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("current checkout playbook did not match the file applied to guests")
	}
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 0 {
		t.Fatalf("read-only checkout revision check extracted files: entries=%v err=%v", entries, err)
	}
}

func TestCurrentPlaybookFSReadsEmbeddedOutsideCheckoutWithoutExtraction(t *testing.T) {
	outside := t.TempDir()
	cache := t.TempDir()
	t.Chdir(outside)
	t.Setenv("TMPDIR", cache)
	got, err := fs.ReadFile(CurrentPlaybookFS(), "site.yml")
	if err != nil {
		t.Fatal(err)
	}
	want, err := fs.ReadFile(sandbar.PlaybookFS, "site.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("packaged revision source differed from embedded playbook")
	}
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 0 {
		t.Fatalf("read-only embedded revision check extracted files: entries=%v err=%v", entries, err)
	}
}

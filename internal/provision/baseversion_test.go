package provision

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// samplePlaybookFS returns a minimal fstest.MapFS that mirrors the real
// playbook fileset (site.yml, ansible.cfg, inventory, roles/**, group_vars/**)
// plus, when includeExtra is true, a file outside that fileset — exercising
// the same shape TestGuestSyncCopiesOnlyThePlaybook pins against the real
// go:embed directives and rsync filter.
func samplePlaybookFS(includeExtra bool) fstest.MapFS {
	fs := fstest.MapFS{
		"site.yml":                  &fstest.MapFile{Data: []byte("- hosts: all\n")},
		"ansible.cfg":               &fstest.MapFile{Data: []byte("[defaults]\n")},
		"inventory":                 &fstest.MapFile{Data: []byte("localhost\n")},
		"roles/base/tasks/main.yml": &fstest.MapFile{Data: []byte("- name: a\n")},
		"group_vars/all.yml":        &fstest.MapFile{Data: []byte("foo: bar\n")},
	}
	if includeExtra {
		// Outside the fileset entirely: a top-level file (e.g. a README a repo
		// checkout would have) that the rsync filter and go:embed directives both
		// exclude.
		fs["README.md"] = &fstest.MapFile{Data: []byte("not part of the playbook\n")}
	}
	return fs
}

func TestPlaybookContentHash_SameFilesSameHash(t *testing.T) {
	a, err := playbookContentHash(samplePlaybookFS(false))
	if err != nil {
		t.Fatalf("hash a: %v", err)
	}
	b, err := playbookContentHash(samplePlaybookFS(false))
	if err != nil {
		t.Fatalf("hash b: %v", err)
	}
	if a != b {
		t.Errorf("identical filesets hashed differently: %q vs %q", a, b)
	}
}

func TestPlaybookContentHash_ChangedContentDifferentHash(t *testing.T) {
	base := samplePlaybookFS(false)
	changed := samplePlaybookFS(false)
	changed["site.yml"] = &fstest.MapFile{Data: []byte("- hosts: all\n- extra: true\n")}

	a, err := playbookContentHash(base)
	if err != nil {
		t.Fatalf("hash base: %v", err)
	}
	b, err := playbookContentHash(changed)
	if err != nil {
		t.Fatalf("hash changed: %v", err)
	}
	if a == b {
		t.Errorf("changing site.yml content did not change the hash (%q)", a)
	}
}

func TestPlaybookContentHash_RenamedFileDifferentHash(t *testing.T) {
	base := samplePlaybookFS(false)
	renamed := samplePlaybookFS(false)
	// Same bytes, different path: rename roles/base/tasks/main.yml to
	// roles/base/tasks/setup.yml. The path+length framing must still detect
	// this even though the total byte content across the fileset is identical.
	data := renamed["roles/base/tasks/main.yml"].Data
	delete(renamed, "roles/base/tasks/main.yml")
	renamed["roles/base/tasks/setup.yml"] = &fstest.MapFile{Data: data}

	a, err := playbookContentHash(base)
	if err != nil {
		t.Fatalf("hash base: %v", err)
	}
	b, err := playbookContentHash(renamed)
	if err != nil {
		t.Fatalf("hash renamed: %v", err)
	}
	if a == b {
		t.Errorf("renaming a file did not change the hash (%q)", a)
	}
}

func TestPlaybookContentHash_UnrelatedFileOutsideFilesetSameHash(t *testing.T) {
	without, err := playbookContentHash(samplePlaybookFS(false))
	if err != nil {
		t.Fatalf("hash without extra: %v", err)
	}
	with, err := playbookContentHash(samplePlaybookFS(true))
	if err != nil {
		t.Fatalf("hash with extra: %v", err)
	}
	if without != with {
		t.Errorf("a file outside the playbook fileset changed the hash: %q vs %q", without, with)
	}
}

func TestPlaybookVersion_DifferentToolsetDifferentVersion(t *testing.T) {
	fsys := samplePlaybookFS(false)
	a, err := PlaybookVersion(fsys, "ddev+go+java")
	if err != nil {
		t.Fatalf("PlaybookVersion a: %v", err)
	}
	b, err := PlaybookVersion(fsys, "go")
	if err != nil {
		t.Fatalf("PlaybookVersion b: %v", err)
	}
	if a == b {
		t.Errorf("different toolset strings produced the same version: %q", a)
	}
}

func TestPlaybookVersion_HasV2Prefix(t *testing.T) {
	v, err := PlaybookVersion(samplePlaybookFS(false), "ddev+go+java")
	if err != nil {
		t.Fatalf("PlaybookVersion: %v", err)
	}
	if !strings.HasPrefix(v, playbookVersionPrefix) {
		t.Errorf("version %q does not have the %q prefix", v, playbookVersionPrefix)
	}
}

// writePlaybookFiles materializes the real-shaped playbook fileset (plus, when
// includeExtra is set, a file outside it — mimicking stray checkout content
// such as .git or Go sources) under dir.
func writePlaybookFiles(t *testing.T, dir string, includeExtra bool) {
	t.Helper()
	files := map[string]string{
		"site.yml":                  "- hosts: all\n",
		"ansible.cfg":               "[defaults]\n",
		"inventory":                 "localhost\n",
		"roles/base/tasks/main.yml": "- name: a\n",
		"group_vars/all.yml":        "foo: bar\n",
	}
	if includeExtra {
		files["README.md"] = "not part of the playbook\n"
	}
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

// TestContentPlaybookVersion_NonGitDirSucceeds is the whole point of this
// task: the old git-HEAD scheme errored outside a git checkout, which
// baseStale then silently treated as "not stale" — so a released/Homebrew
// binary never rebuilt its base and never wrote a stamp at all. The
// content-hash scheme must succeed here.
func TestContentPlaybookVersion_NonGitDirSucceeds(t *testing.T) {
	dir := t.TempDir() // plain temp dir, deliberately not a git checkout
	writePlaybookFiles(t, dir, false)

	v, err := contentPlaybookVersion(dir)
	if err != nil {
		t.Fatalf("contentPlaybookVersion on a non-git dir returned an error: %v", err)
	}
	if v == "" {
		t.Fatal("contentPlaybookVersion on a non-git dir returned an empty stamp")
	}
	if !strings.HasPrefix(v, playbookVersionPrefix) {
		t.Errorf("stamp %q missing %q prefix", v, playbookVersionPrefix)
	}
}

// TestContentPlaybookVersion_UnrelatedFileUnchanged models "a commit that
// touches no playbook file does not change the stamp" — the property the old
// git-HEAD scheme got wrong (any commit, anywhere in the repo, changed HEAD).
func TestContentPlaybookVersion_UnrelatedFileUnchanged(t *testing.T) {
	dir := t.TempDir()
	writePlaybookFiles(t, dir, false)
	before, err := contentPlaybookVersion(dir)
	if err != nil {
		t.Fatalf("contentPlaybookVersion before: %v", err)
	}

	// Simulate a commit that only touches a non-playbook file.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("docs change\n"), 0o644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}

	after, err := contentPlaybookVersion(dir)
	if err != nil {
		t.Fatalf("contentPlaybookVersion after: %v", err)
	}
	if before != after {
		t.Errorf("a change outside the playbook fileset changed the stamp: %q vs %q", before, after)
	}
}

// TestContentPlaybookVersion_PlaybookChangeChangesStamp is the converse of the
// above: editing a file that IS in the playbook fileset must change the
// stamp.
func TestContentPlaybookVersion_PlaybookChangeChangesStamp(t *testing.T) {
	dir := t.TempDir()
	writePlaybookFiles(t, dir, false)
	before, err := contentPlaybookVersion(dir)
	if err != nil {
		t.Fatalf("contentPlaybookVersion before: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "site.yml"), []byte("- hosts: all\n- changed: true\n"), 0o644); err != nil {
		t.Fatalf("write site.yml: %v", err)
	}

	after, err := contentPlaybookVersion(dir)
	if err != nil {
		t.Fatalf("contentPlaybookVersion after: %v", err)
	}
	if before == after {
		t.Errorf("editing a playbook file did not change the stamp (%q)", before)
	}
}

// TestBaseStale_OldFormatGitStampIsStale asserts an upgrading user converges:
// a stamp in the old git-HEAD format (a bare 40-hex commit SHA, no "v2:"
// prefix) must be treated as stale even though it is non-empty and even
// though it happens not to equal the freshly computed content-hash stamp for
// an unrelated reason — the point is that the *format* alone is disqualifying.
func TestBaseStale_OldFormatGitStampIsStale(t *testing.T) {
	origVer, origRead := playbookVersionFn, readBaseVersionFn
	playbookVersionFn = func(string) (string, error) { return "v2:deadbeef:ddev+go+java", nil }
	readBaseVersionFn = func(string) string { return "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2" } // v1-style 40-hex git SHA
	t.Cleanup(func() { playbookVersionFn, readBaseVersionFn = origVer, origRead })

	p := &Provisioner{PlaybookDir: "/playbook"}
	if _, stale := p.baseStale("claude-base", io.Discard); !stale {
		t.Fatal("a v1-style (bare git-hash) stamp must be treated as stale")
	}
}

// TestBaseStale_EmptyStampIsStale preserves today's behaviour: an
// unreadable/absent stamp counts as stale.
func TestBaseStale_EmptyStampIsStale(t *testing.T) {
	origVer, origRead := playbookVersionFn, readBaseVersionFn
	playbookVersionFn = func(string) (string, error) { return "v2:deadbeef:ddev+go+java", nil }
	readBaseVersionFn = func(string) string { return "" }
	t.Cleanup(func() { playbookVersionFn, readBaseVersionFn = origVer, origRead })

	p := &Provisioner{PlaybookDir: "/playbook"}
	if _, stale := p.baseStale("claude-base", io.Discard); !stale {
		t.Fatal("an empty stamp must be treated as stale")
	}
}

// TestBaseStale_MatchingV2StampNotStale is the happy path: a v2-format stamp
// that matches the current computed version is not stale.
func TestBaseStale_MatchingV2StampNotStale(t *testing.T) {
	origVer, origRead := playbookVersionFn, readBaseVersionFn
	playbookVersionFn = func(string) (string, error) { return "v2:deadbeef:ddev+go+java", nil }
	readBaseVersionFn = func(string) string { return "v2:deadbeef:ddev+go+java" }
	t.Cleanup(func() { playbookVersionFn, readBaseVersionFn = origVer, origRead })

	p := &Provisioner{PlaybookDir: "/playbook"}
	if _, stale := p.baseStale("claude-base", io.Discard); stale {
		t.Fatal("a matching v2 stamp must not be treated as stale")
	}
}

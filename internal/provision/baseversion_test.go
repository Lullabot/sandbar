package provision

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lullabot/sandbar/internal/vm"
)

// allToolsConfig is a vm.CreateConfig with BaseName set and the default
// (everything-on) tool-set — the shape most baseStale tests want, so they can
// focus on the version/stamp comparison rather than the toolset selection.
func allToolsConfig(baseName string) vm.CreateConfig {
	return vm.CreateConfig{BaseName: baseName, WithDDEV: true, WithGo: true, WithJava: true}
}

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

	v, err := contentPlaybookVersion(dir, "ddev+go+java")
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
	before, err := contentPlaybookVersion(dir, "ddev+go+java")
	if err != nil {
		t.Fatalf("contentPlaybookVersion before: %v", err)
	}

	// Simulate a commit that only touches a non-playbook file.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("docs change\n"), 0o644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}

	after, err := contentPlaybookVersion(dir, "ddev+go+java")
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
	before, err := contentPlaybookVersion(dir, "ddev+go+java")
	if err != nil {
		t.Fatalf("contentPlaybookVersion before: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "site.yml"), []byte("- hosts: all\n- changed: true\n"), 0o644); err != nil {
		t.Fatalf("write site.yml: %v", err)
	}

	after, err := contentPlaybookVersion(dir, "ddev+go+java")
	if err != nil {
		t.Fatalf("contentPlaybookVersion after: %v", err)
	}
	if before == after {
		t.Errorf("editing a playbook file did not change the stamp (%q)", before)
	}
}

// TestPlaybookVersion_ConfigsDifferingOnlyInWithJavaProduceDifferentStamps is
// the literal acceptance criterion: two vm.CreateConfig values that differ in
// nothing but WithJava must stamp the SAME playbook fileset differently, end
// to end through CreateConfig.ToolsetKey() -> PlaybookVersion — proving the
// tool-set selection actually reaches, and changes, the base version stamp.
func TestPlaybookVersion_ConfigsDifferingOnlyInWithJavaProduceDifferentStamps(t *testing.T) {
	fsys := samplePlaybookFS(false)

	withJava := vm.CreateConfig{WithDDEV: true, WithGo: true, WithJava: true}
	withoutJava := vm.CreateConfig{WithDDEV: true, WithGo: true, WithJava: false}

	a, err := PlaybookVersion(fsys, withJava.ToolsetKey())
	if err != nil {
		t.Fatalf("PlaybookVersion (WithJava=true): %v", err)
	}
	b, err := PlaybookVersion(fsys, withoutJava.ToolsetKey())
	if err != nil {
		t.Fatalf("PlaybookVersion (WithJava=false): %v", err)
	}
	if a == b {
		t.Errorf("configs differing only in WithJava produced the same stamp: %q", a)
	}
}

// TestContentPlaybookVersion_DifferentToolsetDifferentStamp is the wiring
// this task exists to land: contentPlaybookVersion must fold its toolset
// argument into the stamp, not a hardcoded placeholder, so changing
// vm.CreateConfig's tool-set selection alone (no playbook edit at all) still
// marks the base stale.
func TestContentPlaybookVersion_DifferentToolsetDifferentStamp(t *testing.T) {
	dir := t.TempDir()
	writePlaybookFiles(t, dir, false)

	allThree, err := contentPlaybookVersion(dir, "ddev+go+java")
	if err != nil {
		t.Fatalf("contentPlaybookVersion (ddev+go+java): %v", err)
	}
	noJava, err := contentPlaybookVersion(dir, "ddev+go")
	if err != nil {
		t.Fatalf("contentPlaybookVersion (ddev+go): %v", err)
	}
	if allThree == noJava {
		t.Errorf("changing the toolset selection did not change the stamp (%q)", allThree)
	}
}

// TestBaseStale_OldFormatGitStampIsStale asserts an upgrading user converges:
// a stamp in the old git-HEAD format (a bare 40-hex commit SHA, no "v2:"
// prefix) must be treated as stale even though it is non-empty and even
// though it happens not to equal the freshly computed content-hash stamp for
// an unrelated reason — the point is that the *format* alone is disqualifying.
func TestBaseStale_OldFormatGitStampIsStale(t *testing.T) {
	origVer, origRead := playbookVersionFn, readBaseVersionFn
	playbookVersionFn = func(string, string) (string, error) { return "v2:deadbeef:ddev+go+java", nil }
	readBaseVersionFn = func(string) string { return "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2" } // v1-style 40-hex git SHA
	t.Cleanup(func() { playbookVersionFn, readBaseVersionFn = origVer, origRead })

	p := &Provisioner{PlaybookDir: "/playbook"}
	if _, stale := p.baseStale(allToolsConfig("sandbar-base"), io.Discard); !stale {
		t.Fatal("a v1-style (bare git-hash) stamp must be treated as stale")
	}
}

// TestBaseStale_EmptyStampIsStale preserves today's behaviour: an
// unreadable/absent stamp counts as stale.
func TestBaseStale_EmptyStampIsStale(t *testing.T) {
	origVer, origRead := playbookVersionFn, readBaseVersionFn
	playbookVersionFn = func(string, string) (string, error) { return "v2:deadbeef:ddev+go+java", nil }
	readBaseVersionFn = func(string) string { return "" }
	t.Cleanup(func() { playbookVersionFn, readBaseVersionFn = origVer, origRead })

	p := &Provisioner{PlaybookDir: "/playbook"}
	if _, stale := p.baseStale(allToolsConfig("sandbar-base"), io.Discard); !stale {
		t.Fatal("an empty stamp must be treated as stale")
	}
}

// TestBaseStale_MatchingV2StampNotStale is the happy path: a v2-format stamp
// that matches the current computed version is not stale.
func TestBaseStale_MatchingV2StampNotStale(t *testing.T) {
	origVer, origRead := playbookVersionFn, readBaseVersionFn
	playbookVersionFn = func(string, string) (string, error) { return "v2:deadbeef:ddev+go+java", nil }
	readBaseVersionFn = func(string) string { return "v2:deadbeef:ddev+go+java" }
	t.Cleanup(func() { playbookVersionFn, readBaseVersionFn = origVer, origRead })

	p := &Provisioner{PlaybookDir: "/playbook"}
	if _, stale := p.baseStale(allToolsConfig("sandbar-base"), io.Discard); stale {
		t.Fatal("a matching v2 stamp must not be treated as stale")
	}
}

// TestBaseStale_PassesConfigToolsetKey is the wiring this task lands:
// baseStale must ask playbookVersionFn for the stamp using cfg's OWN
// ToolsetKey(), not a hardcoded placeholder — so a create with a
// non-default tool-set selection is compared against the right "want".
func TestBaseStale_PassesConfigToolsetKey(t *testing.T) {
	var gotToolset string
	origVer, origRead := playbookVersionFn, readBaseVersionFn
	playbookVersionFn = func(_ string, toolset string) (string, error) {
		gotToolset = toolset
		return "v2:deadbeef:" + toolset, nil
	}
	readBaseVersionFn = func(string) string { return "v2:deadbeef:ddev+go+java" }
	t.Cleanup(func() { playbookVersionFn, readBaseVersionFn = origVer, origRead })

	p := &Provisioner{PlaybookDir: "/playbook"}
	cfg := vm.CreateConfig{BaseName: "sandbar-base", WithDDEV: true, WithGo: true, WithJava: false}
	if _, stale := p.baseStale(cfg, io.Discard); stale {
		t.Fatal("de-selecting a tool must NOT make the base stale: the base still CONTAINS it " +
			"(a converge-in-place cannot uninstall), so re-applying changes nothing. Calling it " +
			"stale is what made alternating selections re-converge the shared base forever.")
	}
	if want := cfg.ToolsetKey(); gotToolset != want {
		t.Errorf("baseStale passed toolset %q to playbookVersionFn, want cfg.ToolsetKey() = %q", gotToolset, want)
	}
}

// TestBaseStale_NewlySelectedToolIsStale is the other half of the tool-set
// staleness rule: de-selecting is a no-op (above), but SELECTING a tool the base
// does not carry must still mark it stale, or the tool would never be installed.
func TestBaseStale_NewlySelectedToolIsStale(t *testing.T) {
	origVer, origRead := playbookVersionFn, readBaseVersionFn
	playbookVersionFn = func(_ string, toolset string) (string, error) {
		return "v2:deadbeef:" + toolset, nil
	}
	readBaseVersionFn = func(string) string { return "v2:deadbeef:ddev" } // no go, no java
	t.Cleanup(func() { playbookVersionFn, readBaseVersionFn = origVer, origRead })

	p := &Provisioner{PlaybookDir: "/playbook"}
	cfg := vm.CreateConfig{BaseName: "sandbar-base", WithDDEV: true, WithGo: true, WithJava: false}
	want, stale := p.baseStale(cfg, io.Discard)
	if !stale {
		t.Fatal("selecting a tool the base does not carry must be stale, or it never gets installed")
	}
	// And the version it converges TO records the union of what the base already
	// has and what was asked for — what the base will actually contain afterwards.
	if want != "v2:deadbeef:ddev+go" {
		t.Errorf("target stamp = %q, want the union %q", want, "v2:deadbeef:ddev+go")
	}
}

// TestBaseStale_ReselectingAfterDeselectDoesNotPingPong is the regression this
// whole union scheme exists for. With one shared base and additive convergence,
// stamping only the REQUESTED tool-set makes each alternating create disagree
// with the last, so both re-converge forever. Walk that exact sequence and prove
// it settles instead.
func TestBaseStale_ReselectingAfterDeselectDoesNotPingPong(t *testing.T) {
	stamp := "v2:deadbeef:ddev+go+java" // base built with everything
	origVer, origRead := playbookVersionFn, readBaseVersionFn
	playbookVersionFn = func(_ string, toolset string) (string, error) {
		return "v2:deadbeef:" + toolset, nil
	}
	readBaseVersionFn = func(string) string { return stamp }
	t.Cleanup(func() { playbookVersionFn, readBaseVersionFn = origVer, origRead })

	p := &Provisioner{PlaybookDir: "/playbook"}
	noGo := vm.CreateConfig{BaseName: "sandbar-base", WithDDEV: true, WithGo: false, WithJava: true}
	all := vm.CreateConfig{BaseName: "sandbar-base", WithDDEV: true, WithGo: true, WithJava: true}

	// `sand create --with-go=false`: go stays installed, nothing to converge.
	if _, stale := p.baseStale(noGo, io.Discard); stale {
		t.Fatal("--with-go=false against a base that already has go must not re-converge it")
	}
	// A plain create right after it must also be a no-op — the base never lost go,
	// so there is nothing to add back. Before the union scheme this flipped the
	// stamp and each create re-applied the base for minutes, forever.
	if _, stale := p.baseStale(all, io.Discard); stale {
		t.Fatal("a default create following a --with-go=false create must not re-converge the base")
	}
}

// TestShrunk_ReturnsOnlyDeselectedTools proves shrunk() returns exactly the
// tools that were on in stamped but are off in want — nothing else, whatever
// the map iteration order happens to be (hence sorted).
func TestShrunk_ReturnsOnlyDeselectedTools(t *testing.T) {
	stamped := map[string]bool{"ddev": true, "go": true, "java": true}
	want := map[string]bool{"ddev": true, "go": false, "java": false}

	got := shrunk(stamped, want)
	if len(got) != 2 || got[0] != "go" || got[1] != "java" {
		t.Errorf("shrunk() = %v, want [go java]", got)
	}
}

// TestShrunk_NoShrinkReturnsEmpty covers growing and unchanged selections:
// neither is a shrink, so nothing should be reported.
func TestShrunk_NoShrinkReturnsEmpty(t *testing.T) {
	tests := []struct {
		name            string
		stamped, wanted map[string]bool
	}{
		{"identical", map[string]bool{"ddev": true}, map[string]bool{"ddev": true}},
		{"growing", map[string]bool{"ddev": true}, map[string]bool{"ddev": true, "go": true}},
		{"both empty", map[string]bool{}, map[string]bool{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shrunk(tt.stamped, tt.wanted); len(got) != 0 {
				t.Errorf("shrunk(%v, %v) = %v, want empty", tt.stamped, tt.wanted, got)
			}
		})
	}
}

// TestParseToolset_RoundTripsToolsetKey proves parseToolset is the inverse of
// vm.CreateConfig.ToolsetKey() for every selection shape that key can
// produce: the full set, a subset, and the empty ("none") selection.
func TestParseToolset_RoundTripsToolsetKey(t *testing.T) {
	tests := []struct {
		key  string
		want map[string]bool
	}{
		{"ddev+go+java", map[string]bool{"ddev": true, "go": true, "java": true}},
		{"go", map[string]bool{"go": true}},
		{"none", map[string]bool{}},
		{"", map[string]bool{}},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := parseToolset(tt.key)
			if len(got) != len(tt.want) {
				t.Fatalf("parseToolset(%q) = %v, want %v", tt.key, got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("parseToolset(%q)[%q] = %v, want %v", tt.key, k, got[k], v)
				}
			}
		})
	}
}

// TestToolsetFromStamp_ExtractsSuffix pins the "v2:<hash>:<toolset>" parsing,
// including the negative cases (older scheme, empty) that must NOT be
// mistaken for an explicit empty toolset.
func TestToolsetFromStamp_ExtractsSuffix(t *testing.T) {
	tests := []struct {
		name  string
		stamp string
		want  string
	}{
		{"v2 with toolset", "v2:deadbeef:ddev+go+java", "ddev+go+java"},
		{"v2 with none", "v2:deadbeef:none", "none"},
		{"v1-style bare hash", "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolsetFromStamp(tt.stamp); got != tt.want {
				t.Errorf("toolsetFromStamp(%q) = %q, want %q", tt.stamp, got, tt.want)
			}
		})
	}
}

// TestWriteBaseVersion_RoundTripsVersionAndBuiltAt is the real filesystem round
// trip task 8 adds: writeBaseVersion must record BOTH the version AND a
// BuiltAt timestamp close to "now", and readBaseVersion/readBaseBuiltAt must
// read them back correctly through the real stamp file (not a stub).
func TestWriteBaseVersion_RoundTripsVersionAndBuiltAt(t *testing.T) {
	t.Setenv("LIMA_HOME", t.TempDir())

	before := time.Now()
	if err := writeBaseVersion("sandbar-base", "v2:deadbeef:ddev+go+java", time.Now()); err != nil {
		t.Fatalf("writeBaseVersion: %v", err)
	}
	after := time.Now()

	if got := readBaseVersion("sandbar-base"); got != "v2:deadbeef:ddev+go+java" {
		t.Errorf("readBaseVersion = %q, want v2:deadbeef:ddev+go+java", got)
	}

	builtAt, ok := readBaseBuiltAt("sandbar-base")
	if !ok {
		t.Fatal("readBaseBuiltAt: ok = false, want true for a stamp writeBaseVersion just wrote")
	}
	if builtAt.Before(before.Add(-time.Second)) || builtAt.After(after.Add(time.Second)) {
		t.Errorf("readBaseBuiltAt = %v, want between %v and %v", builtAt, before, after)
	}
}

// TestReadBaseBuiltAt_MissingStampIsNotOk: no stamp file at all (a base that has
// never been built/stamped) must report ok=false — the caller
// (baseNeedsRefresh) treats that as "cannot prove fresh" and refreshes.
func TestReadBaseBuiltAt_MissingStampIsNotOk(t *testing.T) {
	t.Setenv("LIMA_HOME", t.TempDir())

	if _, ok := readBaseBuiltAt("sandbar-base"); ok {
		t.Fatal("readBaseBuiltAt on a missing stamp returned ok=true, want false")
	}
}

// TestReadBaseBuiltAt_PreTimestampStampIsNotOk: a stamp written by a sand
// binary that predates this task (version only, no second line) must report
// ok=false for BuiltAt — never guess a build time — while readBaseVersion
// keeps reading the version line exactly as before.
func TestReadBaseBuiltAt_PreTimestampStampIsNotOk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIMA_HOME", home)
	writeRawStamp(t, home, "sandbar-base", "v2:deadbeef:ddev+go+java\n")

	if got := readBaseVersion("sandbar-base"); got != "v2:deadbeef:ddev+go+java" {
		t.Errorf("readBaseVersion = %q, want v2:deadbeef:ddev+go+java", got)
	}
	if _, ok := readBaseBuiltAt("sandbar-base"); ok {
		t.Fatal("readBaseBuiltAt on a pre-timestamp (version-only) stamp returned ok=true, want false")
	}
}

// TestReadBaseBuiltAt_UnparseableTimestampIsNotOk: a corrupt second line (not
// RFC3339) must also report ok=false rather than an incorrect time.Time zero
// value being mistaken for a real (very stale) build time.
func TestReadBaseBuiltAt_UnparseableTimestampIsNotOk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIMA_HOME", home)
	writeRawStamp(t, home, "sandbar-base", "v2:deadbeef:ddev+go+java\nnot-a-timestamp\n")

	if _, ok := readBaseBuiltAt("sandbar-base"); ok {
		t.Fatal("readBaseBuiltAt on an unparseable timestamp returned ok=true, want false")
	}
}

// writeRawStamp writes raw content directly to a base's stamp path, bypassing
// writeBaseVersion, so a test can construct stamp shapes writeBaseVersion
// itself would never produce (pre-timestamp, corrupt).
func writeRawStamp(t *testing.T, limaHome, baseName, content string) {
	t.Helper()
	dir := filepath.Join(limaHome, "_sand")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, baseName+".playbook-version"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestParseBaseStamp_EmptyIsNotOk pins parseBaseStamp's base case: an empty
// stamp file parses to an empty version and ok=false, matching readBaseVersion
// returning "" for an unreadable/missing file.
func TestParseBaseStamp_EmptyIsNotOk(t *testing.T) {
	stamp, ok := parseBaseStamp([]byte(""))
	if ok {
		t.Fatal("parseBaseStamp(\"\") returned ok=true, want false")
	}
	if stamp.Version != "" {
		t.Errorf("parseBaseStamp(\"\").Version = %q, want empty", stamp.Version)
	}
}

// TestShrunkTools_DetectsAndOmitsCorrectly is the end-to-end shrink-detection
// case this task's advisory depends on: a base stamped with the full
// tool-set, reapplied with Java deselected, must report exactly ["java"] —
// and a growing or unchanged selection must report nothing.
func TestShrunkTools_DetectsAndOmitsCorrectly(t *testing.T) {
	fullStamp := "v2:deadbeef:ddev+go+java"

	if got := shrunkTools(fullStamp, "ddev+go"); len(got) != 1 || got[0] != "java" {
		t.Errorf("shrunkTools(full, ddev+go) = %v, want [java]", got)
	}
	if got := shrunkTools(fullStamp, "ddev+go+java"); len(got) != 0 {
		t.Errorf("shrunkTools(full, full) = %v, want empty (no shrink)", got)
	}
	if got := shrunkTools(fullStamp, "ddev+go+java"); len(got) != 0 {
		t.Errorf("shrunkTools with an identical selection = %v, want empty", got)
	}
	// A base with no toolset info in its stamp (older scheme / unstamped): no
	// comparison to make, so nothing to warn about here — the stale-base
	// machinery already forces a rebuild/reapply for that case regardless.
	if got := shrunkTools("v1-style-stamp", "go"); len(got) != 0 {
		t.Errorf("shrunkTools with no toolset info in the stamp = %v, want empty", got)
	}
}

// TestBaseToolset_ReadsBackWhatTheBaseWasBuiltWith is the whole point of the
// read-back: a create must be able to ask the base what it CONTAINS, rather
// than assuming the all-on default and making the user re-de-select the same
// tools on every VM.
func TestBaseToolset_ReadsBackWhatTheBaseWasBuiltWith(t *testing.T) {
	orig := readBaseVersionFn
	defer func() { readBaseVersionFn = orig }()

	readBaseVersionFn = func(string) string { return "v2:deadbeef:ddev+go" }
	set, ok := BaseToolset("sandbar-base")
	if !ok {
		t.Fatal("a v2 stamp carries a tool-set; BaseToolset must report ok")
	}
	if !set["ddev"] || !set["go"] || set["claude"] || set["java"] {
		t.Errorf("BaseToolset = %v, want exactly {ddev, go}", set)
	}
}

// A base built with NOTHING selected stamps "none". That is a real answer, not
// a missing one: reporting ok=false here would send the caller back to its
// all-on default and re-install every tool the user just opted out of — the
// exact bug the read-back exists to kill.
func TestBaseToolset_NoneIsAnAnswerNotAnAbsence(t *testing.T) {
	orig := readBaseVersionFn
	defer func() { readBaseVersionFn = orig }()

	readBaseVersionFn = func(string) string { return "v2:deadbeef:none" }
	set, ok := BaseToolset("sandbar-base")
	if !ok {
		t.Fatal(`a base stamped "none" was built with no tools; that must be reported as ok, or the caller falls back to all-on and re-installs them`)
	}
	if len(set) != 0 {
		t.Errorf("BaseToolset = %v, want an empty set", set)
	}
}

// No stamp (no base built yet) and an older stamp that carries no tool-set
// suffix are both "no information" — the caller keeps its own default.
func TestBaseToolset_NoToolsetInformation(t *testing.T) {
	orig := readBaseVersionFn
	defer func() { readBaseVersionFn = orig }()

	for _, stamp := range []string{"", "somegitsha", "v2:deadbeef"} {
		readBaseVersionFn = func(string) string { return stamp }
		if set, ok := BaseToolset("sandbar-base"); ok {
			t.Errorf("BaseToolset(%q) = %v, ok=true; want ok=false (no tool-set information to adopt)", stamp, set)
		}
	}
}

// TestWriteReadBaseVersion_RealRoundTripLandsAtDerivedPath drives the REAL
// writeBaseVersion and readBaseVersion — not the readBaseVersionFn/
// writeBaseVersionFn stub vars other tests in this file swap out — so the
// actual path derivation in baseVersionPath (LIMA_HOME/_sand/<base>.playbook-
// version) is exercised end to end, not bypassed. Sibling tests
// (TestWriteBaseVersion_RoundTripsVersionAndBuiltAt and friends) already cover
// the BuiltAt round trip through the real functions; this one is the one that
// pins the on-disk LOCATION the stamp must land at.
func TestWriteReadBaseVersion_RealRoundTripLandsAtDerivedPath(t *testing.T) {
	limaHomeDir := t.TempDir()
	t.Setenv("LIMA_HOME", limaHomeDir)

	const baseName = "sandbar-base"
	const version = "v2:cafef00d:ddev+go"
	builtAt := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

	if err := writeBaseVersion(baseName, version, builtAt); err != nil {
		t.Fatalf("writeBaseVersion: %v", err)
	}

	wantPath := baseVersionPath(baseName)
	if !strings.HasPrefix(wantPath, limaHomeDir) {
		t.Fatalf("baseVersionPath(%q) = %q, want it rooted under LIMA_HOME %q", baseName, wantPath, limaHomeDir)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("writeBaseVersion did not write to the path baseVersionPath derives (%s): %v", wantPath, err)
	}

	if got := readBaseVersion(baseName); got != version {
		t.Errorf("readBaseVersion = %q, want %q", got, version)
	}
}

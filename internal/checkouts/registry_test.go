package checkouts

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/registry"
)

// sampleCheckout returns a fully populated row so round-trip tests exercise
// every field, not just the zero value.
func sampleCheckout(path string) Checkout {
	return Checkout{
		Path:      path,
		Kind:      KindRepo,
		Parent:    "",
		Branch:    "feature/land",
		Forge:     "github.com",
		OrgRepo:   "lullabot/sandbar",
		PushState: PushStatePushed,
		Ahead:     2,
		Behind:    1,
		Dirty:     3,
		LastSeen:  time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
	}
}

// TestRoundTrip exercises the custom persistence: Set -> reload (a fresh
// LoadFrom against the same path) -> Get must return the identical rows,
// proving the data actually reached disk rather than only living in the
// original *Registry's memory.
func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkout-registry.json")

	r, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load (missing file): %v", err)
	}
	if _, ok := r.Get(registry.LocalScope, "dev"); ok {
		t.Fatal("empty registry should report no entry for an unknown VM")
	}

	want := VMCheckouts{
		Checkouts: []Checkout{
			sampleCheckout("/home/user/sandbar"),
			{
				Path:      "/home/user/sandbar-wt/fix",
				Kind:      KindWorktree,
				Parent:    "/home/user/sandbar",
				Branch:    "fix/bug",
				PushState: PushStateNever,
				LastSeen:  time.Date(2026, 7, 17, 12, 1, 0, 0, time.UTC),
			},
		},
		Truncated: true,
		SweptAt:   time.Date(2026, 7, 17, 12, 1, 5, 0, time.UTC),
	}

	if err := r.Set(registry.LocalScope, "dev", want); err != nil {
		t.Fatalf("set: %v", err)
	}

	r2, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := r2.Get(registry.LocalScope, "dev")
	if !ok {
		t.Fatal("dev should have an entry after reload")
	}
	if !got.SweptAt.Equal(want.SweptAt) {
		t.Fatalf("SweptAt = %v, want %v", got.SweptAt, want.SweptAt)
	}
	if got.Truncated != want.Truncated {
		t.Fatalf("Truncated = %v, want %v", got.Truncated, want.Truncated)
	}
	if len(got.Checkouts) != len(want.Checkouts) {
		t.Fatalf("Checkouts len = %d, want %d", len(got.Checkouts), len(want.Checkouts))
	}
	for i := range want.Checkouts {
		w, g := want.Checkouts[i], got.Checkouts[i]
		if g.Path != w.Path || g.Kind != w.Kind || g.Parent != w.Parent ||
			g.Branch != w.Branch || g.Forge != w.Forge || g.OrgRepo != w.OrgRepo ||
			g.PushState != w.PushState || g.Ahead != w.Ahead || g.Behind != w.Behind ||
			g.Dirty != w.Dirty || !g.LastSeen.Equal(w.LastSeen) {
			t.Fatalf("row %d = %+v, want %+v", i, g, w)
		}
	}
}

// TestFilePermissions confirms the persisted file is mode 0600, matching
// managed-vms.json and secrets.json.
func TestFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkout-registry.json")
	r, _ := LoadFrom(path)
	if err := r.Set(registry.LocalScope, "dev", VMCheckouts{Checkouts: []Checkout{sampleCheckout("/x")}}); err != nil {
		t.Fatalf("set: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file mode = %o, want 0600", perm)
	}
}

// TestAtomicRewrite confirms Set does not leave a stray temp file behind and
// that the target file always parses (never a half-written truncation) --
// the two properties an os.Rename-based atomic rewrite promises.
func TestAtomicRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkout-registry.json")
	r, _ := LoadFrom(path)
	for i := 0; i < 5; i++ {
		if err := r.Set(registry.LocalScope, "dev", VMCheckouts{Checkouts: []Checkout{sampleCheckout("/x")}}); err != nil {
			t.Fatalf("set %d: %v", i, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("stray temp file left behind: %s", e.Name())
		}
	}
	if _, err := LoadFrom(path); err != nil {
		t.Fatalf("final file failed to reload: %v", err)
	}
}

// TestConnectionScopingCollision: the same VM NAME under two different
// connection scopes must record two independent entries -- Set on one scope
// must never overwrite, or be visible through, the other. This is the
// property internal/registry's (scope, name) keying and internal/secrets'
// connection-scoped store both guarantee, and this registry must too.
func TestConnectionScopingCollision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkout-registry.json")
	r, _ := LoadFrom(path)

	scopeA := registry.LocalScope
	scopeB := registry.Scope{Provider: "ssh", RemoteTarget: "user@host:22"}

	a := VMCheckouts{Checkouts: []Checkout{sampleCheckout("/a/repo")}}
	b := VMCheckouts{Checkouts: []Checkout{{Path: "/b/repo", Kind: KindRepo, Branch: "main", PushState: PushStateNever}}}

	if err := r.Set(scopeA, "web", a); err != nil {
		t.Fatalf("set scopeA: %v", err)
	}
	if err := r.Set(scopeB, "web", b); err != nil {
		t.Fatalf("set scopeB: %v", err)
	}

	gotA, ok := r.Get(scopeA, "web")
	if !ok || len(gotA.Checkouts) != 1 || gotA.Checkouts[0].Path != "/a/repo" {
		t.Fatalf("scopeA web = %+v (ok=%v), want the /a/repo row", gotA, ok)
	}
	gotB, ok := r.Get(scopeB, "web")
	if !ok || len(gotB.Checkouts) != 1 || gotB.Checkouts[0].Path != "/b/repo" {
		t.Fatalf("scopeB web = %+v (ok=%v), want the /b/repo row", gotB, ok)
	}

	// And it must survive a reload from disk, not just live in memory.
	r2, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	gotA2, ok := r2.Get(scopeA, "web")
	if !ok || gotA2.Checkouts[0].Path != "/a/repo" {
		t.Fatalf("after reload, scopeA web = %+v (ok=%v)", gotA2, ok)
	}
	gotB2, ok := r2.Get(scopeB, "web")
	if !ok || gotB2.Checkouts[0].Path != "/b/repo" {
		t.Fatalf("after reload, scopeB web = %+v (ok=%v)", gotB2, ok)
	}
}

// TestGetReturnsDeepCopy: mutating the VMCheckouts (and its Checkouts slice)
// returned by Get must never reach the registry's own stored state -- the
// by-value-read half of the concurrency contract.
func TestGetReturnsDeepCopy(t *testing.T) {
	r := NewEmpty()
	orig := VMCheckouts{Checkouts: []Checkout{sampleCheckout("/x")}}
	if err := r.Set(registry.LocalScope, "dev", orig); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, ok := r.Get(registry.LocalScope, "dev")
	if !ok {
		t.Fatal("expected an entry")
	}
	got.Checkouts[0].Path = "/mutated"
	got.Checkouts[0].Dirty = 999
	got.Truncated = true

	got2, ok := r.Get(registry.LocalScope, "dev")
	if !ok {
		t.Fatal("expected an entry on second read")
	}
	if got2.Checkouts[0].Path == "/mutated" || got2.Checkouts[0].Dirty == 999 {
		t.Fatalf("mutating a Get result leaked into the store: %+v", got2)
	}
	if got2.Truncated {
		t.Fatal("mutating a Get result's Truncated leaked into the store")
	}
}

// TestSetDeepCopiesInput: mutating the VMCheckouts a caller passed to Set
// (after the call returns) must never reach the registry's stored state --
// the by-value-write half of the concurrency contract.
func TestSetDeepCopiesInput(t *testing.T) {
	r := NewEmpty()
	in := VMCheckouts{Checkouts: []Checkout{sampleCheckout("/x")}}
	if err := r.Set(registry.LocalScope, "dev", in); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Mutate the slice's backing array the caller still holds a reference to.
	in.Checkouts[0].Path = "/mutated-after-set"

	got, ok := r.Get(registry.LocalScope, "dev")
	if !ok {
		t.Fatal("expected an entry")
	}
	if got.Checkouts[0].Path == "/mutated-after-set" {
		t.Fatalf("mutating the slice passed to Set leaked into the store: %+v", got)
	}
}

// TestConcurrentSetGet drives many goroutines through Set and Get on the
// same registry concurrently. Run with -race: the mutex must make every
// access safe, and no goroutine may ever observe a torn/partial write.
func TestConcurrentSetGet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkout-registry.json")
	r, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	const goroutines = 20
	const iterations = 25

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			vmName := "vm"
			scope := registry.Scope{Provider: "lima"}
			for i := 0; i < iterations; i++ {
				c := VMCheckouts{
					Checkouts: []Checkout{sampleCheckout("/repo")},
					SweptAt:   time.Now(),
				}
				if err := r.Set(scope, vmName, c); err != nil {
					t.Errorf("goroutine %d set %d: %v", g, i, err)
					return
				}
				if got, ok := r.Get(scope, vmName); ok {
					// Just touch the fields to exercise the copy under -race;
					// the value itself is racing with other writers by design.
					_ = got.Checkouts[0].Path
				}
			}
		}(g)
	}
	wg.Wait()

	// The registry must still be in a coherent, loadable state afterward.
	if _, err := LoadFrom(path); err != nil {
		t.Fatalf("registry unreadable after concurrent access: %v", err)
	}
}

// TestConcurrentDifferentVMs exercises the more realistic concurrency shape:
// distinct VMs (distinct keys) written by distinct goroutines, each verifying
// its OWN key's value round-trips correctly despite the shared map access
// under the mutex.
func TestConcurrentDifferentVMs(t *testing.T) {
	r := NewEmpty()
	const n = 30
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			vmName := "vm"
			scope := registry.Scope{Provider: "lima", RemoteTarget: strFromInt(i)}
			path := strFromInt(i) + "-repo"
			c := VMCheckouts{Checkouts: []Checkout{{Path: path, Kind: KindRepo, Branch: "main"}}}
			if err := r.Set(scope, vmName, c); err != nil {
				t.Errorf("set %d: %v", i, err)
				return
			}
			got, ok := r.Get(scope, vmName)
			if !ok || len(got.Checkouts) != 1 || got.Checkouts[0].Path != path {
				t.Errorf("goroutine %d: got %+v (ok=%v), want path %q", i, got, ok, path)
			}
		}(i)
	}
	wg.Wait()
}

func strFromInt(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{digits[i%10]}, b...)
		i /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// TestLoadMissingFile: a path that does not exist yields an empty, usable
// registry and no error.
func TestLoadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	r, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load missing file: %v", err)
	}
	if _, ok := r.Get(registry.LocalScope, "dev"); ok {
		t.Fatal("expected no entries in a freshly loaded missing-file registry")
	}
}

// TestLoadEmptyFile: an existing but zero-byte file yields an empty, usable
// registry and no error (distinct from a missing file at the os.ReadFile
// level, so it needs its own test).
func TestLoadEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkout-registry.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write empty file: %v", err)
	}
	r, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load empty file: %v", err)
	}
	if _, ok := r.Get(registry.LocalScope, "dev"); ok {
		t.Fatal("expected no entries from an empty file")
	}
}

// TestLoadCorruptFile: bytes that are not valid JSON must not panic. Load
// returns a usable empty registry, moves the bad file aside to
// "<path>.corrupt", and reports the error for the caller to surface.
func TestLoadCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkout-registry.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
	r, err := LoadFrom(path)
	if err == nil {
		t.Fatal("expected an error loading a corrupt file")
	}
	if r == nil {
		t.Fatal("LoadFrom must return a non-nil registry even on error")
	}
	if _, ok := r.Get(registry.LocalScope, "dev"); ok {
		t.Fatal("a corrupt-file load should yield an empty registry")
	}
	if _, statErr := os.Stat(path + ".corrupt"); statErr != nil {
		t.Fatalf("corrupt file was not moved aside: %v", statErr)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("original corrupt path should no longer exist, stat err = %v", statErr)
	}
}

// TestLoadFutureVersion: a file stamped with a schema version newer than
// this build understands must be refused (empty registry, error returned)
// rather than misparsed.
func TestLoadFutureVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkout-registry.json")
	if err := os.WriteFile(path, []byte(`{"version":999,"vms":[]}`), 0o600); err != nil {
		t.Fatalf("write future-version file: %v", err)
	}
	r, err := LoadFrom(path)
	if err == nil {
		t.Fatal("expected an error loading a future-schema-version file")
	}
	if r == nil {
		t.Fatal("LoadFrom must return a non-nil registry even on error")
	}
}

// TestNewEmptyNeverTouchesDisk: NewEmpty's registry has no path, so Set must
// be a working no-op with respect to persistence (in-memory only), never
// erroring and never creating a file.
func TestNewEmptyNeverTouchesDisk(t *testing.T) {
	r := NewEmpty()
	if err := r.Set(registry.LocalScope, "dev", VMCheckouts{Checkouts: []Checkout{sampleCheckout("/x")}}); err != nil {
		t.Fatalf("set on in-memory registry: %v", err)
	}
	got, ok := r.Get(registry.LocalScope, "dev")
	if !ok || len(got.Checkouts) != 1 {
		t.Fatalf("expected the in-memory entry to be readable back: %+v (ok=%v)", got, ok)
	}
}

// TestDefaultPath confirms the XDG derivation matches the sibling packages'
// idiom exactly: ${XDG_DATA_HOME:-...}/sandbar/checkout-registry.json.
func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/x/y")
	got := defaultPath()
	want := filepath.Join("/x/y", "sandbar", "checkout-registry.json")
	if got != want {
		t.Fatalf("defaultPath = %q, want %q", got, want)
	}
}

// TestLoadUsesXDGDataHome exercises Load() end-to-end against a real
// XDG_DATA_HOME, mirroring how internal/registry's and internal/secrets'
// own tests set the env var rather than calling LoadFrom directly.
func TestLoadUsesXDGDataHome(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)

	r, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := r.Set(registry.LocalScope, "dev", VMCheckouts{Checkouts: []Checkout{sampleCheckout("/x")}}); err != nil {
		t.Fatalf("set: %v", err)
	}

	wantPath := filepath.Join(dataHome, "sandbar", "checkout-registry.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected file at %s: %v", wantPath, err)
	}

	r2, err := Load()
	if err != nil {
		t.Fatalf("reload via Load: %v", err)
	}
	if _, ok := r2.Get(registry.LocalScope, "dev"); !ok {
		t.Fatal("expected the entry to survive a fresh Load()")
	}
}

// TestGetOnNilRegistry: a nil *Registry must not panic and must report "no
// entry", mirroring the nil-receiver tolerance internal/ui's heartbeatRegistry
// gives every accessor.
func TestGetOnNilRegistry(t *testing.T) {
	var r *Registry
	if _, ok := r.Get(registry.LocalScope, "dev"); ok {
		t.Fatal("nil registry should report no entry")
	}
}

// TestSetOnNilRegistry: a nil *Registry's Set must error, not panic.
func TestSetOnNilRegistry(t *testing.T) {
	var r *Registry
	if err := r.Set(registry.LocalScope, "dev", VMCheckouts{}); err == nil {
		t.Fatal("expected an error calling Set on a nil registry")
	}
}

// TestDefaultPathFallsBackToHome exercises defaultPath's other branch: with
// XDG_DATA_HOME unset, it must fall back to $HOME/.local/share.
func TestDefaultPathFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := defaultPath()
	want := filepath.Join(home, ".local", "share", "sandbar", "checkout-registry.json")
	if got != want {
		t.Fatalf("defaultPath = %q, want %q", got, want)
	}
}

// TestDefaultPathHomeDirUnavailable exercises defaultPath's final fallback:
// when neither XDG_DATA_HOME nor $HOME (which os.UserHomeDir reads on Unix)
// is set, os.UserHomeDir errors and defaultPath falls back to ".".
func TestDefaultPathHomeDirUnavailable(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "")
	got := defaultPath()
	want := filepath.Join(".", ".local", "share", "sandbar", "checkout-registry.json")
	if got != want {
		t.Fatalf("defaultPath = %q, want %q", got, want)
	}
}

// TestLoadFromReadDirectory: os.ReadFile against a path that is a directory
// returns an error that is NOT fs.ErrNotExist, exercising LoadFrom's other
// read-error branch (as opposed to the missing-file branch).
func TestLoadFromReadDirectory(t *testing.T) {
	dir := t.TempDir()
	r, err := LoadFrom(dir)
	if err == nil {
		t.Fatal("expected an error loading a directory as the registry file")
	}
	if r == nil {
		t.Fatal("LoadFrom must return a non-nil registry even on error")
	}
}

// TestLoadFromMismatchedVMsShape: a file whose top-level JSON is valid (the
// version probe succeeds) but whose "vms" field is not the expected array
// shape must still be treated as corrupt (moved aside, error returned) --
// exercising the second unmarshal's error path, distinct from the first
// (invalid JSON entirely).
func TestLoadFromMismatchedVMsShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkout-registry.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"vms":"not-an-array"}`), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	r, err := LoadFrom(path)
	if err == nil {
		t.Fatal("expected an error loading a file with a mismatched vms shape")
	}
	if r == nil {
		t.Fatal("LoadFrom must return a non-nil registry even on error")
	}
	if _, statErr := os.Stat(path + ".corrupt"); statErr != nil {
		t.Fatalf("mismatched-shape file was not moved aside: %v", statErr)
	}
}

// TestSetSaveMkdirAllFails: when the registry's directory cannot be created
// (a path component is a regular file, not a directory), Set must surface
// the error rather than silently dropping the write.
func TestSetSaveMkdirAllFails(t *testing.T) {
	base := t.TempDir()
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	path := filepath.Join(blocker, "sub", "checkout-registry.json")
	r, _ := LoadFrom(path)
	if err := r.Set(registry.LocalScope, "dev", VMCheckouts{}); err == nil {
		t.Fatal("expected Set to fail when its directory cannot be created")
	}
}

// TestSetSaveRenameFails: when the atomic rename's target is an existing
// directory (rather than a plain file or nothing), the final os.Rename must
// fail and Set must surface that error.
func TestSetSaveRenameFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkout-registry.json")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir at target path: %v", err)
	}
	r, _ := LoadFrom(path)
	if err := r.Set(registry.LocalScope, "dev", VMCheckouts{}); err == nil {
		t.Fatal("expected Set to fail when the target path is a directory")
	}
}

// TestSetSaveCreateTempFails: when the registry's directory exists but is
// not writable, os.CreateTemp inside save must fail and Set must surface
// that error -- exercising the branch distinct from a missing directory
// (TestSetSaveMkdirAllFails) or an occupied target path
// (TestSetSaveRenameFails).
func TestSetSaveCreateTempFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: directory permissions do not block writes")
	}
	base := t.TempDir()
	dir := filepath.Join(base, "readonly")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatalf("mkdir readonly dir: %v", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) }) // let t.TempDir() clean up
	path := filepath.Join(dir, "checkout-registry.json")
	r, _ := LoadFrom(path)
	if err := r.Set(registry.LocalScope, "dev", VMCheckouts{}); err == nil {
		t.Fatal("expected Set to fail when its directory is not writable")
	}
}

// TestNilCheckoutsRoundTrip: a VMCheckouts with a nil Checkouts slice (no
// checkouts discovered at all, or a VM whose sweep found nothing) must
// round-trip cleanly through both Get's copy and a disk reload, exercising
// cloneCheckouts' nil branch on both ends.
func TestNilCheckoutsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkout-registry.json")
	r, _ := LoadFrom(path)
	if err := r.Set(registry.LocalScope, "dev", VMCheckouts{SweptAt: time.Now()}); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, ok := r.Get(registry.LocalScope, "dev")
	if !ok {
		t.Fatal("expected an entry")
	}
	if got.Checkouts != nil {
		t.Fatalf("expected nil Checkouts, got %+v", got.Checkouts)
	}

	r2, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got2, ok := r2.Get(registry.LocalScope, "dev")
	if !ok {
		t.Fatal("expected an entry after reload")
	}
	if len(got2.Checkouts) != 0 {
		t.Fatalf("expected no checkouts after reload, got %+v", got2.Checkouts)
	}
}

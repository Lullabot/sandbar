package provision

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
)

// stubAptCacheHostDir points aptCacheHostDirFn at dir for the duration of the
// test, restoring the original on cleanup — the same indirection pattern
// playbookVersionFn/writeBaseVersionFn use elsewhere in this package.
func stubAptCacheHostDir(t *testing.T, dir string) {
	t.Helper()
	orig := aptCacheHostDirFn
	aptCacheHostDirFn = func() (string, error) { return dir, nil }
	t.Cleanup(func() { aptCacheHostDirFn = orig })
}

// TestSeedAptCache_NoOpWhenNothingCached is the RED/GREEN-proven half of the
// no-cache-yet path: a fresh host cache dir (first build ever, or a harvest
// that never ran) must not attempt a Copy at all — there is nothing to push,
// and Copy against a nonexistent host directory would just fail.
func TestSeedAptCache_NoOpWhenNothingCached(t *testing.T) {
	host := t.TempDir() // empty: no "archives" subdir at all
	stubAptCacheHostDir(t, host)

	f := &fakeRunner{}
	p := &Provisioner{Lima: lima.New(f)}

	if err := p.seedAptCache(context.Background(), "sandbar-base", io.Discard); err != nil {
		t.Fatalf("seedAptCache: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("seedAptCache made %d limactl calls with nothing cached, want 0: %v", len(f.calls), f.calls)
	}
}

// TestSeedAptCache_PushesWhenCachePresent is the mirror case: once a prior
// harvest populated <host>/archives, seedAptCache must push exactly that
// directory into the guest, then move it into place as root.
//
// This is a TWO-step push, not a single Copy straight into the cache dir:
// /var/cache/apt/archives is root:root 0755, and `limactl copy` shells in as
// the unprivileged guest user (never root) — a direct push into it is a plain
// permission-denied for every file, reproduced against a real Lima instance.
// So the Copy lands the archives directory at the guest's /tmp/ (landing at
// aptCacheStagingDir, per Client.Copy's "source placed inside destination"
// contract), and a second, `sudo`-prefixed Shell call moves it into
// /var/cache/apt/archives and cleans up the staged copy.
func TestSeedAptCache_PushesWhenCachePresent(t *testing.T) {
	host := t.TempDir()
	archivesDir := filepath.Join(host, aptArchivesDirName)
	if err := os.MkdirAll(archivesDir, 0o755); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(archivesDir, "cowsay_3.03.deb"), []byte("fake deb"), 0o644); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}
	stubAptCacheHostDir(t, host)

	f := &fakeRunner{}
	p := &Provisioner{Lima: lima.New(f)}

	if err := p.seedAptCache(context.Background(), "sandbar-base", io.Discard); err != nil {
		t.Fatalf("seedAptCache: %v", err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("seedAptCache made %d limactl calls, want 2 (push, then sudo move): %v", len(f.calls), f.calls)
	}
	wantPush := []string{"copy", "-v", "--backend=scp", "-r", archivesDir, "sandbar-base:/tmp/"}
	if got := f.calls[0]; !reflect.DeepEqual(got, wantPush) {
		t.Fatalf("seedAptCache push argv = %v, want %v", got, wantPush)
	}
	move := f.calls[1]
	if len(move) < 3 || move[0] != "shell" || move[1] != "sandbar-base" || move[2] != "sudo" {
		t.Fatalf("seedAptCache move argv = %v, want a `shell sandbar-base sudo ...` call", move)
	}
}

// TestSeedAptCache_CopyFailureIsSwallowed proves the best-effort contract: a
// Copy failure (host unreachable, permissions, whatever) must not fail the
// base build. Caching is an optimisation, not a correctness requirement. It
// also proves the push failing short-circuits the move — there is nothing
// staged to move once the push itself failed.
func TestSeedAptCache_CopyFailureIsSwallowed(t *testing.T) {
	host := t.TempDir()
	archivesDir := filepath.Join(host, aptArchivesDirName)
	if err := os.MkdirAll(archivesDir, 0o755); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(archivesDir, "x.deb"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}
	stubAptCacheHostDir(t, host)

	f := &fakeRunner{err: os.ErrPermission}
	p := &Provisioner{Lima: lima.New(f)}

	if err := p.seedAptCache(context.Background(), "sandbar-base", io.Discard); err != nil {
		t.Fatalf("seedAptCache must swallow a Copy failure, got error: %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("seedAptCache made %d limactl calls after the push failed, want 1 (no move attempted): %v", len(f.calls), f.calls)
	}
}

// TestHarvestAptCache_AlwaysPulls proves harvestAptCache clears apt's
// lock/partial (unreadable by the unprivileged copy user, and not cached .deb
// content anyway — see aptcache.go) before attempting the Copy, and that it
// creates the host cache dir first, since a fresh machine has never had one.
// There is no "nothing to harvest" short-circuit on the guest side: the
// guest's /var/cache/apt/archives may or may not have anything in it, and
// that is Copy/apt's problem, not ours to pre-check.
func TestHarvestAptCache_AlwaysPulls(t *testing.T) {
	host := filepath.Join(t.TempDir(), "does-not-exist-yet")
	stubAptCacheHostDir(t, host)

	f := &fakeRunner{}
	p := &Provisioner{Lima: lima.New(f)}

	if err := p.harvestAptCache(context.Background(), "sandbar-base", io.Discard); err != nil {
		t.Fatalf("harvestAptCache: %v", err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("harvestAptCache made %d limactl calls, want 2 (clear lock/partial, then copy): %v", len(f.calls), f.calls)
	}
	clear := f.calls[0]
	if len(clear) < 3 || clear[0] != "shell" || clear[1] != "sandbar-base" || clear[2] != "sudo" {
		t.Fatalf("harvestAptCache clear argv = %v, want a `shell sandbar-base sudo ...` call", clear)
	}
	wantCopy := []string{"copy", "-v", "--backend=scp", "-r", "sandbar-base:/var/cache/apt/archives", host}
	if got := f.calls[1]; !reflect.DeepEqual(got, wantCopy) {
		t.Fatalf("harvestAptCache copy argv = %v, want %v", got, wantCopy)
	}
	if fi, err := os.Stat(host); err != nil || !fi.IsDir() {
		t.Errorf("harvestAptCache did not create the host cache dir %s: %v", host, err)
	}
}

// TestHarvestAptCache_CopyFailureIsSwallowed mirrors the seed side's
// best-effort contract.
func TestHarvestAptCache_CopyFailureIsSwallowed(t *testing.T) {
	host := filepath.Join(t.TempDir(), "cache")
	stubAptCacheHostDir(t, host)

	f := &fakeRunner{err: os.ErrPermission}
	p := &Provisioner{Lima: lima.New(f)}

	if err := p.harvestAptCache(context.Background(), "sandbar-base", io.Discard); err != nil {
		t.Fatalf("harvestAptCache must swallow a Copy failure, got error: %v", err)
	}
}

// TestSeedAndHarvestAptCache_RoundTripLeafName proves the load-bearing detail
// the package doc calls out: aptArchivesDirName is the SAME leaf name on both
// sides, so a harvest's output directory is exactly the next seed's input
// directory. If a future edit changes one without the other, the seed source
// path and the harvest destination path stop overlapping and the whole cache
// silently stops round-tripping.
func TestSeedAndHarvestAptCache_RoundTripLeafName(t *testing.T) {
	host := t.TempDir()
	stubAptCacheHostDir(t, host)

	f := &fakeRunner{}
	p := &Provisioner{Lima: lima.New(f)}

	if err := p.harvestAptCache(context.Background(), "sandbar-base", io.Discard); err != nil {
		t.Fatalf("harvestAptCache: %v", err)
	}
	// calls[0] is the sudo lock/partial clear; calls[1] is the copy, whose last
	// argv token is the destination.
	harvestDst := f.calls[1][len(f.calls[1])-1]

	// Simulate what a real harvest leaves behind: <host>/archives/*.deb.
	archivesDir := filepath.Join(host, aptArchivesDirName)
	if err := os.MkdirAll(archivesDir, 0o755); err != nil {
		t.Fatalf("simulate harvested content: %v", err)
	}
	if err := os.WriteFile(filepath.Join(archivesDir, "x.deb"), []byte("x"), 0o644); err != nil {
		t.Fatalf("simulate harvested content: %v", err)
	}

	f2 := &fakeRunner{}
	p2 := &Provisioner{Lima: lima.New(f2)}
	if err := p2.seedAptCache(context.Background(), "sandbar-base", io.Discard); err != nil {
		t.Fatalf("seedAptCache: %v", err)
	}
	if len(f2.calls) != 2 {
		t.Fatalf("seedAptCache made %d limactl calls after a harvest, want 2: %v", len(f2.calls), f2.calls)
	}
	// calls[0] is the push; its second-to-last argv token is the copy source
	// (the last token is the guest destination).
	seedSrc := f2.calls[0][len(f2.calls[0])-2]

	if harvestDst != host {
		t.Fatalf("harvest destination = %q, want %q", harvestDst, host)
	}
	if seedSrc != archivesDir {
		t.Fatalf("seed source = %q, want %q", seedSrc, archivesDir)
	}
	if filepath.Dir(seedSrc) != harvestDst {
		t.Fatalf("seed source %q is not inside harvest destination %q — the round trip is broken", seedSrc, harvestDst)
	}
}

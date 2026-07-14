//go:build !limae2e

package provision

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain isolates LIMA_HOME and the apt cache dir for the whole unit suite, so
// no test can write into the developer's real ~/.lima or user cache dir.
//
// It is not hypothetical. This package's tests drive the real provisioning
// orchestration over a fake lima.Runner, and that orchestration writes host state
// beside the Lima instances: the base image's playbook-version stamp, and now the
// base-image lock (baselock.go). The lock leaked into ~/.lima/_sand/ the first time
// its test ran. A fake Runner stops a test from RUNNING limactl; it does nothing
// about the files the code around it writes — the same lesson internal/ui's
// isolateHostState records, learned again here.
//
// The apt cache is isolated by STUBBING aptCacheHostDirFn, not by setting
// XDG_CACHE_HOME. Setting the env var is not enough, and is worst on the platform
// sand mostly runs on: os.UserCacheDir() honours XDG_CACHE_HOME on Linux, but on
// macOS it ignores it entirely and returns ~/Library/Caches. So on a Mac an
// env-var-only isolation leaves defaultAptCacheHostDir() pointing at the
// developer's REAL cache — harvestAptCache MkdirAll's into it, and seedAptCache
// finds whatever .deb files a previous real `sand` run harvested, issues an extra
// limactl copy/shell through the fake Runner, and fails provision_test.go's exact
// command-sequence assertions on their machine while passing in CI. The
// aptCacheHostDirFn seam exists for exactly this; use it, and keep XDG_CACHE_HOME
// set too for any other os.UserCacheDir() caller that may appear.
//
// The limae2e tests are excluded (build tag): they boot real VMs and need the real
// Lima home, which is the entire point of them.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sand-provision-test-lima-home-*")
	if err != nil {
		panic("isolate LIMA_HOME: " + err.Error())
	}
	if err := os.Setenv("LIMA_HOME", dir); err != nil {
		panic("isolate LIMA_HOME: " + err.Error())
	}
	cacheDir, err := os.MkdirTemp("", "sand-provision-test-cache-home-*")
	if err != nil {
		panic("isolate XDG_CACHE_HOME: " + err.Error())
	}
	if err := os.Setenv("XDG_CACHE_HOME", cacheDir); err != nil {
		panic("isolate XDG_CACHE_HOME: " + err.Error())
	}
	aptCacheHostDirFn = func() (string, error) {
		return filepath.Join(cacheDir, "sand", "apt-cache"), nil
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	_ = os.RemoveAll(cacheDir)
	os.Exit(code)
}

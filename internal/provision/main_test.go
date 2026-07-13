//go:build !limae2e

package provision

import (
	"os"
	"testing"
)

// TestMain isolates LIMA_HOME for the whole unit suite, so no test can write into
// the developer's real ~/.lima.
//
// It is not hypothetical. This package's tests drive the real provisioning
// orchestration over a fake lima.Runner, and that orchestration writes host state
// beside the Lima instances: the base image's playbook-version stamp, and now the
// base-image lock (baselock.go). The lock leaked into ~/.lima/_sand/ the first time
// its test ran. A fake Runner stops a test from RUNNING limactl; it does nothing
// about the files the code around it writes — the same lesson internal/ui's
// isolateHostState records, learned again here.
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
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

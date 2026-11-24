package sshx

import (
	"path/filepath"
	"slices"
	"testing"
)

// TestStopMasterArgv: the stop must reach the master every other command shares
// (the same ControlPath and target), ask it to stop rather than exit, and be
// skipped when there is no control directory and so no master.
func TestStopMasterArgv(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	c := New(Config{Host: "example.com", User: "dev", Port: 2222})
	argv := c.StopMasterArgv()

	if !hasToken(argv, "ControlPath="+filepath.Join(c.controlDir, "%C")) {
		t.Errorf("argv lacks the shared ControlPath: %q", argv)
	}
	// %C hashes the port and user, so they must match the commands' own.
	if i := slices.Index(argv, "-p"); i < 0 || argv[i+1] != "2222" {
		t.Errorf("argv lacks the connection's port: %q", argv)
	}
	n := len(argv)
	if n < 3 || argv[n-3] != "-O" || argv[n-2] != "stop" || argv[n-1] != c.Target() {
		t.Errorf("argv does not end `-O stop %s`: %q", c.Target(), argv)
	}

	if got := (&Conn{cfg: testCfg}).StopMasterArgv(); got != nil {
		t.Errorf("StopMasterArgv without a control dir = %q, want nil", got)
	}
}

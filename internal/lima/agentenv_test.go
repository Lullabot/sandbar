package lima

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGuestAttachExprMarksAgentEnvironment pins the marker command to the front
// of every attach expression, in both client modes. It must run before tmux, not
// inside one branch, or a VM whose `main` session already exists, the common
// case for a VM provisioned before the marker existed, would never get it.
func TestGuestAttachExprMarksAgentEnvironment(t *testing.T) {
	for _, mode := range []AttachMode{AttachFullScreen, AttachControl} {
		argv := GuestAttachArgvMode("truecolor", mode)
		if expr := argv[len(argv)-1]; !strings.HasPrefix(expr, agentEnvironmentCmd+"if tmux has-session") {
			t.Errorf("mode %d: attach expression does not start with the agent-environment marker command:\n%s", mode, expr)
		}
	}
	if strings.Contains(agentEnvironmentCmd, "'") {
		t.Errorf("agentEnvironmentCmd contains a single quote, which the ssh transports' quoting would mangle: %s", agentEnvironmentCmd)
	}
}

// sudoNonInteractive is a fake sudo that insists on -n, so a command that could
// prompt for a password fails the test instead of passing it.
const sudoNonInteractive = `[ "$1" = -n ] || exit 1; shift; exec "$@"` + "\n"

// runAgentEnvCmd runs agentEnvironmentCmd in a real bash, with the marker at
// marker instead of /etc and `sudo` replaced by sudoScript, followed by `echo
// attached` to stand in for the tmux branch. It returns stdout.
func runAgentEnvCmd(t *testing.T, marker, sudoScript string) string {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte("#!/bin/sh\n"+sudoScript), 0o755); err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(agentEnvironmentCmd, agentEnvironmentMarker, marker) + "echo attached"
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("marker command failed the attach: %v", err)
	}
	return string(out)
}

func TestAgentEnvironmentCmdCreatesEmptyMarker(t *testing.T) {
	// The parent directory does not exist yet, as on a VM that predates it.
	marker := filepath.Join(t.TempDir(), "etc", "lullabot", "agent-environment")
	if out := runAgentEnvCmd(t, marker, sudoNonInteractive); out != "attached\n" {
		t.Errorf("stdout = %q, want only the attach's own output: -CC reads stdout as protocol", out)
	}
	info, err := os.Stat(marker)
	if err != nil {
		t.Fatalf("marker was not created: %v", err)
	}
	if info.Size() != 0 || info.Mode().Perm() != 0o644 {
		t.Errorf("marker size %d mode %v, want an empty 0644 file", info.Size(), info.Mode().Perm())
	}
}

func TestAgentEnvironmentCmdNeverBlocksAttach(t *testing.T) {
	// A sudo that needs a password fails under -n, loudly. The attach must
	// still go ahead, and the error must not reach stdout.
	marker := filepath.Join(t.TempDir(), "agent-environment")
	out := runAgentEnvCmd(t, marker, "echo 'sudo: a password is required' >&2; exit 1\n")
	if out != "attached\n" {
		t.Errorf("stdout = %q, want the attach to proceed with nothing else printed", out)
	}
}

func TestAgentEnvironmentCmdSkipsSudoOnceMarked(t *testing.T) {
	// An existing marker must short-circuit the privileged call, so a marked VM
	// pays only for the test. This sudo announces itself if it is ever run.
	marker := filepath.Join(t.TempDir(), "agent-environment")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if out := runAgentEnvCmd(t, marker, "echo SUDO-CALLED\n"); out != "attached\n" {
		t.Errorf("stdout = %q: sudo ran although the marker already existed", out)
	}
}

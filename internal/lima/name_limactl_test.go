package lima

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestValidateInstanceNameAgainstRealLimactl is the standing guard that
// ValidateInstanceName still says what limactl says.
//
// The rule it mirrors is not published anywhere in Lima's documentation — it was
// read off the error limactl prints — so a transcribed regexp is a snapshot of
// one version's behaviour and nothing more. If a future Lima widens or narrows
// the accepted set, the cost of not noticing is asymmetric and both halves are
// bad: too strict and sand refuses a name Lima would have taken, too loose and
// the check silently stops doing its job and the failure goes back to arriving
// mid-clone.
//
// The oracle costs no VM: `limactl create --name=<n> -` reads a deliberately
// incomplete template from stdin, so a name limactl ACCEPTS gets as far as
// "field `images` must be set" while one it refuses never reaches the template
// at all. Nothing is created either way — verified by asserting the temp
// LIMA_HOME is still empty afterwards.
//
// Length is deliberately NOT part of the comparison: limactl's ceiling is
// whatever leaves `<LIMA_HOME>/<name>/ssh.sock.<16 digits>` under UNIX_PATH_MAX,
// so it moves with the home directory and is shorter under a test's temp dir
// than under a real one. MaxInstanceNameLen's fixed, stricter cap exists
// precisely because that number is not a constant.
//
// It skips (rather than fails) when limactl is absent, so `go test ./...` stays
// green on a machine without Lima — the same bargain
// TestLimactlToleratesProvenanceMarkerAgainstRealLimactl strikes.
func TestValidateInstanceNameAgainstRealLimactl(t *testing.T) {
	limactl, err := exec.LookPath("limactl")
	if err != nil {
		t.Skip("limactl not on PATH; skipping the real-limactl naming guard")
	}

	limaHome := t.TempDir()
	t.Setenv("LIMA_HOME", limaHome)

	for _, name := range []string{
		"web", "dev-box", "dev.box", "test_vm", "1vm", "a", "Test-VM", "a-b_c.d-e",
		"test--vm", "-vm", "vm-", ".vm", "my vm", "vm/../etc", "vm:1", "naïve",
	} {
		cmd := exec.Command(limactl, "create", "--name="+name, "-")
		cmd.Stdin = strings.NewReader("cpus: 1\n")
		cmd.Env = append(cmd.Environ(), "LIMA_HOME="+limaHome)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("the stub template was accepted for %q; this test's oracle no longer holds", name)
		}
		// limactl prefixes every name complaint with "instance name `<n>`" and
		// reaches the template only once the name is past it.
		limactlRefused := strings.Contains(string(out), "instance name `")

		ourErr := ValidateInstanceName(name)
		switch {
		case limactlRefused && ourErr == nil:
			t.Errorf("limactl refuses %q but ValidateInstanceName accepts it; limactl said: %s", name, out)
		case !limactlRefused && ourErr != nil:
			t.Errorf("limactl accepts %q but ValidateInstanceName refuses it: %v", name, ourErr)
		}
	}

	// A refused name must also have left nothing behind — the oracle is only
	// cheap if it really does create nothing.
	entries, err := os.ReadDir(limaHome)
	if err != nil {
		t.Fatalf("read temp LIMA_HOME: %v", err)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "_") { // Lima's own _config/_networks bookkeeping
			t.Errorf("limactl left %q behind in LIMA_HOME; the oracle is creating instances", e.Name())
		}
	}
}

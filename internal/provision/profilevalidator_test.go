package provision

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	sandbar "github.com/lullabot/sandbar"
)

// validatorScriptPath extracts the embedded playbook fileset and returns the
// path to the embedded provisioning-profile manifest validator
// (scripts/validate_profile.py), so this test exercises the exact artifact
// the guest invokes in production rather than a copy of it living only on the
// CI runner.
func validatorScriptPath(t *testing.T) string {
	t.Helper()
	dir, err := extractEmbedded(sandbar.PlaybookFS)
	if err != nil {
		t.Fatalf("extract embedded playbook: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	p := filepath.Join(dir, "scripts", "validate_profile.py")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("embedded validator script missing at scripts/validate_profile.py: %v", err)
	}
	return p
}

// runValidator invokes the validator script against a manifest fixture and
// returns its stdout, stderr, and exit code.
func runValidator(t *testing.T, manifestPath string) (stdout, stderr string, exitCode int) {
	t.Helper()
	script := validatorScriptPath(t)
	cmd := exec.Command("python3", script, manifestPath)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return outBuf.String(), errBuf.String(), ee.ExitCode()
		}
		t.Fatalf("run validator: %v", err)
	}
	return outBuf.String(), errBuf.String(), 0
}

// TestValidateProfileManifestCorpus runs the standalone provisioning-profile
// manifest validator (scripts/validate_profile.py) against a corpus of valid
// and deliberately-malformed fixtures under testdata/profile-manifests/,
// asserting the validator's public exit-code and message contract: 0 for a
// valid manifest, non-zero with the offending key/field named in stderr for
// each malformed one.
func TestValidateProfileManifestCorpus(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}

	cases := []struct {
		name          string
		fixture       string
		wantExit      int
		wantErrSubstr string // checked against stderr; empty means "don't check"
	}{
		{
			name:     "valid manifest",
			fixture:  "valid.yml",
			wantExit: 0,
		},
		{
			name:          "unknown top-level key",
			fixture:       "bad-unknown-key.yml",
			wantExit:      1,
			wantErrSubstr: "toolz",
		},
		{
			name:          "bad package name",
			fixture:       "bad-package-name.yml",
			wantExit:      1,
			wantErrSubstr: "'packages': 'Bad_Package'",
		},
		{
			name:          "bad service unit name",
			fixture:       "bad-service-name.yml",
			wantExit:      1,
			wantErrSubstr: "'services': 'not-a-unit'",
		},
		{
			name:          "bad toolset name",
			fixture:       "bad-toolset-name.yml",
			wantExit:      1,
			wantErrSubstr: "'toolset': 'docker'",
		},
		{
			name:          "bad seed path",
			fixture:       "bad-seed-path.yml",
			wantExit:      1,
			wantErrSubstr: "'seed': path '/etc/passwd'",
		},
		{
			name:          "bad role name (path traversal)",
			fixture:       "bad-role-name.yml",
			wantExit:      1,
			wantErrSubstr: "'roles': '../../etc/evil'",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := filepath.Join("testdata", "profile-manifests", tc.fixture)
			stdout, stderr, code := runValidator(t, manifest)
			if code != tc.wantExit {
				t.Fatalf("exit code = %d, want %d\nstdout: %s\nstderr: %s", code, tc.wantExit, stdout, stderr)
			}
			if tc.wantErrSubstr != "" && !strings.Contains(stderr, tc.wantErrSubstr) {
				t.Fatalf("stderr = %q, want substring %q", stderr, tc.wantErrSubstr)
			}
		})
	}
}

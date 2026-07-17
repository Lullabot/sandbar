package provision

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	sandbar "github.com/lullabot/sandbar"
)

// shippedProfileTools is the set of shipped provisioning-profile names this
// task (strikethroo plan 18, task 02) restructured the optional dev tools
// into. Kept in lockstep with vm.CreateConfig.ToolPtrs()'s four tool names
// and scripts/validate_profile.py's KNOWN_TOOLSETS — all three must name the
// same four tools.
var shippedProfileTools = []string{"claude", "ddev", "go", "java"}

// TestShippedProfileManifestsValidate runs the real embedded manifest
// validator (scripts/validate_profile.py) against each shipped profile's
// embedded manifest (shipped-profiles/<tool>/profile.yml), proving the
// restructured claude/ddev/go/java tools are expressed in the exact
// declarative manifest format Task 1 defined for repo-checked-in profiles —
// the "shipped profiles double as tested reference examples" property this
// task exists to deliver.
func TestShippedProfileManifestsValidate(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}

	dir, err := extractEmbedded(sandbar.PlaybookFS)
	if err != nil {
		t.Fatalf("extract embedded playbook: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	for _, tool := range shippedProfileTools {
		t.Run(tool, func(t *testing.T) {
			manifest := filepath.Join(dir, "shipped-profiles", tool, "profile.yml")
			if _, err := os.Stat(manifest); err != nil {
				t.Fatalf("shipped profile manifest missing for %q: %v", tool, err)
			}
			stdout, stderr, code := runValidator(t, manifest)
			if code != 0 {
				t.Fatalf("shipped profile manifest for %q failed validation (exit %d)\nstdout: %s\nstderr: %s", tool, code, stdout, stderr)
			}
		})
	}
}

// TestShippedProfileRolesExistUnderRolesPath proves the embedded role content
// a shipped profile's manifest declares under `roles:` is actually present at
// shipped-profiles/roles/<name> — the location ansible.cfg's roles_path adds
// so `role: claude-code` (site.yml) and `import_role: name: ddev`
// (roles/base/tasks/main.yml) keep resolving after the reorganization moved
// their task content out of the top-level roles/ directory.
func TestShippedProfileRolesExistUnderRolesPath(t *testing.T) {
	dir, err := extractEmbedded(sandbar.PlaybookFS)
	if err != nil {
		t.Fatalf("extract embedded playbook: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	for _, name := range []string{"claude-code", "ddev"} {
		taskFile := filepath.Join(dir, "shipped-profiles", "roles", name, "tasks", "main.yml")
		if _, err := os.Stat(taskFile); err != nil {
			t.Errorf("shipped-profiles role %q missing tasks/main.yml: %v", name, err)
		}
	}
}

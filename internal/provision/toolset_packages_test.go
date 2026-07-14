package provision

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// resolveBasePackages runs the REAL role defaults (roles/base/defaults/main.yml
// and roles/dev-tools/defaults/main.yml) through ansible-playbook's Jinja
// engine and returns the package list roles/base/tasks/main.yml's single
// consolidated apt transaction would install, given extraVars on top of those
// defaults. It exercises the actual templated expressions (toolset_packages,
// devtools_ddev_packages) rather than a Go reimplementation of them, so an
// edit to the conditional itself is caught here too, not just a Go-side
// mirror of it.
func resolveBasePackages(t *testing.T, extraVars map[string]string) []string {
	t.Helper()
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		t.Skip("ansible-playbook not installed")
	}

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	dir := t.TempDir()
	outFile := filepath.Join(dir, "packages.json")
	playbookPath := filepath.Join(dir, "resolve.yml")

	playbook := fmt.Sprintf(`---
- hosts: localhost
  connection: local
  gather_facts: false
  vars_files:
    - %s
    - %s
  tasks:
    - name: dump resolved package list
      ansible.builtin.copy:
        dest: %s
        content: "{{ (base_packages + base_nodejs_packages + devtools_docker_packages + devtools_packages + devtools_ddev_packages + toolset_packages) | to_json }}"
`,
		filepath.Join(repoRoot, "roles/base/defaults/main.yml"),
		filepath.Join(repoRoot, "roles/dev-tools/defaults/main.yml"),
		outFile,
	)
	if err := os.WriteFile(playbookPath, []byte(playbook), 0o644); err != nil {
		t.Fatalf("write temp playbook: %v", err)
	}

	args := []string{"-i", "localhost,", "--connection=local"}
	for k, v := range extraVars {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, playbookPath)

	cmd := exec.Command("ansible-playbook", args...)
	cmd.Dir = repoRoot // pick up the repo's ansible.cfg
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ansible-playbook failed: %v\n%s", err, out)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read resolved package list: %v", err)
	}
	var pkgs []string
	if err := json.Unmarshal(data, &pkgs); err != nil {
		t.Fatalf("resolved package list is not valid JSON: %v\n%s", err, data)
	}
	return pkgs
}

// TestBaseToolsetPackages_DefaultSelectionMatchesToday is the backwards-
// compatibility acceptance criterion for this task: with no tool-set
// extra-vars supplied (mirroring an unconfigured `sand create`, whose
// vm.DefaultCreateConfig() selects all three tools), the packages Ansible
// actually resolves for the base phase must still contain ddev, golang, and
// default-jdk-headless — exactly what base_packages installed
// unconditionally before golang and default-jdk-headless were pulled out of
// it and ddev's install was made conditional.
func TestBaseToolsetPackages_DefaultSelectionMatchesToday(t *testing.T) {
	pkgs := resolveBasePackages(t, nil)

	for _, want := range []string{"ddev", "golang", "default-jdk-headless"} {
		if !slices.Contains(pkgs, want) {
			t.Errorf("resolved package list missing %q with the default (all-on) tool-set — backwards compatibility broken: %v", want, pkgs)
		}
	}
}

// TestBaseToolsetPackages_DeselectingOmitsThePackage proves the other half of
// the wiring: turning a toolset_* var off actually removes the corresponding
// package from what gets installed, not just from base_packages' unconditional
// list.
func TestBaseToolsetPackages_DeselectingOmitsThePackage(t *testing.T) {
	pkgs := resolveBasePackages(t, map[string]string{
		"toolset_ddev": "false",
		"toolset_go":   "false",
		"toolset_java": "false",
	})

	for _, unwanted := range []string{"ddev", "golang", "default-jdk-headless"} {
		if slices.Contains(pkgs, unwanted) {
			t.Errorf("resolved package list still contains %q with every toolset_* var false: %v", unwanted, pkgs)
		}
	}
}

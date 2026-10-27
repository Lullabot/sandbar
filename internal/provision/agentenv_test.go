package provision

import (
	"testing"

	sandbar "github.com/lullabot/sandbar"
	"gopkg.in/yaml.v3"
)

// TestAgentEnvironmentMarkerDeployedInEveryPhase guards the `when:` that is
// NOT on roles/base's /etc/lullabot/agent-environment tasks, for the reason
// TestTmuxConfDeployedInEveryPhase guards ~/.tmux.conf: gating them out of
// finalize looks consistent with their neighbours, and a clone of a current
// base still gets the file, but a clone of an older base would not. The
// molecule scenario cannot catch this. Its finalize pass runs on a container
// the base pass already marked, so the file is there either way.
//
// The tasks are found by path, not name, so renaming one cannot hide it from
// this test.
func TestAgentEnvironmentMarkerDeployedInEveryPhase(t *testing.T) {
	const tasksPath = "roles/base/tasks/main.yml"
	raw, err := sandbar.PlaybookFS.ReadFile(tasksPath)
	if err != nil {
		t.Fatalf("read %s from the embedded playbook: %v", tasksPath, err)
	}
	var tasks []map[string]any
	if err := yaml.Unmarshal(raw, &tasks); err != nil {
		t.Fatalf("parse %s: %v", tasksPath, err)
	}

	paths := map[string]string{
		"/etc/lullabot":                   "ansible.builtin.file",
		"/etc/lullabot/agent-environment": "ansible.builtin.copy",
	}
	for _, task := range tasks {
		for path, module := range paths {
			args, _ := task[module].(map[string]any)
			if args["path"] != path && args["dest"] != path {
				continue
			}
			delete(paths, path)
			if when, ok := task["when"]; ok {
				t.Errorf("the %q task in %s is gated by `when: %v`. If that gate excludes the finalize phase,"+
					" VMs cloned from a base built before the marker existed never get %s, and Lullabot's"+
					" Claude Code policy keeps prompting in them.", task["name"], tasksPath, when, path)
			}
			if module == "ansible.builtin.copy" && args["content"] != "" {
				t.Errorf("the marker task writes content %q; the policy only tests that the file exists,"+
					" so it stays empty", args["content"])
			}
		}
	}
	for path, module := range paths {
		t.Errorf("no %s task for %s in %s. Task names:\n%s", module, path, tasksPath, taskNames(tasks))
	}

	// A gate on the role in site.yml would skip the tasks just as surely as
	// one on the tasks themselves.
	site, err := sandbar.PlaybookFS.ReadFile("site.yml")
	if err != nil {
		t.Fatalf("read site.yml from the embedded playbook: %v", err)
	}
	var plays []struct {
		Roles []any `yaml:"roles"`
	}
	if err := yaml.Unmarshal(site, &plays); err != nil {
		t.Fatalf("parse site.yml: %v", err)
	}
	var found bool
	for _, play := range plays {
		for _, role := range play.Roles {
			switch r := role.(type) {
			case string:
				found = found || r == "base"
			case map[string]any:
				if r["role"] == "base" {
					found = true
					if when, ok := r["when"]; ok {
						t.Errorf("site.yml gates the base role with `when: %v`, which also gates the"+
							" agent-environment marker out of some phases", when)
					}
				}
			}
		}
	}
	if !found {
		t.Error("site.yml no longer runs the base role, so no VM gets /etc/lullabot/agent-environment")
	}
}

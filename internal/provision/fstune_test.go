package provision

import (
	"fmt"
	"strings"
	"testing"

	sandbar "github.com/lullabot/sandbar"
	"gopkg.in/yaml.v3"
)

// The two filesystem tunings in roles/base are the kind that fail silently and
// invisibly: nothing errors, nothing looks different, the guest is simply slower
// or fuller than it needed to be. Neither is covered by the molecule suite in a
// way that would catch the mistakes below (a container has no root filesystem to
// remount and no systemd to enable a timer on), so these read the embedded
// playbook directly — the same approach, for the same reason, as
// tmuxconf_test.go.
const baseTasksPath = "roles/base/tasks/main.yml"

// TestFilesystemTuningRunsInEveryPhase guards the `when: provision_phase != …`
// gate that is NOT on these tasks. Adding it to a task in roles/base looks like
// consistency — most of that file's tasks carry it, because installing packages
// twice is waste — and it costs nothing visible, since a VM cloned from a CURRENT
// base image still comes out right. What breaks is every VM cloned from an OLDER
// base (up to 30 days, per base_apt_upgrade's self-refresh) and every VM that
// already exists: those reach the playbook ONLY through finalize, so a gated
// tuning is one they can never pick up, however many times they are reset. The
// tasks are two lines of config apiece; re-paying them per clone is the cheaper
// side of that trade.
func TestFilesystemTuningRunsInEveryPhase(t *testing.T) {
	tasks := baseTasks(t)

	for _, want := range []string{"noatime", "fstrim"} {
		found := false
		for _, task := range tasks {
			name, _ := task["name"].(string)
			if !strings.Contains(strings.ToLower(name), want) {
				continue
			}
			found = true
			when, ok := task["when"]
			if !ok {
				continue
			}
			if cond := fmt.Sprintf("%v", when); strings.Contains(cond, "provision_phase") {
				t.Errorf("the %q task in %s is gated by `when: %v`.\nA phase gate here means an existing VM —"+
					" which only ever reaches this playbook through finalize — can never pick the setting up,"+
					" no matter how often it is reset.", name, baseTasksPath, when)
			}
		}
		if !found {
			t.Errorf("no task mentioning %q in %s. Task names:\n%s", want, baseTasksPath, taskNames(tasks))
		}
	}
}

// TestNoatimeFstabEditIsIdempotent pins the negative lookahead in the fstab
// regexp, which is the whole difference between a tuning and a corruption.
//
// ansible.builtin.replace rewrites every line its pattern matches, every run.
// Without the lookahead the pattern keeps matching the line it has already
// edited, so each converge appends another ",noatime" to the root entry's option
// field — and this playbook runs again on every create and every reset of every
// clone. The lookahead cannot be evaluated here (Go's regexp is RE2 and has no
// lookahead at all, which is precisely why this is a string assertion and not a
// behavioural one), so what is checked is that it is still there.
func TestNoatimeFstabEditIsIdempotent(t *testing.T) {
	for _, task := range baseTasks(t) {
		name, _ := task["name"].(string)
		if !strings.Contains(name, "noatime") {
			continue
		}
		replace, ok := task["ansible.builtin.replace"].(map[string]any)
		if !ok {
			continue
		}
		regexp, _ := replace["regexp"].(string)
		if !strings.Contains(regexp, "(?!") || !strings.Contains(regexp, "noatime") {
			t.Fatalf("the %q task's regexp is %q, which no longer refuses a line that already carries noatime."+
				" This playbook runs again on every create and every reset, so without that guard each run"+
				" appends another \",noatime\" to the root entry in /etc/fstab.", name, regexp)
		}
		// Field 2 of an fstab line is the mount point. Anchoring on "/" is what
		// keeps this to the ROOT entry instead of every filesystem in the file.
		if !strings.Contains(regexp, `\s+/\s+`) {
			t.Errorf("the %q task's regexp is %q, which no longer anchors on the root mount point;"+
				" it would rewrite every entry in /etc/fstab.", name, regexp)
		}
		return
	}
	t.Fatalf("no fstab noatime task in %s", baseTasksPath)
}

// baseTasks parses roles/base's task list out of the embedded playbook.
func baseTasks(t *testing.T) []map[string]any {
	t.Helper()
	raw, err := sandbar.PlaybookFS.ReadFile(baseTasksPath)
	if err != nil {
		t.Fatalf("read %s from the embedded playbook: %v", baseTasksPath, err)
	}
	var tasks []map[string]any
	if err := yaml.Unmarshal(raw, &tasks); err != nil {
		t.Fatalf("parse %s: %v", baseTasksPath, err)
	}
	return tasks
}

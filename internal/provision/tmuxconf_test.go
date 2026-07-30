package provision

import (
	"fmt"
	"strings"
	"testing"

	sandbar "github.com/lullabot/sandbar"
	"gopkg.in/yaml.v3"
)

// The guest's ~/.tmux.conf is the only place two settings live that decide
// whether a copy made inside a VM ever reaches the user's clipboard, and both
// fail SILENTLY when absent — a selection is made, no error appears anywhere,
// and nothing lands. Neither is covered by the molecule suite (which converges
// `base` and `samba`, not `user`), so these read the embedded playbook directly.

const tmuxConfPath = "roles/user/templates/tmux.conf.j2"

// TestTmuxConfEnablesTheClipboard pins both halves of the OSC 52 bridge. They
// are useless individually: `set-clipboard on` alone emits nothing at all when
// TERM is screen-256color or tmux-256color — which is what a guest reached from
// inside a host tmux sees, i.e. sand's own host-tmux fast path — and the
// terminal feature alone only forwards sequences that programs inside tmux
// emit, never tmux's own copies.
func TestTmuxConfEnablesTheClipboard(t *testing.T) {
	conf, err := sandbar.PlaybookFS.ReadFile(tmuxConfPath)
	if err != nil {
		t.Fatalf("read %s from the embedded playbook: %v", tmuxConfPath, err)
	}
	body := string(conf)

	for _, want := range []string{
		"set -s set-clipboard on",
		"terminal-features",
		":clipboard",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("%s does not contain %q. Without both the `set-clipboard on` and the `clipboard`"+
				" terminal-feature lines, copying text inside the guest silently puts nothing on the user's"+
				" clipboard — see internal/lima.clipboardCmds for the measurements.", tmuxConfPath, want)
		}
	}
}

// TestTmuxConfDeployedInEveryPhase guards the `when:` that is NOT on the
// "Deploy ~/.tmux.conf" task. Its identity-free neighbours in roles/user all
// carry `when: provision_phase != 'finalize'` — copying that line onto this
// task is a one-word edit that looks consistent and costs nothing visible,
// because a VM cloned from a CURRENT base image still comes out right. What
// breaks is a VM cloned from an OLDER base (up to 30 days, per base_apt_upgrade's
// self-refresh): it silently keeps whatever ~/.tmux.conf that base was built
// with. The same reasoning, and the same guard, as roles/base's timezone tasks
// (molecule/base/converge.yml).
func TestTmuxConfDeployedInEveryPhase(t *testing.T) {
	const tasksPath = "roles/user/tasks/main.yml"
	raw, err := sandbar.PlaybookFS.ReadFile(tasksPath)
	if err != nil {
		t.Fatalf("read %s from the embedded playbook: %v", tasksPath, err)
	}

	var tasks []map[string]any
	if err := yaml.Unmarshal(raw, &tasks); err != nil {
		t.Fatalf("parse %s: %v", tasksPath, err)
	}

	var found bool
	for _, task := range tasks {
		name, _ := task["name"].(string)
		if !strings.Contains(name, ".tmux.conf") {
			continue
		}
		found = true
		if when, ok := task["when"]; ok {
			t.Errorf("the %q task in %s is gated by `when: %v`.\nIf that gate excludes the finalize phase,"+
				" every VM cloned from a base image built before the current tmux.conf.j2 keeps the OLD config —"+
				" silently, since a stale tmux config is wrong rather than broken. Rendering this template is cheap"+
				" enough to re-pay per clone; that is why it is ungated.", name, tasksPath, when)
		}
		if src, _ := task["ansible.builtin.template"].(map[string]any)["src"]; src != "tmux.conf.j2" {
			t.Errorf("the %q task no longer templates tmux.conf.j2 (src = %v); %s asserts on the wrong file",
				name, src, tmuxConfPath)
		}
	}
	if !found {
		t.Fatalf("no task deploying ~/.tmux.conf in %s. Task names:\n%s", tasksPath, taskNames(tasks))
	}
}

func taskNames(tasks []map[string]any) string {
	var b strings.Builder
	for _, task := range tasks {
		fmt.Fprintf(&b, "\t%v\n", task["name"])
	}
	return b.String()
}

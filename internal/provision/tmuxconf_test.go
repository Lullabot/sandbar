package provision

import (
	"fmt"
	"strings"
	"testing"

	sandbar "github.com/lullabot/sandbar"
	"gopkg.in/yaml.v3"
)

// The guest's /etc/tmux.conf carries two settings that decide
// whether a copy made inside a VM ever reaches the user's clipboard, and both
// fail SILENTLY when absent — a selection is made, no error appears anywhere,
// and nothing lands. Neither is covered by the molecule suite (which converges
// `base` and `samba`, not `user`), so these read the embedded playbook directly.

const tmuxConfPath = "roles/user/templates/tmux.conf.j2"

// TestTmuxConfEnablesTheClipboard pins both halves of the OSC 52 bridge. They
// together support tmux and application copies. `set-clipboard on` alone
// emits nothing at all when
// TERM is screen-256color or tmux-256color — which is what a guest reached from
// inside a host tmux sees, i.e. sand's own host-tmux fast path — and the
// terminal feature alone does not allow applications inside tmux to copy.
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
				" clipboard — see internal/guestsh.terminalSetupCmds for the measurements.", tmuxConfPath, want)
		}
	}
}

// TestTmuxConfDeployedInEveryPhase guards the `when:` that is NOT on the task
// rendering tmux.conf.j2 to /etc/tmux.conf. Its identity-free neighbours in
// roles/user all carry `when: provision_phase != 'finalize'` — copying that
// line onto this task is a one-word edit that looks consistent and costs
// nothing visible, because a VM cloned from a CURRENT base image still comes out
// right. What breaks is a VM cloned from an OLDER base (up to 30 days, per
// base_apt_upgrade's self-refresh): it silently keeps whatever tmux config that
// base was built with. The same reasoning, and the same guard, as roles/base's
// timezone tasks (molecule/base/converge.yml).
//
// The task is found by its template, not its name, so a rename cannot hide it.
// It must write outside the home: a preserved home brings back whatever file
// it held, and the safe home writer then keeps a legacy copy forever.
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
		if !strings.Contains(fmt.Sprint(task), "tmux.conf.j2") {
			continue
		}
		found = true
		if when, ok := task["when"]; ok {
			t.Errorf("the %q task in %s is gated by `when: %v`.\nIf that gate excludes the finalize phase,"+
				" every VM cloned from a base image built before the current tmux.conf.j2 keeps the OLD config —"+
				" silently, since a stale tmux config is wrong rather than broken. Rendering this template is cheap"+
				" enough to re-pay per clone; that is why it is ungated.", name, tasksPath, when)
		}
		args, ok := task["ansible.builtin.template"].(map[string]any)
		if !ok || args["src"] != "tmux.conf.j2" || args["dest"] != "{{ sand_system_tmux_conf }}" {
			t.Errorf("%q must render tmux.conf.j2 to sand_system_tmux_conf, outside the home: %v", name, task)
		}
	}
	if !found {
		t.Fatalf("no task rendering tmux.conf.j2 in %s. Task names:\n%s", tasksPath, taskNames(tasks))
	}

	const defaultsPath = "roles/user/defaults/main.yml"
	raw, err = sandbar.PlaybookFS.ReadFile(defaultsPath)
	if err != nil {
		t.Fatalf("read %s from the embedded playbook: %v", defaultsPath, err)
	}
	var defaults map[string]any
	if err := yaml.Unmarshal(raw, &defaults); err != nil {
		t.Fatalf("parse %s: %v", defaultsPath, err)
	}
	// tmux's compiled-in system path on Debian, and the path the template's
	// reload binding names.
	if got := defaults["sand_system_tmux_conf"]; got != "/etc/tmux.conf" {
		t.Errorf("sand_system_tmux_conf = %v, want /etc/tmux.conf", got)
	}
}

func taskNames(tasks []map[string]any) string {
	var b strings.Builder
	for _, task := range tasks {
		fmt.Fprintf(&b, "\t%v\n", task["name"])
	}
	return b.String()
}

// Both direct terminals and nested host tmux must negotiate modified keys.
func TestTmuxConfClaudeTerminalSettings(t *testing.T) {
	conf, err := sandbar.PlaybookFS.ReadFile(tmuxConfPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"set -g allow-passthrough on", "set -s extended-keys on",
		"set -g focus-events on", "xterm*:extkeys", "screen*:extkeys", "tmux*:extkeys",
	} {
		if !strings.Contains(string(conf), want) {
			t.Errorf("missing %q in guest config", want)
		}
	}
}

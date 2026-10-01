package lima

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	sandbar "github.com/lullabot/sandbar"
)

// Exercise the embedded template and attach-time commands against an isolated
// real server. No VM or user's tmux socket/configuration is used.
func TestTerminalSetupWithRealTmux(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	version, err := exec.Command(binary, "-V").Output()
	if err != nil {
		t.Fatal(err)
	}
	var major, minor int
	if _, err := fmt.Sscanf(string(version), "tmux %d.%d", &major, &minor); err != nil {
		t.Fatalf("parse tmux version %q: %v", version, err)
	}
	if major < 3 || (major == 3 && minor < 3) {
		t.Skip("terminal passthrough requires tmux 3.3 or newer")
	}
	for _, legacy := range []bool{false, true} {
		name := "provisioned"
		if legacy {
			name = "existing legacy server"
		}
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", home)
			t.Setenv("TMUX", "")
			socket := filepath.Join(home, "tmux.sock")
			conf, err := sandbar.PlaybookFS.ReadFile("roles/user/templates/tmux.conf.j2")
			if err != nil {
				t.Fatal(err)
			}
			if legacy {
				conf = nil
			}
			config := filepath.Join(home, "tmux.conf")
			if err := os.WriteFile(config, conf, 0600); err != nil {
				t.Fatal(err)
			}
			run := func(args ...string) string {
				t.Helper()
				out, err := exec.Command(binary, append([]string{"-S", socket, "-f", config}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("tmux %v: %v\n%s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			t.Cleanup(func() { _ = exec.Command(binary, "-S", socket, "kill-server").Run() })
			run("new-session", "-d", "-s", "main", "sleep 60")
			pid := run("display-message", "-p", "-t", "main", "#{pane_pid}")
			if !legacy {
				if got := run("show-options", "-gwv", "allow-passthrough"); got != "on" {
					t.Fatalf("provisioned passthrough = %q", got)
				}
				if got := run("show-options", "-sv", "extended-keys"); got != "on" {
					t.Fatalf("provisioned extended keys = %q", got)
				}
				features := run("show-options", "-s", "terminal-features")
				for _, pattern := range []string{"xterm*:extkeys", "screen*:extkeys", "tmux*:extkeys"} {
					if !strings.Contains(features, pattern) {
						t.Errorf("provisioned config missing %q", pattern)
					}
				}
			}
			if legacy {
				// Ensure the attach path, rather than the template, repairs these.
				run("set", "-g", "allow-passthrough", "off")
				run("set", "-s", "extended-keys", "off")
				run("set", "-s", "focus-events", "off")
			}
			// Convert the shell's escaped command separators into argv separators.
			// The settings contain no expansions; Fields handles their fixed words.
			args := strings.Fields(strings.ReplaceAll(terminalSetupCmds, `\;`, ";"))
			for i := range args {
				args[i] = strings.Trim(args[i], `"`)
			}
			args = append(args, "new-session", "-d", "-t", "=main", "-s", "check")
			run(args...)
			features := run("show-options", "-s", "terminal-features")
			for _, pattern := range []string{"*:clipboard", "xterm*:extkeys", "screen*:extkeys", "tmux*:extkeys"} {
				if !strings.Contains(features, pattern) {
					t.Errorf("missing terminal feature %q: %s", pattern, features)
				}
			}
			run("kill-session", "-t", "check")
			run(args...)
			if got := run("show-options", "-s", "terminal-features"); got != features {
				t.Errorf("reattach changed terminal features:\n%s\n%s", features, got)
			}
			for _, option := range []struct{ scope, name string }{
				{"-gwv", "allow-passthrough"}, {"-sv", "extended-keys"},
				{"-sv", "focus-events"}, {"-sv", "set-clipboard"},
			} {
				if got := run("show-options", option.scope, option.name); got != "on" {
					t.Errorf("%s = %q, want on", option.name, got)
				}
			}
			if got := run("display-message", "-p", "-t", "main", "#{pane_pid}"); got != pid {
				t.Errorf("setup replaced existing pane process: %s -> %s", pid, got)
			}
		})
	}
}

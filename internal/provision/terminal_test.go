package provision

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDetectClaudeNotificationChannel(t *testing.T) {
	for _, tc := range []struct {
		name        string
		env         map[string]string
		outer, want string
	}{
		{"iterm", map[string]string{"TERM_PROGRAM": "iTerm.app"}, "", "iterm2"},
		{"kitty", map[string]string{"TERM": "xterm-kitty"}, "", "kitty"},
		{"ghostty", map[string]string{"TERM": "xterm-ghostty"}, "", "ghostty"},
		{"tmux iterm marker", map[string]string{"TERM_PROGRAM": "tmux", "ITERM_SESSION_ID": "session"}, "", "iterm2"},
		{"tmux kitty marker", map[string]string{"TERM_PROGRAM": "tmux", "KITTY_WINDOW_ID": "1"}, "", "kitty"},
		{"tmux ghostty marker", map[string]string{"TERM_PROGRAM": "tmux", "GHOSTTY_RESOURCES_DIR": "/resources"}, "", "ghostty"},
		{"tmux environment", map[string]string{"TERM_PROGRAM": "tmux", "TMUX": "socket"}, "iTerm.app", "iterm2"},
		{"tmux unknown", map[string]string{"TERM_PROGRAM": "tmux", "TMUX": "socket"}, "unknown", ""},
		{"ide inherited marker", map[string]string{"TERM_PROGRAM": "vscode", "ITERM_SESSION_ID": "session"}, "", ""},
		{"ide inherited program", map[string]string{"TERM_PROGRAM": "iTerm.app", "VSCODE_GIT_ASKPASS_MAIN": "askpass"}, "", ""},
		{"unknown", map[string]string{"TERM": "xterm-256color"}, "", ""},
		{"headless", nil, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := detectClaudeNotificationChannel(func(key string) string { return tc.env[key] }, func() string { return tc.outer })
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildExtraVarsClaudeNotifications(t *testing.T) {
	for _, key := range []string{"CURSOR_TRACE_ID", "VSCODE_GIT_ASKPASS_MAIN", "TERMINAL_EMULATOR"} {
		t.Setenv(key, "")
	}
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	for _, phase := range []string{"base", "finalize", "full"} {
		for _, selected := range []bool{false, true} {
			cfg := fullConfig()
			cfg.WithClaude = selected
			data, err := BuildExtraVars(cfg, phase, "guest", false)
			if err != nil {
				t.Fatal(err)
			}
			vars := parseVars(t, data)
			want := selected && phase != "base"
			got, present := vars["claude_notification_channel"]
			if present != want || (want && got != "iterm2") {
				t.Fatalf("phase=%s selected=%v channel=%v present=%v", phase, selected, got, present)
			}
		}
	}
}

// Run against an isolated tmux server to verify the outer terminal survives
// the same pane environment that masks it from Claude.
func TestTmuxTerminalProgram(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable")
	}
	directory, err := os.MkdirTemp("", "sn-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "tmux.sock")
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	output, err := exec.Command(binary, "-S", socket, "-f", "/dev/null", "new-session", "-d", "-s", "notifications").CombinedOutput()
	if err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command(binary, "-S", socket, "kill-server").Run() })
	t.Setenv("TMUX", socket+",0,0")
	t.Setenv("TERM_PROGRAM", "tmux")
	if got := tmuxTerminalProgram(); got != "iTerm.app" {
		t.Fatalf("outer program = %q", got)
	}
	// Missing session-level entry must still resolve the global environment.
	if output, err := exec.Command(binary, "-S", socket, "set-environment", "-u", "TERM_PROGRAM").CombinedOutput(); err != nil {
		t.Fatalf("unset: %v: %s", err, output)
	}
	if got := tmuxTerminalProgram(); got != "iTerm.app" {
		t.Fatalf("global outer program = %q", got)
	}
}

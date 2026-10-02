package provision

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Detect on the workstation: the provisioning SSH session cannot identify the
// terminal that will receive Claude's notification escape sequences.
func claudeNotificationChannel() string {
	return detectClaudeNotificationChannel(os.Getenv, tmuxTerminalProgram)
}

func detectClaudeNotificationChannel(getenv func(string) string, outerProgram func() string) string {
	channel := func(program string) string {
		switch program {
		case "iTerm.app":
			return "iterm2"
		case "kitty":
			return "kitty"
		case "ghostty":
			return "ghostty"
		default:
			return ""
		}
	}
	// IDE terminals may inherit identifiers from the terminal that launched them.
	if getenv("CURSOR_TRACE_ID") != "" || getenv("VSCODE_GIT_ASKPASS_MAIN") != "" || getenv("TERMINAL_EMULATOR") == "JetBrains-JediTerm" {
		return ""
	}
	program := getenv("TERM_PROGRAM")
	if c := channel(program); c != "" {
		return c
	}
	if getenv("TERM") == "xterm-ghostty" {
		return "ghostty"
	}
	if strings.Contains(getenv("TERM"), "kitty") {
		return "kitty"
	}
	if program != "" && program != "tmux" && program != "screen" {
		return ""
	}
	if getenv("ITERM_SESSION_ID") != "" || getenv("__CFBundleIdentifier") == "com.googlecode.iterm2" {
		return "iterm2"
	}
	if getenv("KITTY_WINDOW_ID") != "" {
		return "kitty"
	}
	if getenv("GHOSTTY_RESOURCES_DIR") != "" {
		return "ghostty"
	}
	if getenv("TMUX") != "" {
		return channel(outerProgram())
	}
	return ""
}

func tmuxTerminalProgram() string {
	// tmux replaces TERM_PROGRAM in panes but usually retains the original in
	// its session environment. Older configurations may retain it only globally.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, args := range [][]string{{"show-environment", "TERM_PROGRAM"}, {"show-environment", "-g", "TERM_PROGRAM"}} {
		output, err := exec.CommandContext(ctx, "tmux", args...).Output()
		if err == nil {
			value, ok := strings.CutPrefix(strings.TrimSpace(string(output)), "TERM_PROGRAM=")
			if ok && value != "" && value != "tmux" && value != "screen" {
				return value
			}
		}
	}
	return ""
}

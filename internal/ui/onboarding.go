package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lullabot/sandbar/internal/releasecheck"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

const onboardingReportURL = "https://github.com/Lullabot/sandbar/issues/new"

type onboardingState struct {
	Acknowledged bool `json:"acknowledged"`
}

// onboardingStatePath keeps the acknowledgement independent of the VM and
// connection-profile stores. XDG state is durable across cache cleanup.
func onboardingStatePath() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, ".local", "state")
		} else {
			base = os.TempDir()
		}
	}
	return filepath.Join(base, "sandbar", "onboarding.json")
}

func loadOnboardingAcknowledged() (bool, error) {
	data, err := os.ReadFile(onboardingStatePath())
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read onboarding state: %w", err)
	}
	var state onboardingState
	if err := json.Unmarshal(data, &state); err != nil {
		return false, fmt.Errorf("read onboarding state: %w", err)
	}
	return state.Acknowledged, nil
}

func saveOnboardingAcknowledged() error {
	path := onboardingStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".onboarding-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.Write([]byte("{\"acknowledged\":true}\n")); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func isWarpTerminal() bool { return os.Getenv("TERM_PROGRAM") == "WarpTerminal" }

// osc8Link is used only with fixed or locally validated GitHub URLs.
func osc8Link(label, target string) string {
	return "\x1b]8;;" + target + "\x1b\\" + label + "\x1b]8;;\x1b\\"
}

type onboardingBrowserMsg struct {
	target string
	err    error
}

func (m model) openOnboardingBrowser(target string) tea.Cmd {
	opener := m.ghActions
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return onboardingBrowserMsg{target: target, err: opener.OpenInBrowser(ctx, target)}
	}
}

func (m model) updateOnboarding(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.OnboardingDone):
		if !m.onboardingFromHelp {
			if err := saveOnboardingAcknowledged(); err != nil {
				m.logWarn("could not save onboarding acknowledgement: " + err.Error())
			}
		}
		m.view = viewBoard
		if m.onboardingFromHelp {
			m.view = viewHelp
		}
		m.onboardingFromHelp = false
		return m, nil
	case key.Matches(msg, m.keys.Back):
		if m.onboardingFromHelp {
			m.onboardingFromHelp = false
			m.view = viewHelp
		}
		return m, nil
	case key.Matches(msg, m.keys.OnboardingRelease):
		return m, m.openOnboardingBrowser(releasecheck.ReleaseNotesURL(buildVersion))
	case key.Matches(msg, m.keys.OnboardingReport):
		return m, m.openOnboardingBrowser(onboardingReportURL)
	case msg.Code == tea.KeyDown:
		m.onboardingScroll++
	case msg.Code == tea.KeyUp:
		m.onboardingScroll--
	case msg.Code == tea.KeyPgDown:
		m.onboardingScroll += m.layout.ContentHeight - 2
	case msg.Code == tea.KeyPgUp:
		m.onboardingScroll -= m.layout.ContentHeight - 2
	}
	m.clampOnboardingScroll()
	return m, nil
}

func (m model) onboardingLines() []string {
	width := m.layout.ContentWidth
	var lines []string
	add := func(s string) {
		for _, line := range wrapText(s, width) {
			lines = append(lines, m.clipLine(line))
		}
	}
	lines = append(lines, m.clipLine(titleStyle.Render("Welcome to sandbar")), "")
	add("Sandbar creates disposable development VMs for your coding agents.")
	if isWarpTerminal() {
		lines = append(lines, "")
		add("Warp warning: Sandbar and Claude Code can have terminal compatibility issues in Warp. Try another terminal if keys or rendering misbehave.")
	}
	lines = append(lines, "")
	add("Your guest shell runs inside tmux. To leave a CLI tool running, detach with C-a d (press Ctrl+A, then d). Reattach from the VM shell later.")
	lines = append(lines, "")
	releaseURL := releasecheck.ReleaseNotesURL(buildVersion)
	lines = append(lines, m.clipLine("Release notes: "+osc8Link(releaseURL, releaseURL)))
	lines = append(lines, m.clipLine("Feature and bug requests: "+osc8Link(onboardingReportURL, onboardingReportURL)))
	lines = append(lines, "")
	add("Press n for the installed release notes, or r to report a feature or bug.")
	return lines
}

func (m *model) clampOnboardingScroll() {
	maxBody := m.layout.ContentHeight - 2
	if maxBody < 1 {
		maxBody = 1
	}
	maxScroll := len(m.onboardingLines()) - maxBody
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.onboardingScroll < 0 {
		m.onboardingScroll = 0
	}
	if m.onboardingScroll > maxScroll {
		m.onboardingScroll = maxScroll
	}
}

func (m model) onboardingView() string {
	lines := m.onboardingLines()
	maxBody := m.layout.ContentHeight - 2
	if maxBody < 1 {
		maxBody = 1
	}
	start := m.onboardingScroll
	if start > len(lines)-maxBody {
		start = len(lines) - maxBody
	}
	if start < 0 {
		start = 0
	}
	end := start + maxBody
	if end > len(lines) {
		end = len(lines)
	}
	visible := append([]string(nil), lines[start:end]...)
	if end < len(lines) && len(visible) > 0 {
		visible[len(visible)-1] = m.clipLine("↓ scroll for links and more")
	}
	footer := m.footerView([]key.Binding{m.keys.OnboardingDone, m.keys.OnboardingRelease, m.keys.OnboardingReport})
	return appStyle.Render(strings.Join(visible, "\n") + "\n" + footer)
}

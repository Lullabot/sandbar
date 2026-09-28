package ui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"
)

func freshOnboardingModel(t *testing.T) model {
	t.Helper()
	isolateHostState(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cli := lima.New(fakeRunner{})
	return New(singleFleet(provider.NewLocalLima(cli, &provision.Provisioner{Lima: cli}), registry.LocalScope)).(model)
}

func TestOnboardingFirstRunDismissalAndHelpReturn(t *testing.T) {
	m := freshOnboardingModel(t)
	if m.view != viewOnboarding {
		t.Fatalf("initial view = %v, want onboarding", m.view)
	}
	text := ansi.Strip(m.View().Content)
	for _, want := range []string{"tmux", "C-a d", "https://github.com/Lullabot/sandbar/issues/new", "v1.2.3"} {
		if !strings.Contains(text, want) && want != "v1.2.3" {
			t.Errorf("onboarding missing %q", want)
		}
	}
	m, _ = press(t, m, runeKey('q'))
	if m.view != viewOnboarding {
		t.Fatal("q left onboarding")
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.view != viewBoard {
		t.Fatalf("dismissed view = %v, want board", m.view)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_STATE_HOME"), "sandbar", "onboarding.json")); err != nil {
		t.Fatalf("acknowledgement not persisted: %v", err)
	}
	cli := lima.New(fakeRunner{})
	relaunched := New(singleFleet(provider.NewLocalLima(cli, &provision.Provisioner{Lima: cli}), registry.LocalScope)).(model)
	if relaunched.view != viewBoard {
		t.Fatalf("relaunch view = %v, want board after acknowledgement", relaunched.view)
	}
	statePath := filepath.Join(os.Getenv("XDG_STATE_HOME"), "sandbar", "onboarding.json")
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	m.openHelp()
	m, _ = press(t, m, runeKey('o'))
	if m.view != viewOnboarding {
		t.Fatalf("help reopen view = %v", m.view)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.view != viewHelp {
		t.Fatalf("esc return view = %v, want help", m.view)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("help revisit rewrote acknowledgement: %v", err)
	}
}

func TestOnboardingWarpDetection(t *testing.T) {
	for _, tc := range []struct {
		program string
		want    bool
	}{
		{"WarpTerminal", true}, {"warp", false}, {"iTerm.app", false}, {"", false},
	} {
		t.Run(tc.program, func(t *testing.T) {
			t.Setenv("TERM_PROGRAM", tc.program)
			m := freshOnboardingModel(t)
			got := strings.Contains(ansi.Strip(m.View().Content), "Warp")
			if got != tc.want {
				t.Fatalf("Warp warning = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOnboardingConstrainedScrollReachesLinks(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "WarpTerminal")
	m := freshOnboardingModel(t)
	m = resized(m, 60, 12)
	if !strings.Contains(ansi.Strip(m.View().Content), "Warp warning") {
		t.Fatal("Warp warning hidden on initial constrained screen")
	}
	for i := 0; i < 30; i++ {
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Release notes:") || !strings.Contains(view, "issues/new") {
		t.Fatalf("links unreachable after scrolling: %q", view)
	}
	if strings.Count(m.View().Content, "\x1b]8;;") < 4 {
		t.Fatal("support and release notes must both be OSC 8 links")
	}
}

func TestOnboardingBrowserRoutesAndFailure(t *testing.T) {
	m := freshOnboardingModel(t)
	pinVersion(t, "v1.2.3")
	fake := &fakeGhActions{}
	m.ghActions = fake
	for _, tc := range []struct {
		key  rune
		want string
	}{
		{'h', onboardingSupportURL},
		{'r', "https://github.com/Lullabot/sandbar/releases/tag/v1.2.3"},
	} {
		var cmd tea.Cmd
		m, cmd = press(t, m, runeKey(tc.key))
		if cmd == nil {
			t.Fatalf("%c did not dispatch browser command", tc.key)
		}
		msg := cmd().(onboardingBrowserMsg)
		m, _ = press(t, m, msg)
		if got := fake.opened[len(fake.opened)-1]; got != tc.want {
			t.Errorf("%c opened %q, want %q", tc.key, got, tc.want)
		}
	}
	fake.openErr = errors.New("no desktop browser")
	m, cmd := press(t, m, runeKey('h'))
	m, _ = press(t, m, cmd())
	if !strings.Contains(m.lastMessage(), "no desktop browser") || !strings.Contains(m.lastMessage(), onboardingSupportURL) {
		t.Fatalf("browser failure did not leave actionable warning: %q", m.lastMessage())
	}
}

func TestOnboardingAcknowledgementWriteFailureStillEntersBoard(t *testing.T) {
	m := freshOnboardingModel(t)
	path := filepath.Join(os.Getenv("XDG_STATE_HOME"), "sandbar", "onboarding.json")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.view != viewBoard || !strings.Contains(m.lastMessage(), "could not save onboarding") {
		t.Fatalf("write failure: view=%v message=%q", m.view, m.lastMessage())
	}
}

func TestOnboardingStateFallbackAndNavigationEdges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", home)
	if got, want := onboardingStatePath(), filepath.Join(home, ".local", "state", "sandbar", "onboarding.json"); got != want {
		t.Fatalf("fallback state path = %q, want %q", got, want)
	}
	if err := os.MkdirAll(onboardingStatePath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if acknowledged, err := loadOnboardingAcknowledged(); err == nil || acknowledged {
		t.Fatalf("directory-shaped state = (%v, %v), want a read error", acknowledged, err)
	}

	m := freshOnboardingModel(t)
	m = resized(m, 60, 12)
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.view != viewOnboarding {
		t.Fatal("esc must not bypass first-run acknowledgement")
	}
	for _, keyMsg := range []tea.KeyPressMsg{
		{Code: tea.KeyDown}, {Code: tea.KeyUp},
		{Code: tea.KeyPgDown}, {Code: tea.KeyPgUp},
	} {
		m, _ = press(t, m, keyMsg)
	}
	if m.onboardingScroll < 0 {
		t.Fatalf("navigation left a negative scroll offset: %d", m.onboardingScroll)
	}
	m.onboardingFromHelp = true
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.view != viewHelp || m.onboardingFromHelp {
		t.Fatalf("continue from help = view %v, origin %v", m.view, m.onboardingFromHelp)
	}
}

func TestTUIOnboardingOrdinary80x24(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "")
	m := freshOnboardingModel(t)
	pinVersion(t, "v1.2.3")
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	waitForText(t, tm, "Welcome to sand")
	teatest.RequireEqualOutput(t, finalScreen(t, tm))
}

func TestTUIOnboardingWarpConstrained(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "WarpTerminal")
	m := freshOnboardingModel(t)
	pinVersion(t, "v1.2.3")
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(60, 12))
	waitForText(t, tm, "Welcome to sand")
	teatest.RequireEqualOutput(t, finalScreen(t, tm))
}

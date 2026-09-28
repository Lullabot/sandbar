package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/releasecheck"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
)

type fakeReleaseChecker struct {
	cached  releasecheck.Release
	checked releasecheck.Release
	checks  int
}

func (f *fakeReleaseChecker) Cached() releasecheck.Release { return f.cached }
func (f *fakeReleaseChecker) Check(context.Context) releasecheck.Release {
	f.checks++
	return f.checked
}

func releaseFor(tag string) releasecheck.Release {
	return releasecheck.Release{Tag: tag, NotesURL: releasecheck.ReleaseNotesURL(tag)}
}

func TestReleaseCacheFirstAndAsyncResultAcrossViews(t *testing.T) {
	isolateHostState(t)
	pinVersion(t, "v1.2.3")
	checker := &fakeReleaseChecker{cached: releaseFor("v1.3.0"), checked: releaseFor("v1.4.0")}
	previous := releaseCheckerFactory
	releaseCheckerFactory = func() releaseChecker { return checker }
	t.Cleanup(func() { releaseCheckerFactory = previous })

	m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)
	if checker.checks != 0 || !strings.Contains(ansi.Strip(m.titleRow()), "(Update available!)") {
		t.Fatalf("cache was not rendered before async work: checks=%d row=%q", checker.checks, m.titleRow())
	}
	startup := m.Init()()
	batch, ok := startup.(tea.BatchMsg)
	if !ok || len(batch) < 2 {
		t.Fatalf("startup should batch provider and release commands, got %T with %d commands", startup, len(batch))
	}
	var releaseMsg releaseCheckedMsg
	for _, cmd := range batch {
		if got, ok := cmd().(releaseCheckedMsg); ok {
			releaseMsg = got
		}
	}
	if checker.checks != 1 || releaseMsg.release.Tag != "v1.4.0" {
		t.Fatalf("release check not delivered asynchronously: checks=%d msg=%+v", checker.checks, releaseMsg)
	}
	m.view = viewHelp
	next, _ := m.Update(releaseMsg)
	m = next.(model)
	if m.release.Tag != "v1.4.0" || m.view != viewHelp || len(m.messages) != 0 {
		t.Fatalf("result should fold silently in any view: release=%+v view=%v messages=%+v", m.release, m.view, m.messages)
	}
	next, _ = m.Update(releaseCheckedMsg{release: releasecheck.Release{}})
	if got := next.(model).release.Tag; got != "v1.4.0" {
		t.Fatalf("failed lookup erased cached release: %q", got)
	}
}

func TestReleaseCheckCmdNilIsDisabled(t *testing.T) {
	if cmd := releaseCheckCmd(nil); cmd != nil {
		t.Fatal("nil checker should not schedule a startup command")
	}
}

func TestReleaseNoticeVersionGateAndTrustedTarget(t *testing.T) {
	isolateHostState(t)
	m := newTestModel(t)
	for _, tc := range []struct {
		installed, latest string
		wantNotice        bool
	}{
		{"v1.2.3", "v1.3.0", true},
		{"v1.2.3", "v1.2.3", false},
		{"v1.2.3", "v1.2.2", false},
		{"v1.2.3", "v1.3.0-rc1", false},
		{"dev", "v1.3.0", false},
		{"v1.2.3-dirty", "v1.3.0", false},
	} {
		t.Run(tc.installed+"/"+tc.latest, func(t *testing.T) {
			pinVersion(t, tc.installed)
			m.release = releaseFor(tc.latest)
			row := m.titleRow()
			if got := strings.Contains(ansi.Strip(row), "(Update available!)"); got != tc.wantNotice {
				t.Fatalf("notice=%v, want %v in %q", got, tc.wantNotice, row)
			}
			if tc.wantNotice {
				wantURL := "https://github.com/Lullabot/sandbar/releases/tag/v1.3.0"
				if !strings.Contains(row, ansi.SetHyperlink(wantURL)) || !strings.Contains(row, ansi.ResetHyperlink()) {
					t.Fatalf("notice must link only the validated notes URL: %q", row)
				}
			}
		})
	}
	pinVersion(t, "v1.2.3")
	m.release = releasecheck.Release{Tag: "v1.3.0", NotesURL: "https://evil.example/release"}
	if strings.Contains(m.titleRow(), ansi.SetHyperlink(m.release.NotesURL)) || strings.Contains(ansi.Strip(m.titleRow()), "(Update available!)") {
		t.Fatalf("unsafe notes target reached header: %q", m.titleRow())
	}
}

func TestReleaseNoticeWidthPriority(t *testing.T) {
	isolateHostState(t)
	pinVersion(t, "v1.2.3")
	m := newTestModel(t)
	m.release = releaseFor("v1.3.0")
	for _, width := range []int{80, 37, 30} {
		m = resized(m, width, 30)
		row := m.titleRow()
		if got := ansi.StringWidth(row); got > m.layout.ContentWidth {
			t.Fatalf("width %d: title width %d exceeds budget %d", width, got, m.layout.ContentWidth)
		}
		plain := ansi.Strip(row)
		if !strings.Contains(plain, "v1.2.3") {
			t.Fatalf("width %d: installed version shed before suffix: %q", width, plain)
		}
		if width == 80 && !strings.Contains(plain, "(Update available!)") {
			t.Fatalf("80-column header lacks update suffix: %q", plain)
		}
		if width == 30 && strings.Contains(plain, "(Update available!)") {
			t.Fatalf("narrow header kept suffix: %q", plain)
		}
	}
	m = resized(m, 80, 12)
	if m.layout.HeaderFull {
		t.Fatal("test needs compact header")
	}
	if compact := ansi.Strip(m.headerView()); strings.Contains(compact, "Update available!") || strings.Contains(compact, "v1.2.3") {
		t.Fatalf("compact header showed release clause: %q", compact)
	}
}

func TestReleaseHelpOpensAvailableOrInstalledNotes(t *testing.T) {
	isolateHostState(t)
	pinVersion(t, "v1.2.3")
	m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)
	m.view = viewHelp
	fake := &fakeGhActions{}
	m.ghActions = fake
	m.release = releaseFor("v1.3.0")

	next, cmd := m.updateHelp(runeKey('r'))
	if cmd == nil {
		t.Fatal("help release key did not return browser command")
	}
	_ = cmd()
	if got, want := fake.opened[0], m.release.NotesURL; got != want {
		t.Fatalf("browser opened %q, want available notes %q", got, want)
	}

	m = next.(model)
	m.release = releaseFor("v1.2.3")
	_, cmd = m.updateHelp(runeKey('r'))
	_ = cmd()
	if got, want := fake.opened[1], releasecheck.ReleaseNotesURL("v1.2.3"); got != want {
		t.Fatalf("browser opened %q, want installed notes %q", got, want)
	}
}

func TestTUIReleaseNotice80x24(t *testing.T) {
	pinHostForHeader(t)
	pinVersion(t, "v1.2.3")
	m := newTestModel(t)
	m.release = releaseFor("v1.3.0")
	m = resized(m, 80, 24)
	golden.RequireEqual(t, renderModel(m))
}

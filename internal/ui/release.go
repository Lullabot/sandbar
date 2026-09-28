package ui

import (
	"context"

	"github.com/lullabot/sandbar/internal/releasecheck"

	tea "charm.land/bubbletea/v2"
)

// releaseChecker keeps the optional network lookup behind the same command
// boundary as provider refreshes. Tests replace the factory without HTTP or
// host-cache access.
type releaseChecker interface {
	Cached() releasecheck.Release
	Check(context.Context) releasecheck.Release
}

var releaseCheckerFactory = func() releaseChecker { return releasecheck.New(releasecheck.Config{}) }

type releaseCheckedMsg struct{ release releasecheck.Release }

func releaseCheckCmd(checker releaseChecker) tea.Cmd {
	if checker == nil {
		return nil
	}
	return func() tea.Msg { return releaseCheckedMsg{release: checker.Check(context.Background())} }
}

// availableRelease validates the model value before either rendering an OSC 8
// target or offering the browser route. This also protects against a malformed
// injected/cache value independent of the checker's own validation.
func (m model) availableRelease() (releasecheck.Release, bool) {
	r := m.release
	if !releasecheck.UpdateAvailable(buildVersion, r.Tag) || r.NotesURL != releasecheck.ReleaseNotesURL(r.Tag) {
		return releasecheck.Release{}, false
	}
	return r, true
}

// releaseNotesURL is shared with help/onboarding keyboard routes. A released
// build defaults to its own notes; a source build goes to the releases index.
func (m model) releaseNotesURL() string {
	if release, ok := m.availableRelease(); ok {
		return release.NotesURL
	}
	return releasecheck.ReleaseNotesURL(buildVersion)
}

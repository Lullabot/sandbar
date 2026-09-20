package ui

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/checkouts"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"

	tea "charm.land/bubbletea/v2"
)

// seedCheckouts writes a sweep result into the host-side registry the reset form
// reads — the same registry the TUI's own sweep loop fills — so a test can open
// the form against a VM whose checkouts are "known" without any guest at all.
func seedCheckouts(t *testing.T, m model, name string, cs ...checkouts.Checkout) {
	t.Helper()
	if err := m.checkouts.Set(registry.LocalScope, name, checkouts.VMCheckouts{
		Checkouts: cs,
		SweptAt:   time.Now(),
	}); err != nil {
		t.Fatalf("seed checkouts: %v", err)
	}
}

func repoAt(path, branch string) checkouts.Checkout {
	return checkouts.Checkout{Path: path, Kind: checkouts.KindRepo, Branch: branch}
}

// TestResetFormOffersSweptCheckouts: every git checkout the last sweep found in
// the VM gets a row of its own, INCLUDING the ones sand never cloned and the
// linked worktrees an agent made — the case the old form had no way to express,
// where the only preservable thing was the repo named in the VM's recorded clone
// URL.
func TestResetFormOffersSweptCheckouts(t *testing.T) {
	cfg := resetConfig() // user "ada", clone URL https://github.com/org/repo
	m := newTestModel(t)
	seedCheckouts(t, m, cfg.Name,
		repoAt("/home/ada/src/app", "feature"),
		checkouts.Checkout{
			Path:   "/home/ada/src/app/.claude/worktrees/spike",
			Kind:   checkouts.KindWorktree,
			Parent: "/home/ada/src/app",
			Branch: "spike",
			Dirty:  3,
		},
		// The VM's own project checkout: already covered by the project toggle,
		// so it must NOT appear a second time.
		repoAt("/home/ada/github.com/org/repo", "main"),
	)
	m.openResetForm(registry.LocalScope, cfg.Name, cfg)

	var labels []string
	for _, c := range m.resetCheckouts {
		labels = append(labels, c.label)
	}
	want := []string{"Preserve ~/src/app", "Preserve ~/src/app/.claude/worktrees/spike"}
	if !reflect.DeepEqual(labels, want) {
		t.Fatalf("checkout rows = %v, want %v", labels, want)
	}

	view := m.formView()
	for _, w := range want {
		if !strings.Contains(view, w) {
			t.Errorf("the reset form does not offer %q; got:\n%s", w, view)
		}
	}

	// The worktree's help says what state it was last seen in and how old that
	// observation is — the same honesty the delete guard gives the same data.
	help := m.toggles()[len(m.toggles())-1].help
	for _, w := range []string{"linked worktree", "3 uncommitted", "Last seen"} {
		if !strings.Contains(help, w) {
			t.Errorf("worktree row help %q is missing %q", help, w)
		}
	}
}

// TestResetFormOffersNothingWithoutASweep: a VM that has never been swept (one
// that has been stopped since before this feature, say) simply has no checkout
// rows. The form still works, and nothing is invented.
func TestResetFormOffersNothingWithoutASweep(t *testing.T) {
	cfg := resetConfig()
	m := newTestModel(t)
	m.openResetForm(registry.LocalScope, cfg.Name, cfg)
	if len(m.resetCheckouts) != 0 {
		t.Fatalf("un-swept VM offered %d checkout rows", len(m.resetCheckouts))
	}
}

// TestResetFormCapsCheckoutRows: the form is a single non-scrolling column, so
// the row list is capped — and what the cap left out is SAID, not silently
// dropped, with the whole-home option named as the way to keep it all.
func TestResetFormCapsCheckoutRows(t *testing.T) {
	cfg := resetConfig()
	m := newTestModel(t)
	var cs []checkouts.Checkout
	for i := 0; i < resetPreserveMaxRows+3; i++ {
		cs = append(cs, repoAt("/home/ada/src/app"+string(rune('a'+i)), "main"))
	}
	seedCheckouts(t, m, cfg.Name, cs...)
	m.openResetForm(registry.LocalScope, cfg.Name, cfg)

	if len(m.resetCheckouts) != resetPreserveMaxRows {
		t.Fatalf("listed %d rows, want the cap of %d", len(m.resetCheckouts), resetPreserveMaxRows)
	}
	if m.resetCheckoutsHidden != 3 {
		t.Fatalf("hidden count = %d, want 3", m.resetCheckoutsHidden)
	}
	view := m.formView()
	if !strings.Contains(view, "3 more checkout(s)") {
		t.Errorf("the form never said 3 checkouts were left out; got:\n%s", view)
	}
	if !strings.Contains(view, "entire home directory") {
		t.Errorf("the overflow note should point at the whole-home option; got:\n%s", view)
	}
}

// capturedReset drives a submitted reset through a fake provider and returns the
// options it was handed. The run closure executes on the job's own goroutine, so
// the options come back over a channel rather than from a shared field.
func capturedReset(t *testing.T, m model) provision.ResetOptions {
	t.Helper()
	got := make(chan provision.ResetOptions, 1)
	m.members[0].prov = &providerfake.Provider{
		ResetFunc: func(_ context.Context, _ vm.CreateConfig, opts provision.ResetOptions, _ io.Writer) error {
			got <- opts
			return nil
		},
	}
	next, cmd := m.Update(ctrlKey('s'))
	m = next.(model)
	if m.formErr != nil {
		t.Fatalf("the form refused the reset: %v", m.formErr)
	}
	if cmd != nil {
		go cmd() // the job's goroutine; its message is not what this test reads
	}
	select {
	case opts := <-got:
		return opts
	case <-time.After(5 * time.Second):
		t.Fatal("the reset never reached the provider")
		return provision.ResetOptions{}
	}
}

// TestSelectedCheckoutsReachTheProvider is the assertion that matters: a ticked
// row must arrive at provision.Reset as the ABSOLUTE GUEST PATH the sweep
// recorded. The label is shortened against a guess at the guest home, and a
// reset that acted on the guess would preserve the wrong directory — or nothing.
func TestSelectedCheckoutsReachTheProvider(t *testing.T) {
	cfg := resetConfig()
	m := openReset(t, cfg)
	seedCheckouts(t, m, cfg.Name, repoAt("/home/ada/src/app", "feature"))
	m.openResetForm(registry.LocalScope, cfg.Name, cfg)

	m = tabToToggle(t, m, "Preserve ~/src/app")
	sp, _ := m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m = sp.(model)

	opts := capturedReset(t, m)
	if !reflect.DeepEqual(opts.PreservePaths, []string{"/home/ada/src/app"}) {
		t.Fatalf("PreservePaths = %v, want the absolute guest path", opts.PreservePaths)
	}
	if opts.PreserveHome {
		t.Error("PreserveHome was set by a checkout selection")
	}
}

// TestWholeHomeIsSentAlone: the whole-home archive already contains everything
// the other options would copy, so the request that goes out says exactly that
// and nothing else. Sending the subsumed flags too would read, in a log, as a
// reset that intended four copies of overlapping data.
func TestWholeHomeIsSentAlone(t *testing.T) {
	cfg := resetConfig()
	m := openReset(t, cfg)
	seedCheckouts(t, m, cfg.Name, repoAt("/home/ada/src/app", "feature"))
	m.openResetForm(registry.LocalScope, cfg.Name, cfg)

	// Tick a checkout and the Claude toggle, then the whole-home toggle.
	for _, label := range []string{"Preserve ~/src/app", "Preserve Claude Code settings", "Preserve the entire home directory"} {
		m = tabToToggle(t, m, label)
		sp, _ := m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
		m = sp.(model)
	}

	opts := capturedReset(t, m)
	if !opts.PreserveHome {
		t.Fatal("PreserveHome was not requested")
	}
	if opts.PreserveClaude || opts.PreserveProject || len(opts.PreservePaths) != 0 {
		t.Errorf("a whole-home reset also sent the options it subsumes: %+v", opts)
	}
}

// TestShortenGuestPath: a path outside the guessed home is shown in full rather
// than disguised as something under ~, because that is exactly the row a reset
// will refuse and the user needs to recognise it.
func TestShortenGuestPath(t *testing.T) {
	cases := []struct{ path, home, want string }{
		{"/home/ada/src/app", "/home/ada", "~/src/app"},
		{"/opt/work/app", "/home/ada", "/opt/work/app"},
		{"/home/ada", "/home/ada", "/home/ada"},
		{"/home/ada/src/app", "", "/home/ada/src/app"},
	}
	for _, tc := range cases {
		if got := shortenGuestPath(tc.path, tc.home); got != tc.want {
			t.Errorf("shortenGuestPath(%q, %q) = %q, want %q", tc.path, tc.home, got, tc.want)
		}
	}
}

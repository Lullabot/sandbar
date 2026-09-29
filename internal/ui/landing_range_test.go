package ui

// landing_range_test.go covers the Landing pane's review-range prompt: R opens
// a one-line input, enter starts the ordinary review with the typed text split
// into diff arguments, a blank entry is the same review v starts, and esc
// starts nothing. Every test drives the model with real key events through
// Update, and fakes m.reviewRun so the assertion reaches the Session the pane
// hands to internal/landreview rather than stopping at the model.

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/landreview"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
)

// pressKey sends one keypress through the top level Update.
func pressKey(m model, k tea.KeyPressMsg) (model, tea.Cmd) {
	next, cmd := m.Update(k)
	return next.(model), cmd
}

// recordSessions replaces m.reviewRun with one that records every Session it
// is asked to run and returns at once.
func recordSessions(m *model) *[]*landreview.Session {
	var got []*landreview.Session
	m.reviewRun = func(ctx context.Context, sess *landreview.Session, w io.Writer) (string, error) {
		got = append(got, sess)
		return "", nil
	}
	return &got
}

// TestLandingReviewRangeSubmitsSplitArgs is the feature end to end: the typed
// text reaches the Session as separate argv elements, split on whitespace with
// no shell and no quote handling, and keys that mean something elsewhere —
// q among them — are text while the input has focus.
func TestLandingReviewRangeSubmitsSplitArgs(t *testing.T) {
	m, _ := landingWithOneCheckout(t)
	sessions := recordSessions(&m)

	m, _ = pressKey(m, runeKey('R'))
	if m.landing.rangeInput == nil {
		t.Fatal("R did not open the range prompt")
	}
	// "--quiet" carries a q, v and r: the quit key on the board, and this
	// pane's review and rescan keys. All of them must land in the field.
	m = typeInto(m, `HEAD~2...HEAD  --quiet "a b"`)
	if m.view != viewLanding {
		t.Fatalf("typing into the prompt left the pane (view %v)", m.view)
	}
	if m.review.path != "" || len(*sessions) != 0 || m.landing.scanning {
		t.Fatal("a key typed into the prompt ran its pane verb instead of being text")
	}

	m, cmd := pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.landing.rangeInput != nil {
		t.Error("the prompt stayed open after submitting")
	}
	if cmd == nil {
		t.Fatal("submitting the prompt started no review")
	}
	drainCmd(cmd)

	if len(*sessions) != 1 {
		t.Fatalf("started %d review sessions, want 1", len(*sessions))
	}
	sess := (*sessions)[0]
	want := []string{"HEAD~2...HEAD", "--quiet", `"a`, `b"`}
	if !reflect.DeepEqual(sess.DiffArgs, want) {
		t.Errorf("Session.DiffArgs = %q, want %q", sess.DiffArgs, want)
	}
	if sess.Clean {
		t.Error("a range review discarded the saved review; only V does that")
	}
	if sess.Checkout.Path != "/home/user/repo" {
		t.Errorf("Session.Checkout.Path = %q, want the row under the cursor", sess.Checkout.Path)
	}
}

// TestLandingReviewRangeBlankIsTheSameAsV pins that an empty (or all-space)
// entry is not an error and not a different review: it is exactly the review
// v starts, with no diff arguments at all.
func TestLandingReviewRangeBlankIsTheSameAsV(t *testing.T) {
	for _, typed := range []string{"", "   "} {
		m, _ := landingWithOneCheckout(t)
		sessions := recordSessions(&m)

		m, _ = pressKey(m, runeKey('R'))
		m = typeInto(m, typed)
		m, cmd := pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		drainCmd(cmd)

		if len(*sessions) != 1 {
			t.Fatalf("typed %q: started %d sessions, want 1", typed, len(*sessions))
		}
		if got := (*sessions)[0]; len(got.DiffArgs) != 0 || got.Clean {
			t.Errorf("typed %q: Session = {DiffArgs: %q, Clean: %v}, want the plain v review", typed, got.DiffArgs, got.Clean)
		}
	}
}

// TestLandingReviewRangeEscStartsNothing pins cancelling: esc closes the
// prompt, keeps the pane open, and nothing reaches the review runner.
func TestLandingReviewRangeEscStartsNothing(t *testing.T) {
	m, _ := landingWithOneCheckout(t)
	sessions := recordSessions(&m)

	m, _ = pressKey(m, runeKey('R'))
	m = typeInto(m, "HEAD~2")
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	drainCmd(cmd)

	if m.landing.rangeInput != nil {
		t.Error("esc left the prompt open")
	}
	if m.view != viewLanding {
		t.Errorf("esc on the prompt left the pane (view %v); it should only close the prompt", m.view)
	}
	if m.review.path != "" || len(*sessions) != 0 {
		t.Error("esc on the prompt started a review")
	}

	// Reopening starts from an empty field, not the abandoned text.
	m, _ = pressKey(m, runeKey('R'))
	if v := m.landing.rangeInput.Value(); v != "" {
		t.Errorf("reopened prompt holds %q, want an empty field", v)
	}
}

// TestLandingReviewRangeRefusedWhileAReviewRuns covers the one-review rule:
// with a review already in flight, R neither opens a prompt nor starts a
// second session, which would leak the first one's cancel func.
func TestLandingReviewRangeRefusedWhileAReviewRuns(t *testing.T) {
	m, v := landingWithOneCheckout(t)
	sessions := recordSessions(&m)
	m.review = activeReview{scope: v.scope, vm: v.Name, path: "/home/user/repo"}

	m, cmd := pressKey(m, runeKey('R'))
	m, cmd2 := pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	drainCmd(cmd)
	drainCmd(cmd2)

	if m.landing.rangeInput != nil {
		t.Error("R opened the prompt while a review was in flight")
	}
	if len(*sessions) != 0 {
		t.Errorf("started %d sessions while one was in flight", len(*sessions))
	}
	if m.review.path != "/home/user/repo" {
		t.Error("the in-flight review's state was disturbed")
	}
}

// TestLandingFooterOffersReviewRange pins where the key is advertised: beside
// v on an idle row, gone from a row already under review (as V is), and, while
// the prompt is open, replaced by the only keys the prompt answers to.
func TestLandingFooterOffersReviewRange(t *testing.T) {
	m, _ := landingWithOneCheckout(t)

	keys := func(bs []string) string { return strings.Join(bs, " ") }
	enabled := func(m model) []string {
		var out []string
		for _, b := range m.landingHelp() {
			if b.Enabled() {
				out = append(out, b.Help().Key)
			}
		}
		return out
	}

	if got := keys(enabled(m)); !strings.Contains(got, "v R V") {
		t.Errorf("footer keys = %q, want R offered right after v", got)
	}

	under := m
	under.review = activeReview{scope: m.landing.scope, vm: m.landing.vmName, path: "/home/user/repo"}
	for _, k := range enabled(under) {
		if k == "R" {
			t.Error("the footer offers R on a row that is already being reviewed")
		}
	}

	open, _ := pressKey(m, runeKey('R'))
	if got := keys(enabled(open)); got != "enter esc" {
		t.Errorf("footer keys with the prompt open = %q, want only the prompt's own keys", got)
	}
}

// TestTUILandingReviewRangePromptGolden pins the prompt as drawn at 80x24:
// its help, its input, and the footer beneath it, all on screen.
func TestTUILandingReviewRangePromptGolden(t *testing.T) {
	m, _ := landingWithOneCheckout(t)
	m = resized(m, 80, 24)
	m, _ = pressKey(m, runeKey('R'))

	out := renderModel(m)
	if !strings.Contains(ansi.Strip(string(out)), "esc cancel") {
		t.Fatalf("the prompt's footer is not on screen at 80x24:\n%s", out)
	}
	golden.RequireEqual(t, out)
}

// TestTUILandingFooterGolden pins the idle pane's footer at 80x24 with the
// range key in it.
func TestTUILandingFooterGolden(t *testing.T) {
	m, _ := landingWithOneCheckout(t)
	m = resized(m, 80, 24)
	golden.RequireEqual(t, renderModel(m))
}

package ui

// landing_review_test.go covers the Landing pane's review key (task 6): the
// key's dispatch, the pane's in-progress row state, the completion message's
// fold-back, the empty-selection no-op, non-blocking Update, and the
// cancellation-on-leave/-quit guarantee (landing.go's runLandingReview,
// handleLandReviewDone, quitCmd).
//
// Every test here fakes m.reviewRun rather than letting a real
// landreview.Session.Run execute — that would pick a real workstation port,
// probe real HTTP, and spawn a real ssh/limactl forwarder child, none of
// which belong in a unit test (internal/landreview's own suite already
// covers Session.Run itself). See reviewRunFunc's doc.

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/checkouts"
	"github.com/lullabot/sandbar/internal/landreview"
	"github.com/lullabot/sandbar/internal/vm"

	tea "charm.land/bubbletea/v2"
)

// seedOneCheckout gives landingTestVM's pane exactly one row to select.
func seedOneCheckout(t *testing.T, m model, v boardVM, path string) {
	t.Helper()
	if err := m.checkouts.Set(v.scope, v.Name, checkouts.VMCheckouts{
		Checkouts: []checkouts.Checkout{
			{Path: path, Kind: checkouts.KindRepo, Branch: "feature", PushState: checkouts.PushStateNever},
		},
	}); err != nil {
		t.Fatalf("seed checkouts: %v", err)
	}
}

// TestUpdateLandingReviewKeyDispatchesAndMarksRowInProgress presses the
// review key on the selected row and checks: a command comes back (so the
// board can drive it asynchronously), the dispatched Session targets the
// right checkout and VM, and the row renders as under review while it runs.
func TestUpdateLandingReviewKeyDispatchesAndMarksRowInProgress(t *testing.T) {
	m, v := landingTestVM(t, "web")
	seedOneCheckout(t, m, v, "/home/user/repo")
	m.ghActions = &fakeGhActions{}

	started := make(chan *landreview.Session, 1)
	release := make(chan struct{})
	m.reviewRun = func(ctx context.Context, sess *landreview.Session, w io.Writer) (string, error) {
		started <- sess
		<-release // held open until the test lets it go, simulating "review open"
		return "/home/user/repo/review.xml", nil
	}
	m.openLandingPane(v)

	next, cmd := m.updateLanding(tea.KeyPressMsg{Code: 'v', Text: "v"})
	m2, ok := next.(model)
	if !ok {
		t.Fatal("updateLanding did not return a model")
	}
	if cmd == nil {
		t.Fatal("the review key produced no command — it must dispatch asynchronously")
	}
	if m2.landing.reviewPath != "/home/user/repo" {
		t.Fatalf("reviewPath = %q, want the selected checkout's path", m2.landing.reviewPath)
	}
	if m2.landing.reviewCancel == nil {
		t.Fatal("no cancel func retained — leaving the pane or quitting could not tear the review down")
	}
	if !strings.Contains(m2.landingView(), "reviewing") {
		t.Fatalf("rendered view does not show the row as under review:\n%s", m2.landingView())
	}

	// Run the returned command (as the Bubble Tea runtime would) and confirm
	// it actually called through to m.reviewRun with the right Session.
	msgCh := make(chan tea.Msg, 1)
	go func() { msgCh <- cmd() }()

	var sess *landreview.Session
	select {
	case sess = <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the command never called m.reviewRun")
	}
	if sess.Checkout.Path != "/home/user/repo" {
		t.Fatalf("Session.Checkout.Path = %q, want %q", sess.Checkout.Path, "/home/user/repo")
	}
	if sess.VM.Name != v.Name {
		t.Fatalf("Session.VM.Name = %q, want %q", sess.VM.Name, v.Name)
	}
	if sess.Open == nil {
		t.Fatal("Session.Open is nil — the review command must wire the browser opener")
	}

	close(release)
	select {
	case msg := <-msgCh:
		done, ok := msg.(landReviewDoneMsg)
		if !ok {
			t.Fatalf("command returned %T, want landReviewDoneMsg", msg)
		}
		if done.path != "/home/user/repo" || done.written != "/home/user/repo/review.xml" {
			t.Fatalf("landReviewDoneMsg = %+v, want it to name the checkout and the written path", done)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the command did not return after m.reviewRun finished")
	}
}

// TestUpdateLandingReviewKeyNoOpOnEmptySweep is the empty-selection guard: no
// rows means no checkout under the cursor, so the key must be a no-op rather
// than panic or dispatch a Session with a zero-value Checkout.
func TestUpdateLandingReviewKeyNoOpOnEmptySweep(t *testing.T) {
	m, v := landingTestVM(t, "web")
	m.ghActions = &fakeGhActions{}
	m.openLandingPane(v) // no checkouts seeded: rows is empty

	next, cmd := m.updateLanding(tea.KeyPressMsg{Code: 'v', Text: "v"})
	m2, ok := next.(model)
	if !ok {
		t.Fatal("updateLanding did not return a model")
	}
	if cmd != nil {
		t.Fatal("the review key dispatched a command with no row under the cursor")
	}
	if m2.landing.reviewPath != "" {
		t.Fatalf("reviewPath = %q, want \"\" (nothing was dispatched)", m2.landing.reviewPath)
	}
}

// TestUpdateLandingReviewKeySecondPressWhileInFlightIsNoOp guards against a
// second Session (and a leaked cancel func) starting while one is already
// running from this pane — Session targets exactly one checkout.
func TestUpdateLandingReviewKeySecondPressWhileInFlightIsNoOp(t *testing.T) {
	m, v := landingTestVM(t, "web")
	seedOneCheckout(t, m, v, "/home/user/repo")
	m.ghActions = &fakeGhActions{}
	block := make(chan struct{})
	m.reviewRun = func(ctx context.Context, sess *landreview.Session, w io.Writer) (string, error) {
		<-block
		return "", nil
	}
	m.openLandingPane(v)

	next, cmd := m.updateLanding(tea.KeyPressMsg{Code: 'v', Text: "v"})
	m2 := next.(model)
	if cmd == nil {
		t.Fatal("first press must dispatch")
	}

	next2, cmd2 := m2.updateLanding(tea.KeyPressMsg{Code: 'v', Text: "v"})
	m3 := next2.(model)
	if cmd2 != nil {
		t.Fatal("second press while a review is in flight must be a no-op")
	}
	if m3.landing.reviewPath != "/home/user/repo" {
		t.Fatal("the in-flight review's state must be untouched by the second press")
	}
	close(block)
}

// TestUpdateLandingReviewKeyDoesNotBlock proves the orchestration runs inside
// the returned tea.Cmd rather than inline in Update: m.reviewRun sets a flag
// only when it actually RUNS, and updateLanding must return long before that
// happens — the command is executed by the caller (the Bubble Tea runtime,
// or this test), never by updateLanding itself.
func TestUpdateLandingReviewKeyDoesNotBlock(t *testing.T) {
	m, v := landingTestVM(t, "web")
	seedOneCheckout(t, m, v, "/home/user/repo")
	m.ghActions = &fakeGhActions{}
	var ran bool
	m.reviewRun = func(ctx context.Context, sess *landreview.Session, w io.Writer) (string, error) {
		ran = true // would only flip if Update itself invoked the run function
		<-ctx.Done()
		return "", ctx.Err()
	}
	m.openLandingPane(v)

	type result struct {
		next tea.Model
		cmd  tea.Cmd
	}
	done := make(chan result, 1)
	go func() {
		next, cmd := m.updateLanding(tea.KeyPressMsg{Code: 'v', Text: "v"})
		done <- result{next, cmd}
	}()

	var r result
	select {
	case r = <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("updateLanding did not return promptly — the review must run in a tea.Cmd, not inline")
	}
	if ran {
		t.Fatal("the review ran INLINE in Update rather than inside the returned tea.Cmd")
	}
	if r.cmd == nil {
		t.Fatal("expected a non-nil command to run the review asynchronously")
	}
	// Release the still-blocked goroutine the command would start once
	// invoked: cancel it directly through the pane rather than running cmd,
	// which this test never needs to.
	m2 := r.next.(model)
	if m2.landing.reviewCancel != nil {
		m2.landing.reviewCancel()
	}
}

// TestHandleLandingReviewDoneClearsInProgressAndLogsSuccess drives the
// completion message through the handler directly (as Update's landReviewDoneMsg
// case does) and checks the row returns to normal and the outcome is logged.
func TestHandleLandingReviewDoneClearsInProgressAndLogsSuccess(t *testing.T) {
	m, v := landingTestVM(t, "web")
	seedOneCheckout(t, m, v, "/home/user/repo")
	m.ghActions = &fakeGhActions{}
	m.openLandingPane(v)
	m.landing.reviewPath = "/home/user/repo"
	m.landing.reviewCancel = func() {}

	m.handleLandReviewDone(landReviewDoneMsg{
		scope: v.scope, vm: v.Name, path: "/home/user/repo", written: "/home/user/repo/review.xml",
	})

	if m.landing.reviewPath != "" {
		t.Fatalf("reviewPath = %q after completion, want \"\" (the row must return to normal)", m.landing.reviewPath)
	}
	if m.landing.reviewCancel != nil {
		t.Fatal("reviewCancel must be cleared once the session has finished")
	}
	if strings.Contains(m.landingView(), "reviewing") {
		t.Fatalf("rendered view still shows the row under review after completion:\n%s", m.landingView())
	}
	if len(m.messages) == 0 || !strings.Contains(m.messages[len(m.messages)-1].text, "review.xml") {
		t.Fatalf("messages = %+v, want the last entry to name the written review.xml path", m.messages)
	}
}

// TestHandleLandingReviewDoneSurfacesErrorRatherThanDroppingIt is the acceptance
// criterion in the task file: an error must reach the pane's own log, not
// vanish — the browser tab (the only other place it could show) may already
// be closed by the time this message arrives.
func TestHandleLandingReviewDoneSurfacesErrorRatherThanDroppingIt(t *testing.T) {
	m, v := landingTestVM(t, "web")
	seedOneCheckout(t, m, v, "/home/user/repo")
	m.ghActions = &fakeGhActions{}
	m.openLandingPane(v)
	m.landing.reviewPath = "/home/user/repo"
	m.landing.reviewCancel = func() {}

	wantErr := errors.New("the review server never answered")
	m.handleLandReviewDone(landReviewDoneMsg{scope: v.scope, vm: v.Name, path: "/home/user/repo", err: wantErr})

	if m.landing.reviewPath != "" {
		t.Fatal("reviewPath must be cleared even when the review failed")
	}
	if len(m.messages) == 0 || !strings.Contains(m.messages[len(m.messages)-1].text, "the review server never answered") {
		t.Fatalf("messages = %+v, want the error surfaced in the pane's log", m.messages)
	}
}

// TestHandleLandingReviewDoneDropsStaleResult mirrors every other async
// completion handler on this pane (landCommitPushDoneMsg, landingPRStateMsg):
// a result for a VM the pane has since moved on from must not mutate the
// pane the user is currently looking at.
func TestHandleLandingReviewDoneDropsStaleResult(t *testing.T) {
	m, v := landingTestVM(t, "web")
	seedOneCheckout(t, m, v, "/home/user/repo")
	m.ghActions = &fakeGhActions{}
	m.openLandingPane(v)
	m.landing.reviewPath = "/home/user/repo"
	cancelCalled := false
	m.landing.reviewCancel = func() { cancelCalled = true }

	m.handleLandReviewDone(landReviewDoneMsg{scope: v.scope, vm: "some-other-vm", path: "/home/user/repo"})

	if m.landing.reviewPath != "/home/user/repo" {
		t.Fatal("a stale result must not clear the CURRENT pane's in-progress marker")
	}
	if cancelCalled {
		t.Fatal("a stale result must not touch the current review's cancel func")
	}
}

// TestLandingHelpIncludesReviewKey is the acceptance criterion that the
// footer advertises the new key alongside the existing act and rescan keys.
func TestLandingHelpIncludesReviewKey(t *testing.T) {
	m, v := landingTestVM(t, "web")
	seedOneCheckout(t, m, v, "/home/user/repo")
	m.ghActions = &fakeGhActions{}
	m.openLandingPane(v)

	var gotDesc string
	var foundKey bool
	for _, b := range m.landingHelp() {
		if b.Help().Key == "v" {
			foundKey = true
			gotDesc = b.Help().Desc
		}
	}
	if !foundKey {
		t.Fatal("landingHelp() does not include the review key")
	}
	if gotDesc != "review" {
		t.Fatalf("review key's help text = %q, want %q", gotDesc, "review")
	}
}

// TestUpdateLandingBackCancelsInFlightReview is the cancellation-on-leave
// acceptance requirement: navigating back to the board while a review is
// open must cancel its context so Session.Run's own teardown runs, rather
// than leaving the goroutine (and the guest server it is supervising)
// orphaned with no reachable cancel func once the pane's state is reset.
func TestUpdateLandingBackCancelsInFlightReview(t *testing.T) {
	m, v := landingTestVM(t, "web")
	seedOneCheckout(t, m, v, "/home/user/repo")
	m.ghActions = &fakeGhActions{}
	ctxCh := make(chan context.Context, 1)
	m.reviewRun = func(ctx context.Context, sess *landreview.Session, w io.Writer) (string, error) {
		ctxCh <- ctx
		<-ctx.Done()
		return "", ctx.Err()
	}
	m.openLandingPane(v)

	next, cmd := m.updateLanding(tea.KeyPressMsg{Code: 'v', Text: "v"})
	m2 := next.(model)
	go cmd()

	var ctx context.Context
	select {
	case ctx = <-ctxCh:
	case <-time.After(2 * time.Second):
		t.Fatal("the review command never started")
	}

	next2, _ := m2.updateLanding(tea.KeyPressMsg{Code: tea.KeyEsc})
	m3, ok := next2.(model)
	if !ok {
		t.Fatal("updateLanding did not return a model")
	}
	if m3.view != viewBoard {
		t.Fatalf("view after Back = %v, want viewBoard (Back must still navigate)", m3.view)
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Back did not cancel the in-flight review's context")
	}
}

// TestLandingQuitCmdCancelsInFlightReview is the other half of the cancellation
// requirement: quitting the whole program must tear the review down too, not
// just leave it running past process exit with an unreachable cancel func.
func TestLandingQuitCmdCancelsInFlightReview(t *testing.T) {
	m, v := landingTestVM(t, "web")
	seedOneCheckout(t, m, v, "/home/user/repo")
	m.ghActions = &fakeGhActions{}
	ctxCh := make(chan context.Context, 1)
	m.reviewRun = func(ctx context.Context, sess *landreview.Session, w io.Writer) (string, error) {
		ctxCh <- ctx
		<-ctx.Done()
		return "", ctx.Err()
	}
	m.openLandingPane(v)

	next, cmd := m.updateLanding(tea.KeyPressMsg{Code: 'v', Text: "v"})
	m2 := next.(model)
	go cmd()

	var ctx context.Context
	select {
	case ctx = <-ctxCh:
	case <-time.After(2 * time.Second):
		t.Fatal("the review command never started")
	}

	quit := m2.quitCmd()
	msg := quit()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("quitCmd() = %T, want tea.QuitMsg", msg)
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("quitCmd did not cancel the in-flight review before quitting")
	}
}

// TestLandingQuitCmdWaitsForTeardownBeforeQuitting is the regression test for
// the real defect a live-VM run turned up: cancelling the context is not a
// wait. m.reviewRun here keeps running for a beat AFTER ctx is cancelled
// (standing in for Session.Run's own guest-side kill round trip), and
// quitCmd() must not return the QuitMsg until that goroutine has actually
// finished — proven by measuring that quit() (called synchronously, exactly
// as the Bubble Tea runtime would drive this tea.Cmd) takes at least as long
// as the simulated teardown, not merely long enough to fire cancel().
func TestLandingQuitCmdWaitsForTeardownBeforeQuitting(t *testing.T) {
	m, v := landingTestVM(t, "web")
	seedOneCheckout(t, m, v, "/home/user/repo")
	m.ghActions = &fakeGhActions{}
	const teardown = 150 * time.Millisecond
	m.reviewRun = func(ctx context.Context, sess *landreview.Session, w io.Writer) (string, error) {
		<-ctx.Done()
		time.Sleep(teardown) // simulates Session.Run's own guest-side kill round trip
		return "", ctx.Err()
	}
	m.openLandingPane(v)

	next, cmd := m.updateLanding(tea.KeyPressMsg{Code: 'v', Text: "v"})
	m2 := next.(model)
	go cmd()
	// Give the goroutine above a moment to actually start and block on
	// ctx.Done(), so the elapsed measurement below times the teardown wait
	// and not scheduling jitter.
	time.Sleep(20 * time.Millisecond)

	start := time.Now()
	msg := m2.quitCmd()()
	elapsed := time.Since(start)

	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("quitCmd() = %T, want tea.QuitMsg", msg)
	}
	if elapsed < teardown {
		t.Fatalf("quitCmd() returned after %s, want it to have waited out the %s teardown", elapsed, teardown)
	}
}

// TestLandingQuitCmdGivesUpAfterTeardownTimeout proves quitCmd does not hang
// the program forever against a review whose teardown never reports back
// (an irrecoverably wedged guest): it shrinks quitTeardownTimeout for the
// duration of the test rather than sleeping out the real 15s budget.
func TestLandingQuitCmdGivesUpAfterTeardownTimeout(t *testing.T) {
	old := quitTeardownTimeout
	quitTeardownTimeout = 50 * time.Millisecond
	t.Cleanup(func() { quitTeardownTimeout = old })

	m, v := landingTestVM(t, "web")
	seedOneCheckout(t, m, v, "/home/user/repo")
	m.ghActions = &fakeGhActions{}
	release := make(chan struct{}) // never closed by the test: reviewRun never returns on its own
	t.Cleanup(func() { close(release) })
	m.reviewRun = func(ctx context.Context, sess *landreview.Session, w io.Writer) (string, error) {
		<-release
		return "", ctx.Err()
	}
	m.openLandingPane(v)

	next, cmd := m.updateLanding(tea.KeyPressMsg{Code: 'v', Text: "v"})
	m2 := next.(model)
	go cmd()
	time.Sleep(10 * time.Millisecond)

	done := make(chan tea.Msg, 1)
	go func() { done <- m2.quitCmd()() }()
	select {
	case msg := <-done:
		if _, ok := msg.(tea.QuitMsg); !ok {
			t.Fatalf("quitCmd() = %T, want tea.QuitMsg", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("quitCmd() did not give up after quitTeardownTimeout — a wedged guest would hang the program forever")
	}
}

// TestLandingQuitCmdWithNoReviewInFlightStillQuits is quitCmd's zero-value path: no
// review means nil cancel, and that must not panic or block the quit.
func TestLandingQuitCmdWithNoReviewInFlightStillQuits(t *testing.T) {
	m := newTestModel(t)
	msg := m.quitCmd()()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("quitCmd() = %T, want tea.QuitMsg", msg)
	}
}

// TestLandingDefaultReviewRunCallsSessionRun pins the production seam: defaultReviewRun
// must call through to the real Session.Run unmodified, not reimplement any
// part of task 5's orchestration. A Provider whose ForwardArgv returns nil and
// whose Shell exits immediately makes Run fail fast (the server "exited"
// before answering) rather than hang, which is all this needs to prove the
// wiring is real.
func TestLandingDefaultReviewRunCallsSessionRun(t *testing.T) {
	sess := &landreview.Session{
		Provider:     fakeReviewProvider{},
		Checkout:     checkouts.Checkout{Path: "/home/user/repo"},
		ReadyTimeout: 50 * time.Millisecond,
		PollInterval: 5 * time.Millisecond,
	}
	_, err := defaultReviewRun(context.Background(), sess, io.Discard)
	if err == nil {
		t.Fatal("expected an error — the fake guest command exits immediately without ever answering")
	}
}

// fakeReviewProvider is the minimal landreview.Provider double
// TestLandingDefaultReviewRunCallsSessionRun needs: a guest command that returns
// immediately (as if the server process exited straight away) and no
// forwarder (matching local Lima).
type fakeReviewProvider struct{}

func (fakeReviewProvider) Shell(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error {
	return nil
}
func (fakeReviewProvider) ShellOut(ctx context.Context, name string, argv ...string) ([]byte, error) {
	return nil, nil
}
func (fakeReviewProvider) ForwardArgv(v vm.VM, hostPort, guestPort int) []string {
	return nil
}

package provision

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// TestPhaseTimer_SummaryListsEachPhaseAndTotal verifies the summary block
// contains one line per recorded phase plus a non-negative TOTAL, and that no
// timing line uses the step() "==> " prefix (a "==> " line resets the TUI
// tile's progress bar — see internal/ui/ansible.go stepPrefix).
func TestPhaseTimer_SummaryListsEachPhaseAndTotal(t *testing.T) {
	var buf bytes.Buffer
	timer := newPhaseTimer(&buf)

	if err := timer.time("phase one", func() error {
		return nil
	}); err != nil {
		t.Fatalf("time(phase one): %v", err)
	}
	if err := timer.time("phase two", func() error {
		return nil
	}); err != nil {
		t.Fatalf("time(phase two): %v", err)
	}
	timer.summary()

	out := buf.String()
	for _, want := range []string{"phase one", "phase two", "TOTAL"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary output missing %q; got:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "==>") {
			t.Errorf("timing output must never use the step() prefix (resets TUI progress); got line: %q", line)
		}
	}
	if len(timer.phases) != 2 {
		t.Fatalf("expected 2 recorded phases, got %d", len(timer.phases))
	}
	for _, p := range timer.phases {
		if p.D < 0 {
			t.Errorf("phase %q has negative duration %v", p.Name, p.D)
		}
	}
}

// TestPhaseTimer_PropagatesError verifies time() still records the phase and
// returns the wrapped function's error rather than swallowing it.
func TestPhaseTimer_PropagatesError(t *testing.T) {
	var buf bytes.Buffer
	timer := newPhaseTimer(&buf)
	wantErr := errors.New("boom")

	err := timer.time("failing phase", func() error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("time() error = %v, want %v", err, wantErr)
	}
	if len(timer.phases) != 1 || timer.phases[0].Name != "failing phase" {
		t.Fatalf("expected failing phase recorded, got %+v", timer.phases)
	}
}

// TestPhaseTimer_NoStepCalls is a static guard against a "tidy-up" that routes
// timing lines through step(), which would blank the TUI tile's progress bar
// mid-run by matching internal/ui/ansible.go's "==> " stepPrefix reset.
func TestPhaseTimer_NoStepCalls(t *testing.T) {
	var buf bytes.Buffer
	timer := newPhaseTimer(&buf)
	_ = timer.time("x", func() error { return nil })
	timer.summary()
	if strings.Contains(buf.String(), "\n==> ") {
		t.Fatalf("timing output must never contain the step() \"==> \" prefix")
	}
}

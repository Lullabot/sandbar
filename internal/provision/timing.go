package provision

import (
	"fmt"
	"io"
	"time"
)

// phaseDuration records one lifecycle phase's name and how long it took.
type phaseDuration struct {
	Name string
	D    time.Duration
}

// phaseTimer measures wall-clock duration for each lifecycle phase of a
// create/reset run and emits a per-phase line plus a final summary block.
//
// Every write phaseTimer makes is a PLAIN write to out, never through step():
// step() prefixes its line with "==> ", which internal/ui/ansible.go parses as
// a progress RESET (it clears Role/Task/Index/Total on the TUI tile). Routing
// timing lines through step() would blank the tile's progress bar mid-run, so
// this is a hard constraint — do not "tidy up" phaseTimer to call step().
type phaseTimer struct {
	out    io.Writer
	phases []phaseDuration
}

// newPhaseTimer returns a phaseTimer that writes its per-phase and summary
// lines to out.
func newPhaseTimer(out io.Writer) *phaseTimer {
	return &phaseTimer{out: out}
}

// time runs fn, records its wall-clock duration under name, and prints a
// plain (non-step) timing line. It returns fn's error unchanged; the phase is
// recorded regardless of whether fn succeeded, so a failing phase still shows
// up in the summary.
func (t *phaseTimer) time(name string, fn func() error) error {
	start := time.Now()
	err := fn()
	d := time.Since(start)
	t.phases = append(t.phases, phaseDuration{Name: name, D: d})
	// PLAIN write — deliberately NOT step(). A "==> " prefix would reset the
	// TUI tile's progress bar (see internal/ui/ansible.go stepPrefix).
	fmt.Fprintf(t.out, "    [timing] %s: %s\n", name, d.Round(time.Millisecond))
	return err
}

// summary prints a compact phase -> duration block, ending with the total
// across all recorded phases. Like time(), every line here is a plain write.
func (t *phaseTimer) summary() {
	fmt.Fprintf(t.out, "\n    [timing] summary\n")
	var total time.Duration
	for _, p := range t.phases {
		fmt.Fprintf(t.out, "    [timing]   %-24s %s\n", p.Name, p.D.Round(time.Millisecond))
		total += p.D
	}
	fmt.Fprintf(t.out, "    [timing]   %-24s %s\n", "TOTAL", total.Round(time.Millisecond))
}

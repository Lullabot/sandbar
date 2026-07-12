package ui

import (
	"os"
	"strings"
	"testing"
)

// The fixtures are REAL, CAPTURED output — contiguous slices of actual
// `sand create` runs against real Lima (limactl 2.1.3, Debian 13 guest),
// verbatim down to limactl's hostagent chatter: the provisioner's own step
// banners, the guest's SAND_ANSIBLE_TASK_TOTAL marker, and the Ansible run
// through PLAY RECAP. Writing this parser against imagined output is how you
// ship one that works on your idea of Ansible and not on Ansible.
//
// The two phases exercise different halves of the grammar. The finalize run is
// mostly SKIPPED tasks (its heavy roles are gated off), which is what proves the
// guest's --list-tasks total is exact rather than optimistic: Ansible announces a
// banner for a task it goes on to skip, so the static count and the live count
// agree. The base run is the long one a user actually watches, and it is the one
// with real RUNNING HANDLER banners in it.
//
// One line of the base fixture is edited: the "Display generated password" task
// really does print the VM's password, and a captured secret does not belong in a
// repository, so that value alone is replaced with REDACTED-BY-SAND-TESTDATA. It
// is not a banner line and the parser never looks at it. Nothing else is touched.
var ansibleFixtures = []struct {
	name     string
	path     string
	total    int    // what the guest reported, and what the run then printed
	lastRole string // the run's final banner...
	lastTask string // ...which is a handler in the base phase
}{
	{"finalize phase", "testdata/ansible-finalize.log", 72, "project", "Clone the project"},
	{"base phase", "testdata/ansible-base.log", 72, "base", "Reload sshd"},
}

func loadFixture(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(b)
}

// parseAll feeds s to a fresh parser in chunks of size n (n <= 0 = all at once).
func parseAll(s string, n int) ansibleProgress {
	var p ansibleParser
	if n <= 0 {
		p.feed(s)
		return p.progress
	}
	for i := 0; i < len(s); i += n {
		end := i + n
		if end > len(s) {
			end = len(s)
		}
		p.feed(s[i:end])
	}
	return p.progress
}

// The parser against real captured output: it finds the play, the current role
// and task, the task's ordinal, and the total the guest reported.
func TestAnsibleParserRealOutput(t *testing.T) {
	for _, f := range ansibleFixtures {
		t.Run(f.name, func(t *testing.T) {
			fixture := loadFixture(t, f.path)

			// Derive the count FROM the fixture, so a banner the parser drops (or
			// invents) fails the test rather than quietly agreeing with a number I
			// typed in.
			wantIndex := strings.Count(fixture, "\nTASK [")
			if wantIndex < 10 {
				t.Fatalf("the fixture should carry a real run's worth of tasks, found %d", wantIndex)
			}

			got := parseAll(fixture, 0)

			// The guest's reported total and the banners the run actually printed must
			// AGREE. This is the whole basis for the tile's progress bar having an
			// honest denominator, and it is checked here against a real run rather than
			// asserted in a comment.
			if got.Total != f.total {
				t.Errorf("Total = %d, want %d (the guest's --list-tasks count + Gathering Facts)", got.Total, f.total)
			}
			if got.Index != wantIndex {
				t.Errorf("Index = %d, want %d — one per TASK banner in the fixture", got.Index, wantIndex)
			}
			if got.Total != got.Index {
				t.Errorf("the guest reported %d tasks but the run printed %d banners — the bar would never reach its end", got.Total, got.Index)
			}
			if got.Play != "Provision Claude Code development VM" {
				t.Errorf("Play = %q", got.Play)
			}
			if got.Role != f.lastRole || got.Task != f.lastTask {
				t.Errorf("Role/Task = %q/%q, want %q/%q (the run's last banner)", got.Role, got.Task, f.lastRole, f.lastTask)
			}
			if got.Fraction() != 1 {
				t.Errorf("Fraction = %v, want a full bar at the end of a completed run", got.Fraction())
			}
		})
	}
}

// A real RUNNING HANDLER banner names the work but must NOT advance the task
// count: --list-tasks does not list handlers, so counting them would push the bar
// past the total the guest reported. Asserted against the real base-phase run,
// which fires several.
func TestAnsibleParserRealHandlersDoNotCount(t *testing.T) {
	fixture := loadFixture(t, "testdata/ansible-base.log")
	handlers := strings.Count(fixture, "\nRUNNING HANDLER [")
	if handlers < 1 {
		t.Fatal("the base-phase fixture should contain real handler banners")
	}
	got := parseAll(fixture, 0)
	if got.Index > got.Total {
		t.Fatalf("Index %d exceeds Total %d: handlers were counted as tasks", got.Index, got.Total)
	}
}

// THE BUFFER-BOUNDARY CASE, which is the one that actually bites: the reader
// hands the parser 4096 bytes at a time, so a `TASK [role : name] ***` banner
// lands astride a chunk boundary sooner or later. A parser that matched
// per-chunk would silently drop those tasks — the count would drift low and the
// tile's bar would under-report for the rest of the run. Every chunking must
// produce the identical result, right down to a one-byte-at-a-time stream.
func TestAnsibleParserAcrossReadBoundaries(t *testing.T) {
	for _, f := range ansibleFixtures {
		t.Run(f.name, func(t *testing.T) {
			fixture := loadFixture(t, f.path)
			want := parseAll(fixture, 0)

			for _, n := range []int{1, 2, 3, 7, 13, 64, 512, 1024, 4093, 4096, 8192} {
				if got := parseAll(fixture, n); got != want {
					t.Errorf("chunk size %d: progress = %+v, want %+v", n, got, want)
				}
			}

			// And a split placed deliberately INSIDE a TASK banner's token, which is
			// the exact failure an unlucky chunk size would otherwise hide.
			i := strings.Index(fixture, "\nTASK [")
			if i < 0 {
				t.Fatal("the fixture should contain a TASK banner")
			}
			for _, off := range []int{1, 3, 6, 12, 20} { // inside "\nTASK [", and inside the role name
				var p ansibleParser
				p.feed(fixture[:i+off])
				p.feed(fixture[i+off:])
				if got := p.progress; got != want {
					t.Errorf("split %d bytes into a TASK banner: progress = %+v, want %+v", off, got, want)
				}
			}
		})
	}
}

// The parser's own grammar, on the shapes the real output showed: a role-less
// task, a role task, a handler (which names the work but does not advance the
// count), sand's phase banners (the only signal during the minutes before
// Ansible starts), and the marker that resets the counter between the two
// playbook runs a single create streams down one pipe.
func TestAnsibleParserGrammar(t *testing.T) {
	var p ansibleParser
	p.feed("==> Cloning \"web\" from base image \"claude-base\"…\n")
	if p.progress.Step != `Cloning "web" from base image "claude-base"…` {
		t.Fatalf("Step = %q", p.progress.Step)
	}
	if p.progress.Index != 0 {
		t.Fatalf("a phase banner has no Ansible task yet, got index %d", p.progress.Index)
	}

	p.feed("SAND_ANSIBLE_TASK_TOTAL=19\n")
	p.feed("PLAY [Provision Claude Code development VM] ****************\n")
	p.feed("TASK [Gathering Facts] *************************************\n")
	if got := p.progress; got.Total != 19 || got.Index != 1 || got.Role != "" || got.Task != "Gathering Facts" {
		t.Fatalf("role-less task: %+v", got)
	}

	p.feed("TASK [dev-tools : Install Docker] **************************\n")
	if got := p.progress; got.Index != 2 || got.Role != "dev-tools" || got.Task != "Install Docker" {
		t.Fatalf("role task: %+v", got)
	}
	if got := p.progress.Fraction(); got != 2.0/19.0 {
		t.Fatalf("Fraction = %v, want 2/19", got)
	}

	p.feed("RUNNING HANDLER [dev-tools : Restart Docker] ***************\n")
	if got := p.progress; got.Index != 2 || got.Task != "Restart Docker" {
		t.Fatalf("a handler names the work but must not advance the count: %+v", got)
	}

	// The second playbook run of the same job (a create streams the base phase and
	// then the finalize phase): the marker resets the counter, so the tile never
	// shows the previous phase's last task, nor a count that keeps climbing.
	p.feed("SAND_ANSIBLE_TASK_TOTAL=72\n")
	if got := p.progress; got.Total != 72 || got.Index != 0 || got.Task != "" {
		t.Fatalf("a new run should reset the counter: %+v", got)
	}
}

// Fraction is what fills the tile's bar: unknown until the guest reports a total
// (an indeterminate bar, not a lie), and clamped at 1 so an unexpected extra
// banner can never render past the end of its own bar.
func TestAnsibleProgressFraction(t *testing.T) {
	cases := []struct {
		p    ansibleProgress
		want float64
	}{
		{ansibleProgress{}, 0},
		{ansibleProgress{Index: 7, Total: 0}, 0}, // total not reported: no denominator, no bar
		{ansibleProgress{Index: 0, Total: 19}, 0},
		{ansibleProgress{Index: 19, Total: 19}, 1},
		{ansibleProgress{Index: 25, Total: 19}, 1}, // clamped, never past the end
	}
	for _, c := range cases {
		if got := c.p.Fraction(); got != c.want {
			t.Errorf("%+v.Fraction() = %v, want %v", c.p, got, c.want)
		}
	}
}

// A stream that never sends a newline (a carriage-return progress bar, a binary
// blob) must not grow the partial-line buffer without limit.
func TestAnsibleParserBoundsItsPartialLine(t *testing.T) {
	var p ansibleParser
	for i := 0; i < 64; i++ {
		p.feed(strings.Repeat("x", 4096))
	}
	if len(p.partial) > maxPartialLine {
		t.Fatalf("partial line grew to %d bytes, want it bounded at %d", len(p.partial), maxPartialLine)
	}
	// And it still parses the next real banner once a newline finally arrives.
	p.feed("\nTASK [base : Set hostname] ****\n")
	if p.progress.Task != "Set hostname" {
		t.Fatalf("the parser should recover after dropping an unbounded fragment, got %+v", p.progress)
	}
}

package ui

// ansible.go parses the provisioner's streamed output into the progress a
// building VM's tile renders: which Ansible role and task are running now, and
// how far through the play they are (`ansible: docker · 7/19`).
//
// Two things about this parser are load-bearing.
//
// IT IS LINE-BUFFERED, deliberately. The reader hands us whatever the pipe has —
// up to 4096 bytes at a time — so a `TASK [role : name] ***` banner WILL
// eventually land astride a chunk boundary. A parser that matched per-chunk
// would drop those tasks silently: the index would drift low and the tile's bar
// would under-report for the rest of the run, which is worse than no bar at all.
// So a chunk is appended to a partial line and only WHOLE lines are ever matched.
//
// THE TOTAL COMES FROM THE GUEST, not from a guess. Ansible's default stdout
// callback prints no task count anywhere in its output, so the denominator has to
// be supplied: the in-guest script (internal/provision/provision.go) runs
// `ansible-playbook --list-tasks` first and echoes the count as the
// SAND_ANSIBLE_TASK_TOTAL marker below. That count is exact, not an estimate,
// because Ansible announces a TASK banner even for a task it goes on to skip —
// a `when:`-gated role still prints every one of its tasks and then "skipping:".
// (Verified against a real base-phase run: 71 listed tasks + the ungated
// "Gathering Facts" = the 72 TASK banners the run actually printed.)

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Line shapes the parser recognises, all anchored at the start of a line.
const (
	playPrefix    = "PLAY ["            // PLAY [Provision Claude Code development VM] ****
	taskPrefix    = "TASK ["            // TASK [base : Set hostname] ****
	handlerPrefix = "RUNNING HANDLER [" // RUNNING HANDLER [base : Reload sshd] ****
	stepPrefix    = "==> "              // sand's own phase banner (see provision.step)

	// totalPrefix is the marker the in-guest script echoes immediately before each
	// ansible-playbook run, carrying the number of TASK banners that run will
	// print. It also delimits one playbook run from the next: a create streams TWO
	// runs (the base phase, then the finalize phase) down a single job's pipe, so
	// the marker is what resets the counter between them.
	totalPrefix = "SAND_ANSIBLE_TASK_TOTAL="

	// maxPartialLine bounds the unterminated tail we are willing to hold. Nothing
	// we match is remotely this long, so a stream that never sends a newline (a
	// carriage-return progress bar, a binary blob) drops its tail rather than
	// growing the buffer without limit.
	maxPartialLine = 64 << 10
)

// ansibleProgress is one job's parsed position in its provisioning run. It is a
// plain value: it is copied out of the registry into every jobSnapshot.
type ansibleProgress struct {
	// Step is sand's own phase banner ("Cloning "web" from base image…"), which
	// the provisioner writes precisely because the long stretches before Ansible
	// starts — the Debian download, the boot, the clone — are otherwise silent.
	// It is the tile's only signal during those minutes.
	Step string

	Play string // the current play's name
	Role string // the current task's role ("dev-tools"), empty for a role-less task
	Task string // the current task's name ("Install Docker")

	// Index is the 1-based ordinal of the current TASK banner within the current
	// playbook run; Total is how many that run will print (0 = not yet known).
	// Handlers do not advance Index: they are not part of the counted task list.
	Index int
	Total int
}

// Fraction is the progress bar's fill, in [0,1]. It is 0 when the total is not
// known yet, and clamped at 1 so an unexpected extra banner (a handler-heavy run
// against a stale total) can never render a bar past its end.
func (p ansibleProgress) Fraction() float64 {
	switch {
	case p.Total <= 0 || p.Index <= 0:
		return 0
	case p.Index >= p.Total:
		return 1
	}
	return float64(p.Index) / float64(p.Total)
}

// ansibleParser accumulates a job's byte stream and tracks its progress. It
// lives on a *job inside the job registry — never on the model, which is copied
// by value — and is only ever touched under the registry's lock.
type ansibleParser struct {
	partial  string // bytes since the last newline: the head of a line whose tail has not arrived
	progress ansibleProgress
}

// feed folds one chunk of streamed output into the parser. Only complete lines
// are matched; a trailing fragment is held until its newline arrives, which is
// what makes a banner split across a read boundary parse correctly.
func (p *ansibleParser) feed(chunk string) {
	p.partial += chunk
	for {
		i := strings.IndexByte(p.partial, '\n')
		if i < 0 {
			break
		}
		p.line(p.partial[:i])
		p.partial = p.partial[i+1:]
	}
	if len(p.partial) > maxPartialLine {
		// Nothing we match is this long, so the fragment cannot be a banner we are
		// waiting on. Drop it rather than grow forever.
		p.partial = ""
	}
}

// line matches one complete line of output. It strips ANSI first: Ansible emits
// none when its stdout is a pipe (as it is here), but a coloured stream must not
// silently stop matching.
func (p *ansibleParser) line(l string) {
	l = strings.TrimRight(ansi.Strip(l), " \t\r")

	switch {
	case strings.HasPrefix(l, totalPrefix):
		// A new playbook run begins here: reset the counter and the last run's task
		// so the tile never shows the base phase's final task during finalize.
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(l, totalPrefix)))
		if err != nil || n < 0 {
			return
		}
		p.progress.Total = n
		p.progress.Index = 0
		p.progress.Play, p.progress.Role, p.progress.Task = "", "", ""

	case strings.HasPrefix(l, stepPrefix):
		// A sand phase banner supersedes whatever Ansible was last doing: the
		// previous run's last task is over, and this phase may not run Ansible at all.
		p.progress = ansibleProgress{Step: strings.TrimSpace(strings.TrimPrefix(l, stepPrefix))}

	case strings.HasPrefix(l, taskPrefix):
		if name, ok := bracketed(l, taskPrefix); ok {
			p.progress.Index++
			p.progress.Role, p.progress.Task = splitRoleTask(name)
		}

	case strings.HasPrefix(l, handlerPrefix):
		// A handler is real work worth naming, but it is not in the counted task
		// list (--list-tasks does not list handlers), so it must not advance Index.
		if name, ok := bracketed(l, handlerPrefix); ok {
			p.progress.Role, p.progress.Task = splitRoleTask(name)
		}

	case strings.HasPrefix(l, playPrefix):
		if name, ok := bracketed(l, playPrefix); ok {
			p.progress.Play = name
		}
	}
}

// bracketed pulls the name out of a banner line: everything between the prefix's
// opening bracket and the LAST closing bracket on the line, which is the real
// one — Ansible pads every banner with trailing asterisks, and a task name may
// itself contain a bracket.
func bracketed(line, prefix string) (string, bool) {
	rest := line[len(prefix):]
	end := strings.LastIndex(rest, "]")
	if end < 0 {
		return "", false // the line is truncated or is not a banner after all
	}
	return strings.TrimSpace(rest[:end]), true
}

// splitRoleTask splits Ansible's "role : task" banner name. A task outside a role
// ("Gathering Facts") has no role, which is not an error — it is just a task.
func splitRoleTask(s string) (role, task string) {
	if i := strings.Index(s, " : "); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+len(" : "):])
	}
	return "", s
}

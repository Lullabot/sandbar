package ui

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/lullabot/sandbar/internal/vm"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// readPipe bundles the read end of the provisioner pipe. It is stored on the
// model (by pointer) so successive readNextCmds keep reading the same stream.
type readPipe struct {
	r *io.PipeReader
}

// provisionFunc matches Provisioner.CreateVM / Recreate.
type provisionFunc func(ctx context.Context, cfg vm.CreateConfig, out io.Writer) error

// streamFunc is a generic streaming operation writing to out and honouring ctx.
// It backs both provisioning and file transfers.
type streamFunc func(ctx context.Context, out io.Writer) error

// beginStream sets up the io.Pipe → viewport → spinner plumbing shared by
// provisioning and file transfers, spawns run in a goroutine feeding the pipe,
// and returns the commands that stream the pipe and animate the spinner. This is
// the non-blocking pattern: Update never runs the operation directly — it only
// reacts to the messages this produces. m.provCfg is cleared here so a run is NOT
// recorded as managed unless the caller sets it afterwards (beginProvision does).
func (m *model) beginStream(title string, back view, run streamFunc) tea.Cmd {
	pr, pw := io.Pipe()
	m.reader = &readPipe{r: pr}
	m.running = true
	m.canceled = false
	m.doneErr = nil
	m.output = ""
	m.progressTitle = title
	m.progressBack = back // where esc returns once the run finishes
	m.view = viewProgress
	m.viewport.SetContent("")
	m.provCfg = vm.CreateConfig{}

	// A cancellable context lets ctrl+c (in Update) abort the run mid-flight,
	// killing the limactl subprocess the operation is currently blocked on.
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	go func() {
		// CloseWithError(nil) closes the writer cleanly, surfacing io.EOF to the
		// reader; a non-nil err surfaces as that error on the final Read.
		pw.CloseWithError(run(ctx, pw))
	}()

	return tea.Batch(readNextCmd(m.reader), m.spinner.Tick)
}

// beginProvision launches a provisioner call through beginStream and records its
// config so a successful run can be marked managed (and reproduced faithfully on
// a future recreate).
func (m *model) beginProvision(title string, run provisionFunc, cfg vm.CreateConfig) tea.Cmd {
	cmd := m.beginStream(title, viewList, func(ctx context.Context, out io.Writer) error {
		return run(ctx, cfg, out)
	})
	m.provCfg = cfg
	return cmd
}

// readNextCmd reads one chunk from the provisioner pipe. It emits a
// provisionOutputMsg per chunk (Update re-issues it to read the next one) and a
// provisionDoneMsg at EOF or on error. Reading happens off the Update goroutine.
func readNextCmd(rp *readPipe) tea.Cmd {
	return func() tea.Msg {
		if rp == nil {
			return provisionDoneMsg{}
		}
		buf := make([]byte, 4096)
		n, err := rp.r.Read(buf)
		if n > 0 {
			return provisionOutputMsg(buf[:n])
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return provisionDoneMsg{}
			}
			return provisionDoneMsg{err: err}
		}
		return provisionOutputMsg("")
	}
}

// updateProgress handles keys on the progress view. While a build runs, ctrl+c
// (handled in Update) cancels it and all other keys only scroll the output — q
// and esc must not quit or navigate away and abandon the running provisioner.
// Once it's done, q quits and back/enter return to the refreshed list.
func (m model) updateProgress(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if !m.running {
		if key.Matches(msg, m.keys.Quit) {
			return m, tea.Quit
		}
		if key.Matches(msg, m.keys.Back) || key.Matches(msg, m.keys.Enter) {
			m.view = m.progressBack // detail for a transfer, list for a provision
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

// progressView renders the spinner/title, the scrollable output box, the final
// status, and the help bar.
func (m model) progressView() string {
	var b strings.Builder

	head := m.progressTitle
	if m.running {
		head = m.spinner.View() + " " + head
	}
	b.WriteString(titleStyle.Render(head))
	b.WriteString("\n\n")
	b.WriteString(boxStyle.Render(m.viewport.View()))
	b.WriteString("\n")

	if !m.running {
		back := "list"
		if m.progressBack == viewDetail {
			back = "VM"
		}
		switch {
		case m.canceled:
			b.WriteString("\n" + statusStyle.Render("Canceled — press esc to return to the "+back+"."))
		case m.doneErr != nil:
			b.WriteString("\n" + errStyle.Render("Failed: "+m.doneErr.Error()))
		default:
			b.WriteString("\n" + okStyle.Render("Done — press esc to return to the "+back+"."))
		}
	}

	b.WriteString("\n" + m.help.ShortHelpView(m.viewHelp()))
	return appStyle.Render(b.String())
}

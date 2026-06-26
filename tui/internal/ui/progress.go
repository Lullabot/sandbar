package ui

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/deviantintegral/claude-code-ansible/tui/internal/vm"

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

// beginProvision launches a provisioner call in a goroutine that writes to an
// io.Pipe, and returns the commands that (a) stream the pipe into the viewport
// and (b) animate the spinner. This is the non-blocking pattern: Update never
// calls the provisioner directly — it only reacts to the messages this produces.
func (m *model) beginProvision(title string, run provisionFunc, cfg vm.CreateConfig) tea.Cmd {
	pr, pw := io.Pipe()
	m.reader = &readPipe{r: pr}
	m.running = true
	m.doneErr = nil
	m.output = ""
	m.progressTitle = title
	m.view = viewProgress
	m.viewport.SetContent("")
	// Remember the config so a successful run can be recorded as managed (and
	// reproduced faithfully on a future recreate).
	m.provCfg = cfg

	go func() {
		// CloseWithError(nil) closes the writer cleanly, surfacing io.EOF to the
		// reader; a non-nil err surfaces as that error on the final Read.
		pw.CloseWithError(run(context.Background(), cfg, pw))
	}()

	return tea.Batch(readNextCmd(m.reader), m.spinner.Tick)
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

// updateProgress handles keys on the progress view. While running, keys only
// scroll the viewport (and ctrl+c quits); once done, back/enter return to the
// refreshed list.
func (m model) updateProgress(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, m.keys.Quit) {
		return m, tea.Quit
	}
	if !m.running {
		if key.Matches(msg, m.keys.Back) || key.Matches(msg, m.keys.Enter) {
			m.view = viewList
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
		if m.doneErr != nil {
			b.WriteString("\n" + errStyle.Render("Failed: "+m.doneErr.Error()))
		} else {
			b.WriteString("\n" + okStyle.Render("Done — press esc to return to the list."))
		}
	}

	b.WriteString("\n" + m.help.ShortHelpView(m.viewHelp()))
	return appStyle.Render(b.String())
}

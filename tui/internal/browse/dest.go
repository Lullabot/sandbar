package browse

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// NormalizePath cleans a pasted or drag-dropped path so it is immediately
// usable: it trims surrounding whitespace, drops an optional file:// (or
// file://localhost) scheme, strips one layer of surrounding matching quotes, and
// un-escapes backslash-escaped non-alphanumeric characters (a terminal escapes
// spaces/parens on drag-drop, e.g. "/a\ b" -> "/a b"). It returns plain text
// only — percent-decoding of file:// URLs is intentionally out of scope for v1,
// and the result is always passed downstream as a discrete process argument,
// never interpolated into a shell string.
func NormalizePath(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "file://localhost")
	s = strings.TrimPrefix(s, "file://")
	// Strip one layer of surrounding matching quotes.
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			s = s[1 : len(s)-1]
		}
	}
	// Drop a backslash that escapes a non-alphanumeric char (spaces, parens, …).
	// Byte iteration is fine: paths are UTF-8 and the escapes we drop are ASCII.
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			nxt := s[i+1]
			isAlnum := (nxt >= 'a' && nxt <= 'z') || (nxt >= 'A' && nxt <= 'Z') || (nxt >= '0' && nxt <= '9')
			if !isAlnum {
				continue // skip the backslash; the escaped char is kept next iteration
			}
		}
		b.WriteByte(s[i])
	}
	return strings.TrimSpace(b.String())
}

// DestInput is a copy-safe wrapper over a textinput.Model for entering the
// transfer destination directory. It pre-fills a sensible default and normalizes
// pasted/drag-dropped paths so a dropped path lands clean. It holds only the
// textinput.Model, so it is safe to embed in the root model (passed by value).
type DestInput struct{ ti textinput.Model }

// NewDestInput builds a focused destination input showing prompt and pre-filled
// with def (cursor at the end so the default is easy to edit or replace).
func NewDestInput(prompt, def string) DestInput {
	ti := textinput.New()
	ti.Prompt = prompt
	ti.SetValue(def)
	ti.CursorEnd()
	_ = ti.Focus()
	return DestInput{ti: ti}
}

// Value returns the current destination text.
func (d DestInput) Value() string { return d.ti.Value() }

// View renders the input.
func (d DestInput) View() string { return d.ti.View() }

// Update feeds a message to the input. A bracketed paste (a KeyMsg with
// Paste==true, which is also how terminals surface drag-and-drop) is normalized
// before it lands; everything else is ordinary editing.
func (d DestInput) Update(msg tea.Msg) (DestInput, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.Paste {
		d.ti.SetValue(NormalizePath(string(k.Runes)))
		d.ti.CursorEnd()
		return d, nil
	}
	var cmd tea.Cmd
	d.ti, cmd = d.ti.Update(msg)
	return d, cmd
}

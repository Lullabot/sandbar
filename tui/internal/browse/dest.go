package browse

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

// maxSuggestRows caps the visible autocomplete dropdown.
const maxSuggestRows = 8

var selectedSuggestStyle = lipgloss.NewStyle().Reverse(true)

// dirSuggestMsg carries a directory listing requested for autocomplete. It is an
// internal browse type routed back to DestInput.Update by the root model.
type dirSuggestMsg struct {
	dir     string
	entries []string
	err     error
}

// DestInput is a copy-safe destination-directory field with directory
// autocomplete: as the user types, the subdirectories of the directory portion of
// the path are offered as a dropdown; ↑/↓ move through them and enter fills in the
// highlighted directory (drilling one level deeper), after which ctrl+s runs the
// copy. Paste/drag-drop is normalized. It holds only value types, an interface,
// and freshly-allocated slices, so it is safe to embed in the root model (passed
// by value).
type DestInput struct {
	ti      textinput.Model
	lister  DirLister
	dir     string   // directory currently listed (cache key + relevance guard)
	entries []string // subdirectory names in dir, sorted
	matches []string // entries filtered by the trailing path component
	cursor  int      // highlighted match, or -1 when editing the field
}

// NewDestInput builds a focused destination field pre-filled with def and returns
// a command that lists def's directory for autocomplete. lister lists the
// destination side (host for a download, guest for an upload); it may be nil to
// disable completion.
func NewDestInput(prompt, def string, lister DirLister) (DestInput, tea.Cmd) {
	ti := textinput.New()
	ti.Prompt = prompt
	ti.SetValue(def)
	ti.CursorEnd()
	_ = ti.Focus()
	d := DestInput{ti: ti, lister: lister, cursor: -1}
	d.dir, _ = splitDest(def)
	return d, d.suggest(d.dir)
}

// Value returns the current destination text.
func (d DestInput) Value() string { return d.ti.Value() }

// suggest lists dir's subdirectories off the Update goroutine (the guest lister
// shells out over SSH), yielding a dirSuggestMsg.
func (d DestInput) suggest(dir string) tea.Cmd {
	lister := d.lister
	return func() tea.Msg {
		if lister == nil {
			return dirSuggestMsg{dir: dir}
		}
		entries, err := lister.List(context.Background(), dir)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir {
				names = append(names, e.Name)
			}
		}
		sort.Strings(names)
		return dirSuggestMsg{dir: dir, entries: names, err: err}
	}
}

// splitDest splits a path into the directory to list and the trailing component
// to complete: "/a/b/c" -> ("/a/b","c"), "/a/b/" -> ("/a/b",""), "/" -> ("/",""),
// "" -> ("",""), "x" -> ("","x"). POSIX "/" applies to both host and guest paths.
func splitDest(v string) (dir, prefix string) {
	i := strings.LastIndex(v, "/")
	switch {
	case i < 0:
		return "", v
	case i == 0:
		return "/", v[1:]
	default:
		return v[:i], v[i+1:]
	}
}

// refilter recomputes matches from entries against the current trailing prefix
// (case-insensitive prefix match) and resets the highlight.
func (d *DestInput) refilter() {
	_, prefix := splitDest(d.ti.Value())
	lp := strings.ToLower(prefix)
	var matches []string
	for _, name := range d.entries {
		if strings.HasPrefix(strings.ToLower(name), lp) {
			matches = append(matches, name)
		}
	}
	d.matches = matches
	d.cursor = -1
}

// maybeRelist requests a fresh listing when the directory portion of the value
// changed, otherwise just refilters the cached entries. Returns a suggest command
// or nil.
func (d *DestInput) maybeRelist() tea.Cmd {
	dir, _ := splitDest(d.ti.Value())
	if dir != d.dir {
		d.dir = dir
		d.entries = nil
		d.refilter()
		return d.suggest(dir)
	}
	d.refilter()
	return nil
}

// accept fills the highlighted match into the field as dir/<name>/ and lists it
// so the user can keep drilling; returns the suggest command for the new
// directory. It is a no-op (nil) when no match is highlighted.
func (d *DestInput) accept() tea.Cmd {
	if d.cursor < 0 || d.cursor >= len(d.matches) {
		return nil
	}
	dir, _ := splitDest(d.ti.Value())
	sep := "/"
	if dir == "" || strings.HasSuffix(dir, "/") {
		sep = ""
	}
	val := dir + sep + d.matches[d.cursor] + "/"
	d.ti.SetValue(val)
	d.ti.CursorEnd()
	d.dir = strings.TrimSuffix(val, "/")
	if d.dir == "" {
		d.dir = "/"
	}
	d.entries = nil
	d.matches = nil
	d.cursor = -1
	return d.suggest(d.dir)
}

// Update feeds a message to the field. Async directory listings (dirSuggestMsg)
// populate the dropdown; ↑/↓ move the highlight, enter fills the highlighted
// directory, a bracketed paste is normalized, and any other edit refilters (and
// re-lists when the directory portion changed).
func (d DestInput) Update(msg tea.Msg) (DestInput, tea.Cmd) {
	switch msg := msg.(type) {
	case dirSuggestMsg:
		if msg.dir != d.dir {
			return d, nil // stale: the user has since moved to another directory
		}
		d.entries = msg.entries
		d.refilter()
		return d, nil
	case tea.KeyMsg:
		if msg.Paste {
			d.ti.SetValue(NormalizePath(string(msg.Runes)))
			d.ti.CursorEnd()
			return d, d.maybeRelist()
		}
		switch msg.Type {
		case tea.KeyDown:
			if d.cursor < len(d.matches)-1 {
				d.cursor++
			}
			return d, nil
		case tea.KeyUp:
			if d.cursor >= 0 {
				d.cursor--
			}
			return d, nil
		case tea.KeyEnter:
			return d, d.accept()
		}
	}
	// Only re-list/refilter when the value actually changed. Cursor-blink ticks
	// and other periodic messages also reach here; refiltering on those would reset
	// the highlighted suggestion on every blink.
	before := d.ti.Value()
	var cmd tea.Cmd
	d.ti, cmd = d.ti.Update(msg)
	if d.ti.Value() == before {
		return d, cmd
	}
	return d, tea.Batch(cmd, d.maybeRelist())
}

// View renders the input line followed by the directory-completion dropdown.
func (d DestInput) View() string {
	var b strings.Builder
	b.WriteString(d.ti.View())
	for i, name := range d.matches {
		if i >= maxSuggestRows {
			b.WriteString("\n  … " + strconv.Itoa(len(d.matches)-maxSuggestRows) + " more")
			break
		}
		line := name + "/"
		if i == d.cursor {
			b.WriteString("\n› " + selectedSuggestStyle.Render(line))
		} else {
			b.WriteString("\n  " + line)
		}
	}
	return b.String()
}

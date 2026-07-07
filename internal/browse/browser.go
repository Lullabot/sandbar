package browse

import (
	"context"
	"path"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

// selectKey chooses the highlighted entry regardless of type — distinct from
// Enter (which navigates into a directory) so picking a directory as a
// recursive-copy source never collides with entering it.
var selectKey = key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "select"))

// item wraps a DirEntry as a bubbles/list item. up marks the synthetic ".."
// parent entry.
type item struct {
	e  DirEntry
	up bool
}

func (i item) Title() string {
	if i.up {
		return ".."
	}
	if i.e.IsDir {
		return i.e.Name + "/"
	}
	return i.e.Name
}

// Description satisfies list.DefaultItem but is unused: the browser hides
// descriptions (a directory is obvious from its trailing "/").
func (i item) Description() string { return "" }

func (i item) FilterValue() string {
	if i.up {
		return ".."
	}
	return i.e.Name
}

// dirLoadedMsg carries the result of a DirLister.List call issued by Open. It is
// delivered to Update, which applies the entries (or surfaces the error).
type dirLoadedMsg struct {
	path    string
	entries []DirEntry
	err     error
}

// Browser is a source-agnostic file browser over a DirLister, built on
// bubbles/list for its fuzzy filter. It is copy-safe (only a list.Model,
// interface, and small scalars), so it embeds cleanly in the root model that
// Bubble Tea passes by value.
type Browser struct {
	lister   DirLister
	list     list.Model
	path     string // current absolute directory
	selPath  string // last selection (absolute)
	selDir   bool   // last selection is a directory (=> recursive copy)
	selected bool   // a selection is pending for the caller to read
	err      error  // last load error, shown in View
}

// NewBrowser builds a Browser over lister with the given list title. Call Open to
// populate it and SetSize to fit the terminal.
func NewBrowser(lister DirLister, title string) Browser {
	// A compact, single-line delegate: no per-item description (a directory is
	// obvious from its trailing "/") and no blank line between entries.
	d := list.NewDefaultDelegate()
	d.ShowDescription = false
	d.SetHeight(1)
	d.SetSpacing(0)
	l := list.New(nil, d, 0, 0)
	l.Title = title
	return Browser{lister: lister, list: l}
}

// Open returns a tea.Cmd that lists path off the Update goroutine (the guest
// lister shells out over SSH) and yields a dirLoadedMsg. Use it to enter the
// initial directory and to navigate.
func (b Browser) Open(path string) tea.Cmd {
	lister, p := b.lister, path
	return func() tea.Msg {
		entries, err := lister.List(context.Background(), p)
		return dirLoadedMsg{path: p, entries: entries, err: err}
	}
}

// SetSize fits the inner list to the given dimensions.
func (b *Browser) SetSize(w, h int) { b.list.SetSize(w, h) }

// NotFiltering reports whether the list is NOT in its active-typing filter state,
// so the caller can decide whether esc should back out (vs. cancel the filter).
func (b Browser) NotFiltering() bool { return b.list.FilterState() != list.Filtering }

// Selected reports the last chosen entry: its absolute path, whether it is a
// directory (a recursive copy), and whether a selection is pending. Navigating to
// a new directory clears a pending selection.
func (b Browser) Selected() (string, bool, bool) { return b.selPath, b.selDir, b.selected }

// ClearSelection discards a pending selection so the caller can re-enter the
// browser (e.g. after backing out of the destination prompt) and navigate or pick
// a different source, instead of being bounced straight back to the prompt on the
// next keystroke.
func (b *Browser) ClearSelection() { b.selected = false }

// Update handles async load results and key input. Enter navigates into a
// directory (or selects a file); the select key chooses the highlighted entry of
// any type. While the fuzzy filter is being typed, keys are delegated to the list
// so filtering works.
func (b Browser) Update(msg tea.Msg) (Browser, tea.Cmd) {
	switch msg := msg.(type) {
	case dirLoadedMsg:
		if msg.err != nil {
			b.err = msg.err // surface the error; keep any already-loaded items visible
			// On the INITIAL load there are no prior items and ".." is only added on
			// success, so a missing/inaccessible start directory would trap the user in
			// an empty list. Seed a ".." entry (unless at root) so they can still
			// navigate up out of it.
			if len(b.list.Items()) == 0 && msg.path != "/" {
				b.path = msg.path
				return b, b.list.SetItems([]list.Item{item{up: true}})
			}
			return b, nil
		}
		b.err = nil
		b.selected = false // navigating clears any stale selection
		b.path = msg.path
		items := make([]list.Item, 0, len(msg.entries)+1)
		if msg.path != "/" {
			items = append(items, item{up: true})
		}
		for _, e := range msg.entries {
			items = append(items, item{e: e})
		}
		cmd := b.list.SetItems(items)
		b.list.ResetFilter() // show the new directory in full, not narrowed by the old filter
		b.list.ResetSelected()
		return b, cmd

	case tea.KeyMsg:
		// While the user is typing a filter, let the list consume every key so it
		// does not steal navigate/select.
		if b.list.FilterState() == list.Filtering {
			var cmd tea.Cmd
			b.list, cmd = b.list.Update(msg)
			return b, cmd
		}
		switch {
		case key.Matches(msg, selectKey):
			if it, ok := b.list.SelectedItem().(item); ok {
				b.applySelect(it)
			}
			return b, nil
		case msg.Type == tea.KeyEnter:
			it, ok := b.list.SelectedItem().(item)
			if !ok {
				return b, nil
			}
			switch {
			case it.up:
				return b, b.Open(parent(b.path))
			case it.e.IsDir:
				return b, b.Open(join(b.path, it.e.Name))
			default:
				b.applySelect(it) // a file: Enter selects it
				return b, nil
			}
		}
	}

	var cmd tea.Cmd
	b.list, cmd = b.list.Update(msg)
	return b, cmd
}

// applySelect records the chosen entry as the pending selection.
func (b *Browser) applySelect(it item) {
	if it.up {
		b.selPath = parent(b.path)
		b.selDir = true
	} else {
		b.selPath = join(b.path, it.e.Name)
		b.selDir = it.e.IsDir
	}
	b.selected = true
	// Clear the filter so returning to the browser (e.g. esc from the dest prompt)
	// shows the full listing again rather than the previously narrowed one.
	b.list.ResetFilter()
}

// View renders the list and any load error.
func (b Browser) View() string {
	v := b.list.View()
	v += "\nenter: open dir · ctrl+s: select · /: filter"
	if b.err != nil {
		v += "\nerror: " + b.err.Error()
	}
	return v
}

// join and parent use POSIX path semantics (not path/filepath) so guest paths
// resolve identically regardless of the host OS.
func join(dir, name string) string { return path.Join(dir, name) }

func parent(dir string) string { return path.Dir(path.Clean(dir)) }

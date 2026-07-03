package browse

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeLister is an in-memory DirLister keyed by absolute path, so the Browser can
// be driven without a real VM or filesystem.
type fakeLister map[string][]DirEntry

func (f fakeLister) List(_ context.Context, p string) ([]DirEntry, error) { return f[p], nil }

// runCmd executes a tea.Cmd and returns its message (nil-safe).
func runCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

// TestBrowserNavigateAndSelect covers YOUR logic: Open populates items, moving to
// a directory and pressing the select key reports the right absolute path with
// isDir==true, and Enter into a directory schedules a reload of the child path.
func TestBrowserNavigateAndSelect(t *testing.T) {
	f := fakeLister{
		"/root":     {{Name: "sub", IsDir: true}, {Name: "a.txt", Size: 3}},
		"/root/sub": {{Name: "inner.txt", Size: 7}},
	}
	b := NewBrowser(f, "test")
	b.SetSize(80, 24)

	// Open("/root") -> dirLoadedMsg -> Update applies items.
	loaded := runCmd(b.Open("/root"))
	if _, ok := loaded.(dirLoadedMsg); !ok {
		t.Fatalf("Open should yield a dirLoadedMsg, got %T", loaded)
	}
	b, _ = b.Update(loaded)
	if b.path != "/root" {
		t.Fatalf("path = %q, want /root", b.path)
	}
	// Items: ".." + sub/ + a.txt (three, since /root != /).
	if got := len(b.list.Items()); got != 3 {
		t.Fatalf("got %d items, want 3 ('..', sub, a.txt)", got)
	}

	// Cursor starts on ".." (index 0); one Down lands on sub (index 1).
	b, _ = b.Update(tea.KeyMsg{Type: tea.KeyDown})
	if it, ok := b.list.SelectedItem().(item); !ok || it.up || it.e.Name != "sub" {
		t.Fatalf("after Down, selected item = %+v, want the 'sub' directory", b.list.SelectedItem())
	}

	// Enter on the directory schedules a reload of the child path.
	entered, cmd := b.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg := runCmd(cmd)
	dl, ok := msg.(dirLoadedMsg)
	if !ok {
		t.Fatalf("Enter on a directory should return a load cmd, got %T", msg)
	}
	if dl.path != "/root/sub" {
		t.Fatalf("Enter navigated to %q, want /root/sub", dl.path)
	}
	// Entering must not itself register a selection.
	if _, _, sel := entered.Selected(); sel {
		t.Fatalf("Enter into a directory must not select it")
	}

	// Back on /root, the select key on 'sub' reports it as a recursive-source dir.
	b, _ = b.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	p, isDir, ok := b.Selected()
	if !ok {
		t.Fatalf("select key should register a selection")
	}
	if p != "/root/sub" || !isDir {
		t.Fatalf("Selected() = (%q, %v), want (/root/sub, true)", p, isDir)
	}
}

// TestBrowserRootHasNoParent verifies "/" gets no synthetic ".." entry and its
// parent stays "/".
func TestBrowserRootHasNoParent(t *testing.T) {
	f := fakeLister{"/": {{Name: "etc", IsDir: true}}}
	b := NewBrowser(f, "root")
	b.SetSize(80, 24)
	b, _ = b.Update(runCmd(b.Open("/")))
	if got := len(b.list.Items()); got != 1 {
		t.Fatalf("root should have no '..' entry; got %d items", got)
	}
	if p := parent("/"); p != "/" {
		t.Fatalf("parent(/) = %q, want /", p)
	}
	if p := parent("/a/b"); p != "/a" {
		t.Fatalf("parent(/a/b) = %q, want /a", p)
	}
}

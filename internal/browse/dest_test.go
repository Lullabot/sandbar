package browse

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestNormalizePath table-tests the custom un-escaping/quote/scheme logic.
func TestNormalizePath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "/Users/me/file.txt", "/Users/me/file.txt"},
		{"backslash space", `/Users/me/My\ Files`, "/Users/me/My Files"},
		{"multiple escapes", `/a\ b\ c`, "/a b c"},
		{"escaped parens", `/tmp/\(x\)`, "/tmp/(x)"},
		{"double quoted", `"/a b/c"`, "/a b/c"},
		{"single quoted", `'/a b/c'`, "/a b/c"},
		{"file scheme", "file:///Users/me/x", "/Users/me/x"},
		{"file localhost scheme", "file://localhost/Users/me/x", "/Users/me/x"},
		{"surrounding whitespace", "  /a/b  ", "/a/b"},
		{"keeps alnum backslash", `/a\b`, `/a\b`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizePath(tc.in); got != tc.want {
				t.Fatalf("NormalizePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestDestInputNormalizesPaste asserts a PasteMsg with an escaped path yields
// a clean Value (the drag-drop convenience path).
func TestDestInputNormalizesPaste(t *testing.T) {
	d, _ := NewDestInput("Destination dir: ", "/home/u", nil)
	d, _ = d.Update(tea.PasteMsg{Content: `/Users/me/My\ Files`})
	if got := d.Value(); got != "/Users/me/My Files" {
		t.Fatalf("pasted value = %q, want %q", got, "/Users/me/My Files")
	}
}

func TestSplitDest(t *testing.T) {
	cases := []struct{ in, dir, prefix string }{
		{"/a/b/c", "/a/b", "c"},
		{"/a/b/", "/a/b", ""},
		{"/x", "/", "x"},
		{"/", "/", ""},
		{"", "", ""},
		{"rel", "", "rel"},
	}
	for _, c := range cases {
		if d, p := splitDest(c.in); d != c.dir || p != c.prefix {
			t.Fatalf("splitDest(%q) = (%q,%q), want (%q,%q)", c.in, d, p, c.dir, c.prefix)
		}
	}
}

// Directory autocomplete: only directories are suggested (files excluded), the
// trailing component prefix-filters them, and ↓+enter fills in the highlighted
// directory (drilling one level deeper).
func TestDestAutocomplete(t *testing.T) {
	f := fakeLister{"/root": {
		{Name: "beta", IsDir: true},
		{Name: "alpha", IsDir: true},
		{Name: "readme.txt"}, // a file — must be excluded from suggestions
		{Name: "alp2", IsDir: true},
	}}
	d, cmd := NewDestInput("dest: ", "/root/", f)
	d, _ = d.Update(runCmd(cmd)) // apply the initial listing

	if len(d.matches) != 3 { // three dirs; the file is excluded
		t.Fatalf("want 3 dir suggestions, got %v", d.matches)
	}
	if d.matches[0] != "alp2" || d.matches[2] != "beta" {
		t.Fatalf("suggestions should be sorted, got %v", d.matches)
	}

	// Type "al": prefix filter → alp2, alpha.
	for _, r := range "al" {
		d, _ = d.Update(runeKey(r))
	}
	if len(d.matches) != 2 || d.matches[0] != "alp2" {
		t.Fatalf("prefix 'al' should match alp2, alpha; got %v", d.matches)
	}

	// ↓ highlights the first match; enter fills it in (drilling one level deeper).
	d, _ = d.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if d.cursor != 0 {
		t.Fatalf("down should highlight the first match, cursor=%d", d.cursor)
	}
	d, _ = d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if d.Value() != "/root/alp2/" {
		t.Fatalf("enter should fill the highlighted dir, value=%q want /root/alp2/", d.Value())
	}
}

// A message that does not change the input value (a cursor-blink tick, arrow-left,
// …) must NOT reset the highlighted suggestion. Regression: refiltering on every
// blink stole the selection.
func TestDestSelectionSurvivesNonEdits(t *testing.T) {
	f := fakeLister{"/root": {{Name: "alpha", IsDir: true}, {Name: "beta", IsDir: true}}}
	d, cmd := NewDestInput("dest: ", "/root/", f)
	d, _ = d.Update(runCmd(cmd))
	d, _ = d.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // highlight match 0
	if d.cursor != 0 {
		t.Fatalf("precondition: down should highlight match 0, cursor=%d", d.cursor)
	}
	// KeyLeft moves the textinput cursor without changing the value — the same
	// fall-through path a blink tick takes; the selection must survive.
	d, _ = d.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if d.cursor != 0 {
		t.Fatalf("a value-preserving message reset the selection, cursor=%d want 0", d.cursor)
	}
}

package browse

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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

// TestDestInputNormalizesPaste asserts a Paste KeyMsg with an escaped path yields
// a clean Value (the drag-drop convenience path).
func TestDestInputNormalizesPaste(t *testing.T) {
	d := NewDestInput("Destination dir: ", "/home/u")
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Paste: true, Runes: []rune(`/Users/me/My\ Files`)})
	if got := d.Value(); got != "/Users/me/My Files" {
		t.Fatalf("pasted value = %q, want %q", got, "/Users/me/My Files")
	}
}

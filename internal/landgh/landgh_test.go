package landgh

import "testing"

// TestNewWiresRealImplementations is a thin sanity check that New() returns a
// usable Client with all seams populated — not a behavioral test (that would
// require a real gh binary and browser, which no test in this package may
// assume; see the package doc and AGENTS.md).
func TestNewWiresRealImplementations(t *testing.T) {
	c := New()
	if c.run == nil {
		t.Fatal("New().run is nil")
	}
	if c.open == nil {
		t.Fatal("New().open is nil")
	}
	if c.lookPath == nil {
		t.Fatal("New().lookPath is nil")
	}
}

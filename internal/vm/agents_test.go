package vm

import "testing"

func TestAgentSelectionDoesNotChangeBase(t *testing.T) {
	c := DefaultCreateConfig()
	key := c.ToolsetKey()
	c.WithClaude = false
	c.WithCodex = true
	if c.ToolsetKey() != key {
		t.Fatal("agent selection changed base key")
	}
	c.ApplyToolset(map[string]bool{"go": true})
	if c.WithClaude || !c.WithCodex {
		t.Fatal("base defaults overwrote agent selection")
	}
}

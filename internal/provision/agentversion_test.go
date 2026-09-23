package provision

import "testing"

func TestLegacyAgentsLeaveBaseStampWithoutLosingDependencies(t *testing.T) {
	have := "v2:old:claude+codex+ddev+go"
	want := "v3:new:java"
	if got := mergeToolsetVersion(want, have); got != "v3:new:ddev+go+java" {
		t.Fatalf("migrated union %q", got)
	}
	if lost := shrunkTools(have, "ddev+go"); len(lost) != 0 {
		t.Fatalf("agent migration warned about removed base tools: %v", lost)
	}
}

package vm

import "testing"

func TestStartAtBootRoundTrip(t *testing.T) {
	for _, mode := range StartAtBootModes {
		// Start from the opposite corner so a mode that forgets to clear a
		// field is caught.
		c := CreateConfig{DisableStartAtBoot: true, BootFollowsPower: true}
		if err := c.SetStartAtBoot(mode); err != nil {
			t.Fatal(err)
		}
		if got := c.StartAtBoot(); got != mode {
			t.Errorf("SetStartAtBoot(%q) reads back as %q", mode, got)
		}
	}
}

// Records written before the modes existed carry only DisableStartAtBoot, and
// must keep meaning what they meant.
func TestStartAtBootLegacyRecords(t *testing.T) {
	if got := (CreateConfig{}).StartAtBoot(); got != StartAtBootAlways {
		t.Errorf("zero config = %q, want always", got)
	}
	if got := (CreateConfig{DisableStartAtBoot: true}).StartAtBoot(); got != StartAtBootNever {
		t.Errorf("disabled config = %q, want never", got)
	}
	if got := DefaultCreateConfig().StartAtBoot(); got != StartAtBootAlways {
		t.Errorf("default config = %q, want always", got)
	}
}

func TestSetStartAtBootRejectsUnknownMode(t *testing.T) {
	c := CreateConfig{}
	if err := c.SetStartAtBoot("sometimes"); err == nil {
		t.Fatal("accepted an unknown mode")
	}
	if c.StartAtBoot() != StartAtBootAlways {
		t.Error("a rejected mode changed the config")
	}
}

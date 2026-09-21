package pve

import (
	"strings"
	"testing"
)

// TestValidateVMName pins ValidateVMName to PVE's `dns-name` format — the type
// its API declares for a VM's name. The two cases that matter most are the ones
// where it DISAGREES with Lima: `test_vm` is refused here and accepted there,
// `a--b` the other way round. Anything that collapses the two rules into one
// "sensible" check breaks one backend or the other.
func TestValidateVMName(t *testing.T) {
	valid := []string{
		"web",
		"dev-box",
		"dev.box.example",
		"a--b", // PVE allows a doubled hyphen inside a label; Lima does not
		"1vm",
		"a",
		"Test-VM",
		strings.Repeat("a", MaxVMNameLen),
	}
	for _, name := range valid {
		if err := ValidateVMName(name); err != nil {
			t.Errorf("ValidateVMName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{
		"",
		"   ",
		"test_vm", // the reported case: PVE answers "value does not look like a valid DNS name"
		"-vm",
		"vm-",
		".vm",
		"vm.",
		"a..b",
		"my vm",
		"naïve",
		strings.Repeat("a", MaxVMNameLen+1),
	}
	for _, name := range invalid {
		if err := ValidateVMName(name); err == nil {
			t.Errorf("ValidateVMName(%q) = nil, want an error", name)
		}
	}
}

// TestValidateVMNameExplainsTheUnderscore guards the sentence a user actually
// reads. PVE's own rejection — `400 Parameter verification failed. (name:
// invalid format - value does not look like a valid DNS name)` — arrives from
// inside a clone task and never says which character was the problem.
func TestValidateVMNameExplainsTheUnderscore(t *testing.T) {
	err := ValidateVMName("test_vm")
	if err == nil {
		t.Fatal("expected an error for an underscore")
	}
	for _, want := range []string{"test_vm", "'_'", "DNS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

package lima

import (
	"strings"
	"testing"
)

// TestValidateInstanceName pins ValidateInstanceName to limactl's own answers.
// Every case below was MEASURED against limactl 2.2.0 (`limactl create
// --name=<n> -` against a stub template: a name it accepts gets as far as
// complaining about the template, one it refuses never does) rather than read
// off a documentation page — the rule is documented nowhere, and the accepted
// set is surprising in both directions: `a_b` is fine, `a--b` is not.
func TestValidateInstanceName(t *testing.T) {
	valid := []string{
		"web",
		"dev-box",
		"dev.box",
		"test_vm",                               // limactl takes an underscore; Proxmox does not (see pve.ValidateVMName)
		"1vm",                                   // a leading digit is fine — this is not a hostname rule
		"a",                                     // one character, no separator to place
		"Test-VM",                               // case is neither folded nor refused
		"a-b_c.d-e",                             // separators may be mixed, so long as each stands alone
		strings.Repeat("a", MaxInstanceNameLen), /* the length boundary itself */
	}
	for _, name := range valid {
		if err := ValidateInstanceName(name); err != nil {
			t.Errorf("ValidateInstanceName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{
		"",
		"   ",
		"test--vm",  // doubled separator
		"-vm",       // leading separator
		"vm-",       // trailing separator
		".vm",       // …of any kind
		"my vm",     // space
		"vm/../etc", // a path is not a name
		"vm:1",      // nor a transport endpoint
		"naïve",     // non-ASCII
		strings.Repeat("a", MaxInstanceNameLen+1), // one past the boundary
	}
	for _, name := range invalid {
		if err := ValidateInstanceName(name); err == nil {
			t.Errorf("ValidateInstanceName(%q) = nil, want an error", name)
		}
	}
}

// TestValidateInstanceNameNamesTheOffendingCharacter is about the error TEXT,
// which is most of the product here: the check exists so a user can fix the
// field they are looking at, and "must match ^[A-Za-z0-9]+(?:[._-][A-Za-z0-9]+)*$"
// — what limactl itself says — is not an answer to hand someone mid-create.
func TestValidateInstanceNameNamesTheOffendingCharacter(t *testing.T) {
	err := ValidateInstanceName("my vm")
	if err == nil {
		t.Fatal("expected an error for a name containing a space")
	}
	if !strings.Contains(err.Error(), "my vm") {
		t.Errorf("error must quote the name the user typed, got %q", err)
	}
	if !strings.Contains(err.Error(), "' '") {
		t.Errorf("error must name the offending character, got %q", err)
	}
}

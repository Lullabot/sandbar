package provider

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provision"
)

// inertRunner is a lima.Runner that does nothing and is never expected to be
// called: ValidateName is a pure string check, so a provider built over this
// runner proves by construction that no limactl subprocess is involved.
type inertRunner struct{}

func (inertRunner) Output(context.Context, ...string) ([]byte, error)                { return nil, nil }
func (inertRunner) Stream(context.Context, io.Reader, io.Writer, ...string) error    { return nil }
func (inertRunner) StreamOut(context.Context, io.Reader, io.Writer, ...string) error { return nil }

// TestValidateNameIsPerBackend is the load-bearing half of Provider.ValidateName:
// the two backends must be allowed to DISAGREE.
//
// `test_vm` is the reported case — Proxmox answers "value does not look like a
// valid DNS name" and Lima creates the VM quite happily — and `a--b` runs the
// other way. Any future "tidy-up" that hoists one rule into a shared helper (or
// into vm.CreateConfig.Validate, which is per-value and knows nothing about
// where the VM is going) makes one of these two assertions fail, which is the
// point of asserting both here rather than only in the backend packages.
func TestValidateNameIsPerBackend(t *testing.T) {
	core := lima.New(inertRunner{})
	limaProv := NewLocalLima(core, &provision.Provisioner{Lima: core})

	m := newPVEMock(t)
	pveProv := newProxmoxForTest(t, m)

	cases := []struct {
		name      string
		limaOK    bool
		proxmoxOK bool
	}{
		{"dev-box", true, true},
		{"test_vm", true, false}, // Lima's identifier rule admits '_'; PVE's dns-name does not
		{"a--b", false, true},    // and PVE admits a doubled hyphen that Lima refuses
		{"-vm", false, false},    // both refuse a leading separator
		{"my vm", false, false},  // …and a space
		{"", false, false},       // …and nothing at all
	}
	for _, tc := range cases {
		if err := limaProv.ValidateName(tc.name); (err == nil) != tc.limaOK {
			t.Errorf("lima ValidateName(%q) = %v; want ok=%v", tc.name, err, tc.limaOK)
		}
		if err := pveProv.ValidateName(tc.name); (err == nil) != tc.proxmoxOK {
			t.Errorf("proxmox ValidateName(%q) = %v; want ok=%v", tc.name, err, tc.proxmoxOK)
		}
	}
}

// TestValidateNameMakesNoCalls pins that the check is free. It runs on the
// submit path of a form the user is still editing, so it must not reach the
// Proxmox API (a round trip to an unreachable endpoint would hang the TUI on a
// keystroke) — reachability is Preflight's job, not this one's.
func TestValidateNameMakesNoCalls(t *testing.T) {
	m := newPVEMock(t)
	p := newProxmoxForTest(t, m)

	if err := p.ValidateName("test_vm"); err == nil {
		t.Fatal("ValidateName(test_vm) = nil; want a rejection")
	}
	if got := m.seen(); len(got) != 0 {
		t.Errorf("ValidateName made %d API request(s): %v; want none", len(got), got)
	}
}

// TestValidateNameErrorIsAttributed keeps the backend's name in the sentence.
// The error is rendered under the create form's Name field, where "Proxmox
// requires a DNS name" is the difference between a user retyping the field and
// a user wondering why sand has opinions about underscores. Asserted
// case-insensitively because the attribution belongs in the SENTENCE, not in a
// "proxmox: " prefix — the prefix would spend a line of a budgeted help area
// saying what the sentence already says.
func TestValidateNameErrorIsAttributed(t *testing.T) {
	m := newPVEMock(t)
	p := newProxmoxForTest(t, m)

	err := p.ValidateName("test_vm")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "proxmox") {
		t.Errorf("error %q does not say which backend refused the name", err)
	}
}

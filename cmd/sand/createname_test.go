package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/providerfake"
)

// TestCheckBackendNameRefusesANewVM is the CLI half of the create form's name
// check: `sand create --name test_vm` against a Proxmox profile must fail on
// stderr, immediately, rather than minutes later inside a clone task.
func TestCheckBackendNameRefusesANewVM(t *testing.T) {
	var asked []string
	p := &providerfake.Provider{
		ValidateNameFunc: func(name string) error {
			asked = append(asked, name)
			return errors.New("proxmox: name " + name + " cannot contain '_'")
		},
	}

	err := checkBackendName(p, "test_vm", false)
	if err == nil {
		t.Fatal("checkBackendName accepted a name the backend refuses")
	}
	if !strings.Contains(err.Error(), "test_vm") {
		t.Errorf("error = %q; it must quote the name that was passed", err)
	}
	if len(asked) != 1 {
		t.Errorf("backend was asked %d times; want exactly 1", len(asked))
	}
}

// TestCheckBackendNameSkipsRecreate pins the exemption. --recreate rebuilds a VM
// that ALREADY EXISTS, whose name the backend accepted when it was made;
// applying today's rule to it would leave a VM that predates the rule with no
// way to be rebuilt. The backend must not even be asked.
func TestCheckBackendNameSkipsRecreate(t *testing.T) {
	asked := false
	p := &providerfake.Provider{
		ValidateNameFunc: func(string) error {
			asked = true
			return errors.New("refused")
		},
	}

	if err := checkBackendName(p, "legacy_box", true); err != nil {
		t.Fatalf("--recreate of an existing VM was refused: %v", err)
	}
	if asked {
		t.Error("--recreate consulted the naming rule; an existing VM's name is not up for review")
	}
}

// TestCheckBackendNameAcceptsAGoodName is the negative control: without it, a
// checkBackendName that refused everything would pass the test above.
func TestCheckBackendNameAcceptsAGoodName(t *testing.T) {
	p := &providerfake.Provider{
		ValidateNameFunc: func(name string) error {
			if strings.Contains(name, "_") {
				return errors.New("refused")
			}
			return nil
		},
	}
	if err := checkBackendName(p, "dev-box", false); err != nil {
		t.Fatalf("checkBackendName(dev-box) = %v, want nil", err)
	}
}

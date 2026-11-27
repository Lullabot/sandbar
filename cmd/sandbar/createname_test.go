package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/pve"
	"github.com/lullabot/sandbar/internal/vm"
)

// TestCheckBackendNameRefusesANewVM is the CLI half of the create form's name
// check: `sandbar create --name test_vm` against a Proxmox profile must fail on
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

// TestCheckBackendBaseNameRefusesAPathName pins the check on a typed
// --base-name. The base name becomes a directory under the Lima home, and
// `--base-name ..` once reached a failed base build whose cleanup removed the
// Lima home's parent: the user's home directory. It must be refused before
// anything is built, by the same rule the backend applies to --name.
func TestCheckBackendBaseNameRefusesAPathName(t *testing.T) {
	p := &providerfake.Provider{ValidateNameFunc: lima.ValidateInstanceName}
	for _, base := range []string{"..", ".", "../../x", "/home/someone", "a/b", ""} {
		err := checkBackendBaseName(p, base, true)
		if err == nil {
			t.Errorf("--base-name %q was accepted", base)
			continue
		}
		if !strings.Contains(err.Error(), "base image name") {
			t.Errorf("--base-name %q: error %q does not say it is about the base image name", base, err)
		}
	}
}

// TestCheckBackendBaseNameLeavesSuppliedNamesAlone: a base name sandbar supplies
// itself (the default, a template instance, or one recorded for a VM being
// recreated) is not re-judged. The backend is not even asked.
func TestCheckBackendBaseNameLeavesSuppliedNamesAlone(t *testing.T) {
	asked := false
	p := &providerfake.Provider{ValidateNameFunc: func(string) error {
		asked = true
		return errors.New("refused")
	}}
	if err := checkBackendBaseName(p, "legacy_base", false); err != nil {
		t.Fatalf("a base name the user did not type was refused: %v", err)
	}
	if asked {
		t.Error("a supplied base name was put to the naming rule")
	}
}

// TestSandbarOwnBaseNamesPassBothBackends is the negative control for the check
// above: the names sandbar itself gives a base or a template must pass both
// backends' rules, or `--base-name sandbar-base` would refuse the default.
func TestSandbarOwnBaseNamesPassBothBackends(t *testing.T) {
	names := []string{vm.DefaultCreateConfig().BaseName, vm.TemplateInstanceName("My Golden Template")}
	for _, name := range names {
		if err := lima.ValidateInstanceName(name); err != nil {
			t.Errorf("Lima rejects sandbar's own base name %q: %v", name, err)
		}
		if err := pve.ValidateVMName(name); err != nil {
			t.Errorf("Proxmox rejects sandbar's own base name %q: %v", name, err)
		}
		if err := checkBackendBaseName(&providerfake.Provider{ValidateNameFunc: lima.ValidateInstanceName}, name, true); err != nil {
			t.Errorf("--base-name %s was refused: %v", name, err)
		}
	}
}

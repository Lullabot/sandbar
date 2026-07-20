package provision

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
)

// templateListJSON renders the one canned `limactl list <name> --format json`
// line lima.Client.Get needs to see an instance as present. Get (unlike
// Status) parses this as JSON rather than reading raw status text, so a
// template-sourced create/reset's existence check needs its own canned
// output distinct from the plain "Stopped\n"/"Running\n" the package's other
// tests feed Status.
func templateListJSON(name, status string) []byte {
	return []byte(`{"name":"` + name + `","status":"` + status + `"}` + "\n")
}

// TestCreateVMWithOptions_TemplateSource_ClonesFromTemplateSkipsBase is the
// key create-side assertion: a template-sourced create clones straight from
// the template instance and never touches the shared base image at all — no
// base status check, no base build, no base provisioning run.
func TestCreateVMWithOptions_TemplateSource_ClonesFromTemplateSkipsBase(t *testing.T) {
	const tmplInstance = "sandbar-tmpl-golden"
	f := &fakeRunner{status: map[string][]byte{tmplInstance: templateListJSON(tmplInstance, "Stopped")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig() // Name=claude, BaseName=sandbar-base
	opts := CreateOptions{TemplateSource: tmplInstance}

	if err := p.CreateVMWithOptions(context.Background(), cfg, opts, io.Discard); err != nil {
		t.Fatalf("CreateVMWithOptions: %v", err)
	}

	want := [][]string{
		{"list", "claude", "--format", "{{.Status}}"}, // exists-guard: target absent
		{"list", tmplInstance, "--format", "json"},    // template existence check (Get)
		{"clone", tmplInstance, "claude"},             // clone FROM THE TEMPLATE, not the base
		{"edit", "--set", `.cpus=4 | .memory="8GiB" | .disk="100GiB" | .mounts |= map(select(.writable != true))`, "claude"}, // Configure clone sizes
		{"start", "claude"},
		{"shell", "claude", "sudo", "bash", "-c", inGuestScript},      // finalize provision
		{"shell", "claude", "test", "-e", "/var/run/reboot-required"}, // needsReboot check
		{"stop", "claude"},  // bounce
		{"start", "claude"}, // bounce
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("call sequence mismatch:\n got %v\nwant %v", f.calls, want)
	}

	// No base-image machinery may run: no CreateStreaming ("start --name ..."),
	// no base status/provisioning call naming cfg.BaseName at all.
	for _, c := range f.calls {
		if len(c) >= 2 && c[0] == "start" && c[1] == "--name" {
			t.Errorf("base image was built from scratch; unexpected call %v", c)
		}
		if len(c) >= 2 && c[1] == cfg.BaseName {
			t.Errorf("unexpected call touching the base image %q: %v", cfg.BaseName, c)
		}
	}

	if len(f.streams) != 1 {
		t.Fatalf("got %d streamed stdins, want 1 (finalize only)", len(f.streams))
	}
	if !strings.Contains(f.streams[0], "provision_phase: finalize") {
		t.Errorf("finalize stdin missing provision_phase: finalize:\n%s", f.streams[0])
	}
}

// TestCreateVMWithOptions_TemplateSource_MissingTemplateFails proves a
// template that does not exist on the host fails fast with an error naming
// it, instead of silently falling back to the base image.
func TestCreateVMWithOptions_TemplateSource_MissingTemplateFails(t *testing.T) {
	const tmplInstance = "sandbar-tmpl-ghost"
	f := &fakeRunner{} // no canned status for tmplInstance: Get sees it as absent
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig()
	opts := CreateOptions{TemplateSource: tmplInstance}

	err := p.CreateVMWithOptions(context.Background(), cfg, opts, io.Discard)
	if err == nil {
		t.Fatal("expected an error for a non-existent template")
	}
	if !strings.Contains(err.Error(), tmplInstance) {
		t.Errorf("error %q does not name the missing template %q", err.Error(), tmplInstance)
	}

	// Nothing was cloned or built: the failure must be fail-fast, not a
	// fallback to the base image.
	if callIndex(f.snapshot(), "clone") != -1 {
		t.Errorf("expected no clone call at all; got %v", f.snapshot())
	}
}

// TestReset_TemplateProvenance_ClonesFromTemplateInstance proves that
// resetting a VM whose stored config carries template provenance re-clones
// from the template instance (recorded in cfg.BaseName by whatever created
// it from a template — task 1) rather than the shared base image, and skips
// base machinery exactly like the create path.
func TestReset_TemplateProvenance_ClonesFromTemplateInstance(t *testing.T) {
	const tmplInstance = "sandbar-tmpl-golden"
	f := &fakeRunner{status: map[string][]byte{tmplInstance: templateListJSON(tmplInstance, "Stopped")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig()
	cfg.BaseName = tmplInstance // task 1: template-sourced VM's BaseName is the template's instance name

	resetOpts := ResetOptions{TemplateSource: "golden"} // provenance signal; the name itself is not consulted
	if err := p.Reset(context.Background(), cfg, resetOpts, io.Discard); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	calls := f.snapshot()
	if callIndex(calls, "delete", "claude", "-f") == -1 {
		t.Fatalf("expected the old instance to be deleted; got %v", calls)
	}
	if callIndex(calls, "clone", tmplInstance, "claude") == -1 {
		t.Fatalf("expected a clone FROM THE TEMPLATE instance %q; got %v", tmplInstance, calls)
	}
	// No base-image machinery: no CreateStreaming, no status/provision call
	// against the real default base name.
	for _, c := range calls {
		if len(c) >= 2 && c[0] == "start" && c[1] == "--name" {
			t.Errorf("base image was built from scratch during a template reset; unexpected call %v", c)
		}
	}
	if idx := callIndex(calls, "list", "sandbar-base"); idx != -1 {
		t.Errorf("unexpected call touching the default base image; got %v at %d", calls, idx)
	}
}

// TestReset_TemplateSource_MissingTemplateFails mirrors the create-side
// missing-template assertion for Reset: a template recorded as this VM's
// provenance but no longer present on the host must fail fast, naming it.
func TestReset_TemplateSource_MissingTemplateFails(t *testing.T) {
	const tmplInstance = "sandbar-tmpl-ghost"
	f := &fakeRunner{} // no canned status for tmplInstance: Get sees it as absent
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig()
	cfg.BaseName = tmplInstance

	err := p.Reset(context.Background(), cfg, ResetOptions{TemplateSource: "ghost"}, io.Discard)
	if err == nil {
		t.Fatal("expected an error for a non-existent template")
	}
	if !strings.Contains(err.Error(), tmplInstance) {
		t.Errorf("error %q does not name the missing template %q", err.Error(), tmplInstance)
	}
}

// TestReset_NormalVM_Unchanged is a narrow regression check that a
// non-template reset still clones from the base image exactly as before —
// the "byte-for-byte unchanged" half of the acceptance criteria, scoped to
// the one call that matters (the clone source), since the full sequence is
// already covered by the package's existing reset tests.
func TestReset_NormalVM_Unchanged(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig() // BaseName=sandbar-base
	if err := p.Reset(context.Background(), cfg, ResetOptions{}, io.Discard); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	calls := f.snapshot()
	if callIndex(calls, "clone", "sandbar-base", "claude") == -1 {
		t.Fatalf("expected a normal reset to clone from the base image; got %v", calls)
	}
}

//go:build limae2e

// template_e2e_test.go proves plan 17's golden-VM-template round-trip against
// REAL Lima instances: snapshot a running source VM into a template, create a
// second VM straight from that template, and confirm the guest state carried
// over with a FRESH per-VM identity — hostname and git config, both set by
// finalize's own Ansible pass regardless of clone source (roles/base's "Set
// hostname" task carries no `when:` gate at all, and roles/user's ~/.gitconfig
// template is gated only against the BASE phase, so both run — with THIS
// VM's own values — on every clone, template-sourced or not). Then the
// template is deleted and its instance confirmed gone.
//
// Gated behind the `limae2e` build tag and LIMA_E2E, exactly like this
// package's other real-Lima test (lima_e2e_test.go) — a plain `go test ./...`
// never reaches this file, and even a `-tags limae2e` run skips cleanly with
// LIMA_E2E unset (this sandbox has no nested virtualization to boot a real
// VM with, so it must never attempt one).
//
// Run (needs limactl + nested virt/KVM; downloads the Debian 13 image once,
// then builds a full base image — this is comparatively slow, matching
// cmd/sand/create_e2e_test.go's own shared-base build):
//
//	LIMA_E2E=1 go test -tags limae2e -timeout 30m -run TestTemplateRoundTrip ./internal/provision/
package provision

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/vm"
)

// TestTemplateRoundTrip is this task's one cohesive scenario, kept to a single
// round-trip per the plan's Remote-Parity risk mitigation (creates are
// expensive; breadth is covered by tasks 1-3's unit tests): create a source
// VM, seed guest-state markers in it, snapshot it into a golden template,
// create a second VM from that template, confirm the markers carried over
// with a fresh per-VM identity, then delete the template and confirm cleanup.
func TestTemplateRoundTrip(t *testing.T) {
	if os.Getenv("LIMA_E2E") == "" {
		t.Skip("set LIMA_E2E=1 (and -tags limae2e) to run the real-Lima e2e tests")
	}

	playbookDir, err := LocatePlaybook()
	if err != nil {
		t.Skipf("could not locate the embedded playbook (%v); skipping — this environment cannot run a real base build", err)
	}

	cli := lima.New(lima.NewExecRunner())
	prov := &Provisioner{Lima: cli, PlaybookDir: playbookDir}

	const (
		baseName   = "sand-tmpl-e2e-base"
		sourceName = "sand-tmpl-e2e-source"
		cloneName  = "sand-tmpl-e2e-clone"
	)
	templateInstance := vm.TemplateInstanceName("golden-e2e")

	// Clean slate + unconditional teardown of every instance this test can
	// create, mirroring every other e2e test in this repo — a prior
	// interrupted run must not confuse this one, and a failure here must not
	// strand instances behind it.
	cleanup := func() {
		_ = cli.Delete(cloneName, true)
		_ = cli.Delete(templateInstance, true)
		_ = cli.Delete(sourceName, true)
		_ = cli.Delete(baseName, true)
	}
	cleanup()
	t.Cleanup(cleanup)

	baseCfg := vm.CreateConfig{
		BaseName: baseName,
		User:     vm.HostUser(),
		CPUs:     2,
		Memory:   "2GiB",
		Disk:     vm.BaseDiskFloor,
		Domain:   "lan",
		Locale:   "en_US.UTF-8",
		// Every optional tool-set flag left at its zero value (false): this
		// test exercises the template mechanics, never the base's installed
		// tooling — see cmd/sand/create_e2e_test.go's ensureCmdE2EBase for the
		// same reasoning.
	}
	var buildLog bytes.Buffer
	if err := prov.BuildBase(context.Background(), baseCfg, &buildLog); err != nil {
		t.Fatalf("build base: %v\n%s", err, buildLog.String())
	}

	ctx := context.Background()

	// --- create the source VM, a plain clone of the base -------------------
	sourceCfg := baseCfg
	sourceCfg.Name, sourceCfg.GitName, sourceCfg.GitEmail =
		sourceName, "Sand Template E2E Source", "sand-tmpl-e2e-source@example.com"

	var createLog bytes.Buffer
	if err := prov.CreateVMWithOptions(ctx, sourceCfg, CreateOptions{}, &createLog); err != nil {
		t.Fatalf("create source VM: %v\n%s", err, createLog.String())
	}

	// --- seed a guest marker: a file and a directory, plus a tiny sqlite3 DB
	// if (and only if) the base image happens to carry sqlite3 -------------
	guestOut(t, cli, sourceName, "sh", "-c",
		"set -e; echo golden > ~/marker.txt; mkdir -p ~/markerdir; echo golden-dir > ~/markerdir/marker2.txt; "+
			`if command -v sqlite3 >/dev/null 2>&1; then sqlite3 ~/seed.db 'create table t(x); insert into t values(1);'; fi`)
	hasSQLite := guestOut(t, cli, sourceName, "sh", "-c", "command -v sqlite3 >/dev/null 2>&1 && echo yes || echo no") == "yes"

	sourceHostname := guestOut(t, cli, sourceName, "hostname")
	sourceGitName := guestOut(t, cli, sourceName, "git", "config", "--get", "user.name")
	if sourceHostname != sourceName {
		t.Fatalf("source hostname = %q, want %q (EffectiveHostname defaults to the VM name)", sourceHostname, sourceName)
	}

	// --- snapshot: source must end back in the power state it started in
	// (Running — it is a VM the "user" is actively working in), and the
	// template instance must be a stopped clone ----------------------------
	if status, err := cli.Status(sourceName); err != nil || status != "Running" {
		t.Fatalf("source status before snapshot = (%q, %v), want (Running, nil)", status, err)
	}

	var snapLog bytes.Buffer
	if _, err := prov.SnapshotTemplate(ctx, sourceName, templateInstance, &snapLog); err != nil {
		t.Fatalf("SnapshotTemplate: %v\n%s", err, snapLog.String())
	}

	if status, err := cli.Status(sourceName); err != nil || status != "Running" {
		t.Fatalf("source status after snapshot = (%q, %v), want (Running, nil) — snapshot must restore the prior power state", status, err)
	}
	if status, err := cli.Status(templateInstance); err != nil || status != "Stopped" {
		t.Fatalf("template instance status = (%q, %v), want (Stopped, nil)", status, err)
	}

	// --- create VM2 straight from the template ------------------------------
	cloneCfg := baseCfg
	cloneCfg.Name, cloneCfg.BaseName = cloneName, templateInstance
	cloneCfg.GitName, cloneCfg.GitEmail = "Sand Template E2E Clone", "sand-tmpl-e2e-clone@example.com"

	var cloneLog bytes.Buffer
	if err := prov.CreateVMWithOptions(ctx, cloneCfg, CreateOptions{TemplateSource: templateInstance}, &cloneLog); err != nil {
		t.Fatalf("create VM2 from template: %v\n%s", err, cloneLog.String())
	}

	// The marker must have carried over from the template.
	if got := guestOut(t, cli, cloneName, "sh", "-c", "cat ~/marker.txt"); got != "golden" {
		t.Fatalf("clone marker.txt = %q, want %q", got, "golden")
	}
	if got := guestOut(t, cli, cloneName, "sh", "-c", "cat ~/markerdir/marker2.txt"); got != "golden-dir" {
		t.Fatalf("clone markerdir/marker2.txt = %q, want %q", got, "golden-dir")
	}
	if hasSQLite {
		if got := guestOut(t, cli, cloneName, "sh", "-c", "sqlite3 ~/seed.db 'select x from t;'"); got != "1" {
			t.Fatalf("clone seed.db content = %q, want %q", got, "1")
		}
	}

	// Hostname and git identity must be the NEW VM's own — never the
	// source's — proving finalize re-ran its identity tasks on the clone
	// rather than the template's baked-in values leaking through.
	cloneHostname := guestOut(t, cli, cloneName, "hostname")
	if cloneHostname == sourceHostname {
		t.Fatalf("clone hostname %q must differ from source hostname %q", cloneHostname, sourceHostname)
	}
	if cloneHostname != cloneName {
		t.Fatalf("clone hostname = %q, want %q (its own EffectiveHostname)", cloneHostname, cloneName)
	}
	cloneGitName := guestOut(t, cli, cloneName, "git", "config", "--get", "user.name")
	if cloneGitName == sourceGitName {
		t.Fatalf("clone git user.name %q must differ from source's %q", cloneGitName, sourceGitName)
	}
	if cloneGitName != cloneCfg.GitName {
		t.Fatalf("clone git user.name = %q, want %q", cloneGitName, cloneCfg.GitName)
	}

	// --- delete the template, confirm the instance (and its disk) is gone --
	var delLog bytes.Buffer
	if err := prov.DeleteTemplate(ctx, templateInstance, &delLog); err != nil {
		t.Fatalf("DeleteTemplate: %v\n%s", err, delLog.String())
	}
	if _, err := cli.Get(templateInstance); !errors.Is(err, lima.ErrNoSuchInstance) {
		t.Fatalf("Get(%q) after DeleteTemplate = %v, want lima.ErrNoSuchInstance", templateInstance, err)
	}
	dir := filepath.Join(lima.LocalFiles().LimaHome(), templateInstance)
	if _, err := lima.LocalFiles().Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("template instance dir %q still present after DeleteTemplate (stat err=%v)", dir, err)
	}
}

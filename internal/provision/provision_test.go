package provision

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/vm"
)

// fakeRunner records the argv of every limactl call (and the stdin of every
// streamed call) and returns canned output, so the integration tests assert the
// orchestration's ordered calls without spawning a real limactl/ansible.
//
// It is mutex-guarded because the base-lock tests drive it from a goroutine while
// the test body reads the recorded sequence — an unguarded slice append racing a
// read is exactly what `go test -race` exists to catch, and a fake that races is a
// fake nobody can trust.
type fakeRunner struct {
	mu      sync.Mutex
	calls   [][]string
	streams []string          // stdin contents captured from Stream calls, in order
	status  map[string][]byte // per-instance canned `list <name> --format` output
	outputs map[string][]byte // canned output keyed by the first argv token
	err     error
	// failOn, when non-nil, makes the matching call (and only it) fail with
	// failErr. It lets a test inject a fault at one specific step — e.g. the
	// stage-out tar — while every other call still succeeds.
	failOn  func([]string) bool
	failErr error
	// hook, when non-nil, runs as a call is answered, before its result is
	// decided. It lets a test act at one precise point in the sequence — e.g.
	// cancel the create's own context the moment the base playbook starts.
	hook func([]string)
}

// callErr returns the error a given call should produce: failErr for a call the
// test singled out via failOn, otherwise the runner-wide err (usually nil).
func (f *fakeRunner) callErr(args []string) error {
	if f.failOn != nil && f.failOn(args) {
		if f.failErr != nil {
			return f.failErr
		}
		return errors.New("injected failure")
	}
	return f.err
}

// record appends one call (and any stdin it carried) to the recorded sequence.
func (f *fakeRunner) record(args []string, stdin io.Reader) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	if stdin != nil {
		data, _ := io.ReadAll(stdin)
		f.streams = append(f.streams, string(data))
	}
}

// mark writes a synthetic entry into the recorded sequence, so a test can assert
// that a real call lands on the far side of a host-side event the runner cannot
// see — notably the release of the base lock.
func (f *fakeRunner) mark(label string) {
	f.record([]string{label}, nil)
}

// snapshot copies the recorded sequence under the lock, for a test that reads it
// while a create is still running.
func (f *fakeRunner) snapshot() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.calls...)
}

func (f *fakeRunner) Output(ctx context.Context, args ...string) ([]byte, error) {
	f.record(args, nil)
	if f.hook != nil {
		f.hook(args)
	}
	// A killed limactl reports the cancellation, so the fake must too: the
	// provisioner's cancellation behaviour is only real if a cancelled ctx can
	// actually fail a call.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := f.callErr(args); err != nil {
		return nil, err
	}
	// Status queries look like `list <name> --format {{.Status}}`. Return the
	// per-instance canned status; an unset instance reads as absent (empty).
	if len(args) >= 2 && args[0] == "list" && args[1] != "--format" {
		return f.status[args[1]], f.err
	}
	// guestHome reads the home dir from stdout of `shell <name> getent passwd
	// <user>` (fields user:x:uid:gid:gecos:home:shell) via ShellOut/Output; emit
	// a canned passwd line so the staging path resolves to /home/andrew in the
	// reset tests.
	for _, a := range args {
		if a == "getent" {
			return []byte("andrew:x:1000:1000::/home/andrew:/bin/bash\n"), f.err
		}
	}
	return f.outputs[args[0]], f.err
}

func (f *fakeRunner) Stream(ctx context.Context, stdin io.Reader, out io.Writer, args ...string) error {
	f.record(args, stdin)
	if f.hook != nil {
		f.hook(args)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return f.callErr(args)
}

func (f *fakeRunner) StreamOut(ctx context.Context, stdin io.Reader, out io.Writer, args ...string) error {
	// StageOut now streams through StreamOut; mirror Stream so the reset tests'
	// recorded call sequence and staged-stdin capture are unchanged.
	return f.Stream(ctx, stdin, out, args...)
}

func testConfig() vm.CreateConfig {
	cfg := vm.DefaultCreateConfig() // Name=claude, BaseName=sandbar-base
	cfg.User = "andrew"
	cfg.GitName = "Andrew Berry"
	cfg.GitEmail = "andrew@example.com"
	cfg.CPUs = 4
	return cfg
}

// TestCreateVM_StoppedBase is the key integration test: with a pre-existing,
// already-stopped base image, CreateVM must clone -> start -> shell(finalize) ->
// stop -> start, in that exact order.
func TestCreateVM_StoppedBase(t *testing.T) {
	// Base is stopped; target "claude" is absent so the exists-guard passes.
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	if err := p.CreateVM(context.Background(), testConfig(), io.Discard); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	want := [][]string{
		{"list", "claude", "--format", "{{.Status}}"},       // exists-guard: target absent
		{"list", "sandbar-base", "--format", "{{.Status}}"}, // Status(base) -> Stopped
		{"clone", "sandbar-base", "claude"},                 // Clone
		{"edit", "--set", `.cpus=4 | .memory="8GiB" | .disk="100GiB" | .mounts |= map(select(.writable != true))`, "claude"}, // Configure clone sizes (and strip the base's writable apt-cache mount)
		{"start", "claude"}, // Start
		{"shell", "claude", "sudo", "bash", "-c", inGuestScript},      // finalize provision
		{"shell", "claude", "test", "-e", "/var/run/reboot-required"}, // needsReboot check
		{"stop", "claude"},  // bounce: stop (fakeRunner defaults to success, so needsReboot reads true)
		{"start", "claude"}, // bounce: start
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("CreateVM call sequence mismatch:\n got %v\nwant %v", f.calls, want)
	}

	// The single shell call must stream the FINALIZE vars over stdin (never argv).
	if len(f.streams) != 1 {
		t.Fatalf("got %d streamed stdins, want 1", len(f.streams))
	}
	if !strings.Contains(f.streams[0], "provision_phase: finalize") {
		t.Errorf("finalize stdin missing provision_phase: finalize:\n%s", f.streams[0])
	}
	if !strings.Contains(f.streams[0], "user_git_user_name: Andrew Berry") {
		t.Errorf("finalize stdin missing git identity:\n%s", f.streams[0])
	}
}

// seedLegacyBase writes the on-disk footprint of a base built by a pre-rename
// sand — the Lima instance dir with a lima.yaml (what migrateLegacyBase stats to
// decide whether there is anything to migrate) and its version stamp beside it —
// into an isolated LIMA_HOME.
func seedLegacyBase(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("LIMA_HOME", home)
	dir := filepath.Join(home, legacyBaseName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir legacy instance: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lima.yaml"), []byte("images: []\n"), 0o644); err != nil {
		t.Fatalf("write legacy lima.yaml: %v", err)
	}
	stamp := baseVersionPath(lima.LocalFiles(), legacyBaseName)
	if err := os.MkdirAll(filepath.Dir(stamp), 0o755); err != nil {
		t.Fatalf("mkdir _sand: %v", err)
	}
	if err := os.WriteFile(stamp, []byte("v2:deadbeef:go+java\n2026-01-01T00:00:00Z\n"), 0o644); err != nil {
		t.Fatalf("write legacy stamp: %v", err)
	}
}

// indexOfCall returns the position of the first recorded call equal to want, or
// -1 if it never happened.
func indexOfCall(calls [][]string, want []string) int {
	for i, c := range calls {
		if reflect.DeepEqual(c, want) {
			return i
		}
	}
	return -1
}

// TestMigrateLegacyBase renames a base built under the old name to the current
// default: it clones the legacy instance to the new name, carries the version
// stamp across, and deletes the old instance — in that order.
func TestMigrateLegacyBase(t *testing.T) {
	seedLegacyBase(t)
	f := &fakeRunner{status: map[string][]byte{legacyBaseName: []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	p.migrateLegacyBase(context.Background(), testConfig(), io.Discard)

	want := [][]string{
		{"list", "sandbar-base", "--format", "{{.Status}}"}, // target already present?
		{"list", "claude-base", "--format", "{{.Status}}"},  // legacy present?
		{"clone", "claude-base", "sandbar-base"},            // rename: copy to the new name
		{"delete", "claude-base", "-f"},                     // reclaim the old instance
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("migration call sequence mismatch:\n got %v\nwant %v", f.calls, want)
	}
	// The version stamp moved with it: the new base is stamped, the old is gone —
	// so the migrated base reads as current and is not re-applied on the spot.
	if _, err := os.Stat(baseVersionPath(lima.LocalFiles(), "sandbar-base")); err != nil {
		t.Errorf("new base stamp missing after migration: %v", err)
	}
	if _, err := os.Stat(baseVersionPath(lima.LocalFiles(), legacyBaseName)); !os.IsNotExist(err) {
		t.Errorf("legacy stamp still present after migration (err=%v)", err)
	}
}

// TestMigrateLegacyBase_NoLegacyInstance: with no legacy instance on disk the
// migration makes ZERO limactl calls — the host-side stat gate keeps the common
// path (a machine that never had a pre-rename base) free of any extra work on
// every single create.
func TestMigrateLegacyBase_NoLegacyInstance(t *testing.T) {
	t.Setenv("LIMA_HOME", t.TempDir())
	f := &fakeRunner{status: map[string][]byte{legacyBaseName: []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	p.migrateLegacyBase(context.Background(), testConfig(), io.Discard)

	if len(f.calls) != 0 {
		t.Fatalf("no legacy instance dir: expected no limactl calls, got %v", f.calls)
	}
}

// TestMigrateLegacyBase_CustomBaseUntouched: a create that targets a base other
// than the current default is not part of the default's rename and must not
// migrate, even when a legacy instance happens to exist.
func TestMigrateLegacyBase_CustomBaseUntouched(t *testing.T) {
	seedLegacyBase(t)
	f := &fakeRunner{status: map[string][]byte{legacyBaseName: []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
	cfg := testConfig()
	cfg.BaseName = "my-own-base"

	p.migrateLegacyBase(context.Background(), cfg, io.Discard)

	if len(f.calls) != 0 {
		t.Fatalf("custom base: expected no migration, got %v", f.calls)
	}
	if _, err := os.Stat(baseVersionPath(lima.LocalFiles(), legacyBaseName)); err != nil {
		t.Errorf("legacy stamp should be untouched for a custom base: %v", err)
	}
}

// TestMigrateLegacyBase_TargetExistsWins: if the new base already exists (a prior
// create migrated or built it) the legacy instance is left entirely alone — no
// clone, no delete — so a stray old instance can never clobber a live base.
func TestMigrateLegacyBase_TargetExistsWins(t *testing.T) {
	seedLegacyBase(t)
	f := &fakeRunner{status: map[string][]byte{
		legacyBaseName: []byte("Stopped\n"),
		"sandbar-base": []byte("Stopped\n"),
	}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	p.migrateLegacyBase(context.Background(), testConfig(), io.Discard)

	want := [][]string{{"list", "sandbar-base", "--format", "{{.Status}}"}}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("target-exists: expected only the target status check, got %v", f.calls)
	}
	if _, err := os.Stat(baseVersionPath(lima.LocalFiles(), legacyBaseName)); err != nil {
		t.Errorf("legacy stamp should be untouched when the target already exists: %v", err)
	}
}

// TestCreateVM_MigratesLegacyBaseThenClones: a full create on a machine with a
// pre-rename base renames it first (under the base lock) and then clones the
// target from the correctly-named base — it never rebuilds the base from scratch.
func TestCreateVM_MigratesLegacyBaseThenClones(t *testing.T) {
	seedLegacyBase(t)
	f := &fakeRunner{status: map[string][]byte{legacyBaseName: []byte("Stopped\n")}}
	// Renaming the legacy base makes the new base exist and stopped, the way a real
	// `limactl clone` would; without this the fake's static status would leave
	// ensureBaseStopped thinking the target is absent and rebuild it.
	f.hook = func(c []string) {
		if len(c) == 3 && c[0] == "clone" && c[1] == legacyBaseName && c[2] == "sandbar-base" {
			f.mu.Lock()
			f.status["sandbar-base"] = []byte("Stopped\n")
			f.mu.Unlock()
		}
	}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	if err := p.CreateVM(context.Background(), testConfig(), io.Discard); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	iMigrate := indexOfCall(f.calls, []string{"clone", legacyBaseName, "sandbar-base"})
	iClone := indexOfCall(f.calls, []string{"clone", "sandbar-base", "claude"})
	if iMigrate < 0 || iClone < 0 || iMigrate > iClone {
		t.Fatalf("expected the legacy rename before the target clone; got %v", f.calls)
	}
	// The base is reused, never rebuilt: a from-scratch build starts it via
	// `start --name <base> <overlay>`, which must not appear.
	for _, c := range f.calls {
		if c[0] == "start" && hasTok(c, "--name") {
			t.Fatalf("base was rebuilt from scratch after migration: %v", f.calls)
		}
	}
}

// TestCreateVM_NoBounceWithoutRebootMarker is the conditional-bounce
// acceptance criterion: a guest that does not report
// /var/run/reboot-required must not be stopped and started after finalize —
// the two stated reasons for the old unconditional bounce (the finalize apt
// upgrade, removed in task 8; and docker-group membership, granted in the
// BASE phase and baked into the image) are both gone, so a create that never
// asked the guest to reboot pays none of that latency.
func TestCreateVM_NoBounceWithoutRebootMarker(t *testing.T) {
	f := &fakeRunner{
		status: map[string][]byte{"sandbar-base": []byte("Stopped\n")},
		failOn: func(c []string) bool { // the guest has no reboot-required marker
			return len(c) > 0 && c[0] == "shell" && hasTok(c, "/var/run/reboot-required")
		},
		failErr: errors.New("exit status 1"),
	}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	if err := p.CreateVM(context.Background(), testConfig(), io.Discard); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	for _, c := range f.calls {
		if c[0] == "stop" || (c[0] == "start" && hasTok(c, "claude") && !hasTok(c, "--name")) {
			// The clone's own "start claude" (bringing the VM up before
			// finalize) is expected; only a stop, or a start AFTER one, means a
			// bounce happened. Fail on any stop outright.
			if c[0] == "stop" {
				t.Fatalf("no reboot-required marker: CreateVM must not bounce (stop/start) the VM: %v", f.calls)
			}
		}
	}
	// Belt and suspenders: the reboot-required probe ran, and it is the LAST
	// call — nothing follows it (no stop, no start).
	last := f.calls[len(f.calls)-1]
	if last[0] != "shell" || !hasTok(last, "/var/run/reboot-required") {
		t.Fatalf("expected the needsReboot probe to be the final call when no bounce is needed, got %v", f.calls)
	}
}

// TestCreateVM_BouncesWhenRebootRequired is the converse: a guest that DOES
// report /var/run/reboot-required (the fake's default: every call succeeds
// unless told otherwise) is bounced — stop immediately followed by start —
// after finalize.
func TestCreateVM_BouncesWhenRebootRequired(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	if err := p.CreateVM(context.Background(), testConfig(), io.Discard); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	probe := findCall(t, f.calls, 0, "needsReboot probe", func(c []string) bool {
		return c[0] == "shell" && hasTok(c, "/var/run/reboot-required")
	})
	if probe+2 >= len(f.calls) {
		t.Fatalf("expected a stop and a start after the needsReboot probe, got %v", f.calls)
	}
	if f.calls[probe+1][0] != "stop" {
		t.Fatalf("call after needsReboot probe = %v, want stop", f.calls[probe+1])
	}
	if f.calls[probe+2][0] != "start" {
		t.Fatalf("call after bounce stop = %v, want start", f.calls[probe+2])
	}
}

// TestDefaultGuestScriptStaysCollectionFree pins the default (SAND_PROFILE
// unset) in-guest script to never install any Ansible collection: profile_tasks
// lives in ansible.posix, which the default Lima dependency script does not
// install (strikethroo plan 13, task 02), so the default path must not depend
// on it either.
func TestDefaultGuestScriptStaysCollectionFree(t *testing.T) {
	if strings.Contains(inGuestScript, "ansible-galaxy") || strings.Contains(inGuestScript, "collection") {
		t.Errorf("default inGuestScript must stay collection-free:\n%s", inGuestScript)
	}
}

// TestRunProvision_SandProfileInstallsAnsiblePosix verifies the SAND_PROFILE
// opt-in: when set, the guest script run over `limactl shell` installs the
// ansible.posix collection on demand so the profile_tasks callback (enabled
// unconditionally in ansible.cfg) actually loads. Unset, CreateVM must keep
// using the default collection-free script.
func TestRunProvision_SandProfileInstallsAnsiblePosix(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	t.Setenv("SAND_PROFILE", "1")

	if err := p.CreateVM(context.Background(), testConfig(), io.Discard); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	var sawInstall bool
	for _, call := range f.calls {
		if len(call) > 0 && call[0] == "shell" {
			script := call[len(call)-1]
			if strings.Contains(script, "ansible-galaxy collection install ansible.posix") {
				sawInstall = true
			}
		}
	}
	if !sawInstall {
		t.Errorf("SAND_PROFILE=1 did not select a script that installs ansible.posix; calls:\n%v", f.calls)
	}
}

// TestRunProvision_SandProfileUnsetStaysDefault confirms the unset (default)
// case runs the unmodified inGuestScript, never touching ansible-galaxy.
func TestRunProvision_SandProfileUnsetStaysDefault(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	if err := p.CreateVM(context.Background(), testConfig(), io.Discard); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	for _, call := range f.calls {
		// Only inspect calls that actually run the provisioning script (`shell
		// <name> sudo bash -c <script>`) — the needsReboot check is also a
		// `shell` call, but a one-liner probe (`test -e ...`), not the
		// provisioning script, and must not be compared against it.
		if len(call) > 0 && call[0] == "shell" && hasTok(call, "bash") && hasTok(call, "-c") {
			script := call[len(call)-1]
			if script != inGuestScript {
				t.Errorf("SAND_PROFILE unset must run the default inGuestScript verbatim, got:\n%s", script)
			}
		}
	}
}

// TestCreateVM_BuildsBaseWhenAbsent: with no base image (empty status), CreateVM
// builds the base first (create -> base provision -> stop) and only then clones
// and finalizes. The base provision must NOT carry the git identity; the
// finalize provision must.
func TestCreateVM_BuildsBaseWhenAbsent(t *testing.T) {
	f := &fakeRunner{} // no canned status => target and base both absent
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	if err := p.CreateVM(context.Background(), testConfig(), io.Discard); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	// Inspect the sequence by leading argv tokens; the base-overlay path passed to
	// `start --name` is a temp file, so compare only its stable prefix.
	type call struct{ first, second string }
	var got []call
	for _, c := range f.calls {
		cl := call{first: c[0]}
		if len(c) > 1 {
			cl.second = c[1]
		}
		got = append(got, cl)
	}
	want := []call{
		{"list", "claude"},        // exists-guard: target absent
		{"list", "sandbar-base"},  // Status(base) -> absent
		{"start", "--name"},       // BuildBase: Create(base)
		{"shell", "sandbar-base"}, // BuildBase: base provision
		{"shell", "sandbar-base"}, // BuildBase: harvest step 1 - clear apt lock/partial before the copy
		{"copy", "-v"},            // BuildBase: harvest step 2 - copy the archives out (seed is a no-op: nothing cached yet)
		{"stop", "sandbar-base"},  // BuildBase: stop base
		{"clone", "sandbar-base"}, // Clone
		{"edit", "--set"},         // Configure clone sizes
		{"start", "claude"},       // Start clone
		{"shell", "claude"},       // finalize provision
		{"shell", "claude"},       // needsReboot check
		{"stop", "claude"},        // bounce: stop (fakeRunner defaults to success, so needsReboot reads true)
		{"start", "claude"},       // bounce: start
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CreateVM(absent base) sequence mismatch:\n got %v\nwant %v", got, want)
	}

	// Two provisions ran: base (no identity) then finalize (with identity).
	if len(f.streams) != 2 {
		t.Fatalf("got %d streamed stdins, want 2", len(f.streams))
	}
	if !strings.Contains(f.streams[0], "provision_phase: base") {
		t.Errorf("first provision is not the base phase:\n%s", f.streams[0])
	}
	if strings.Contains(f.streams[0], "user_git_user_name") {
		t.Errorf("base provision must not carry the git identity:\n%s", f.streams[0])
	}
	if !strings.Contains(f.streams[1], "provision_phase: finalize") {
		t.Errorf("second provision is not the finalize phase:\n%s", f.streams[1])
	}
}

// TestCreateVM_RefusesExistingTarget: when the named instance already exists,
// CreateVM bails out before touching the base image or cloning.
func TestCreateVM_RefusesExistingTarget(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"claude": []byte("Running\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	err := p.CreateVM(context.Background(), testConfig(), io.Discard)
	if err == nil {
		t.Fatal("CreateVM should refuse an existing target")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v, want an 'already exists' message", err)
	}
	// Only the guard's status check ran — no clone/start/shell.
	for _, c := range f.calls {
		switch c[0] {
		case "clone", "start", "shell", "stop":
			t.Fatalf("CreateVM took a destructive/creative action despite existing target: %v", f.calls)
		}
	}
}

// stubBaseVersion replaces the git/filesystem indirections used by the base
// staleness check for the duration of a test, returning a captured record of the
// version written to each base and a restore func.
func stubBaseVersion(t *testing.T, current string, currentErr error, stamped map[string]string) map[string]string {
	t.Helper()
	written := map[string]string{}
	origVer, origRead, origWrite := playbookVersionFn, readBaseVersionFn, writeBaseVersionFn
	playbookVersionFn = func(string, string) (string, error) { return current, currentErr }
	readBaseVersionFn = func(_ lima.HostFiles, base string) string { return stamped[base] }
	writeBaseVersionFn = func(_ lima.HostFiles, base, v string, _ time.Time) error { written[base] = v; return nil }
	t.Cleanup(func() {
		playbookVersionFn, readBaseVersionFn, writeBaseVersionFn = origVer, origRead, origWrite
	})
	// This helper exists to drive the PLAYBOOK-VERSION staleness dimension
	// (baseStale); default every base to freshly built so callers are not
	// incidentally tripped by the unrelated 30-day age-refresh check
	// (baseNeedsRefresh). Tests that exercise the age dimension itself
	// (TestConcurrentCreatesRefreshTheAgedBaseOnce and friends) stub
	// readBaseBuiltAtFn themselves, after calling this.
	stubFreshBuiltAt(t)
	return written
}

// stubFreshBuiltAt makes every base report a BuiltAt of "now" for the duration
// of a test, so ensureBaseStopped's age check (baseNeedsRefresh) never fires
// unless a test deliberately overrides readBaseBuiltAtFn afterwards to say
// otherwise.
func stubFreshBuiltAt(t *testing.T) {
	t.Helper()
	orig := readBaseBuiltAtFn
	readBaseBuiltAtFn = func(lima.HostFiles, string) (time.Time, bool) { return time.Now(), true }
	t.Cleanup(func() { readBaseBuiltAtFn = orig })
}

// firstSecond reduces a call sequence to its leading two argv tokens, matching
// the comparison style of TestCreateVM_BuildsBaseWhenAbsent (temp-file paths in
// `start --name <tmp>` make full-argv comparison brittle).
func firstSecond(calls [][]string) []struct{ first, second string } {
	var got []struct{ first, second string }
	for _, c := range calls {
		cl := struct{ first, second string }{first: c[0]}
		if len(c) > 1 {
			cl.second = c[1]
		}
		got = append(got, cl)
	}
	return got
}

// stubBaseOverlay makes the base image report the overlay it was created from —
// which playbook directory it mounts, and the bootstrap script Lima runs in it —
// without materialising a Lima instance on disk. The real implementation reads the
// instance's own lima.yaml (baseoverlay.go, tested against a real Lima instance
// file there); an empty dir stands for a base whose overlay cannot be read at all.
//
// Only a base whose overlay matches the one this create would use may be converged
// in place; everything else is rebuilt from scratch. See baseoverlay.go.
func stubBaseOverlay(t *testing.T, dir string, bootstrap string) {
	t.Helper()
	orig := baseOverlayFn
	baseOverlayFn = func(lima.HostFiles, string) (baseOverlay, bool) {
		if dir == "" {
			return baseOverlay{}, false
		}
		return baseOverlay{PlaybookDir: dir, Bootstrap: bootstrap}, true
	}
	t.Cleanup(func() { baseOverlayFn = orig })
}

// currentBootstrap is the bootstrap script the CURRENT base overlay installs — what
// a base created by this build of sand carries, and therefore the only bootstrap a
// base may have and still be converged in place.
func currentBootstrap(t *testing.T) string {
	t.Helper()
	y, err := RenderBaseOverlay(vm.DefaultCreateConfig(), "/playbook")
	if err != nil {
		t.Fatalf("RenderBaseOverlay: %v", err)
	}
	o, ok := parseBaseOverlay(y)
	if !ok {
		t.Fatalf("could not parse the current base overlay:\n%s", y)
	}
	return o.Bootstrap
}

// stubConvergeableBase makes the base look exactly like one this build of sand
// created from the playbook at dir: the case — and the only case — in which a stale
// base is converged in place rather than rebuilt.
func stubConvergeableBase(t *testing.T, dir string) {
	t.Helper()
	stubBaseOverlay(t, dir, currentBootstrap(t))
}

// TestCreateVM_StaleBaseIsReappliedInPlace: an existing base whose recorded
// playbook version differs from the current one is NOT destroyed. Ansible is
// idempotent, so the base is started, the base-phase playbook is re-run against
// it (converging only the delta), it is re-stamped, and it is stopped again ready
// to be cloned. A from-scratch rebuild would re-download Debian and re-run every
// task for what may be a one-line playbook edit.
func TestCreateVM_StaleBaseIsReappliedInPlace(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
	written := stubBaseVersion(t, "newsha", nil, map[string]string{"sandbar-base": "oldsha"})
	stubConvergeableBase(t, "/playbook") // a base this build of sand created, from this playbook

	if err := p.CreateVM(context.Background(), testConfig(), io.Discard); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	type call = struct{ first, second string }
	want := []call{
		{"list", "claude"},        // exists-guard: target absent
		{"list", "sandbar-base"},  // Status(base) -> Stopped
		{"start", "sandbar-base"}, // re-apply: start the EXISTING base (no delete, no `start --name`)
		{"shell", "sandbar-base"}, // re-apply: re-run the base playbook against it
		{"shell", "sandbar-base"}, // re-apply: harvest step 1 - clear apt lock/partial before the copy
		{"copy", "-v"},            // re-apply: harvest step 2 - copy the archives out (seed is a no-op: nothing cached yet)
		{"stop", "sandbar-base"},  // re-apply: stop it again for cloning
		{"clone", "sandbar-base"}, // Clone
		{"edit", "--set"},         // Configure clone sizes
		{"start", "claude"},       // Start clone
		{"shell", "claude"},       // finalize provision
		{"shell", "claude"},       // needsReboot check
		{"stop", "claude"},        // bounce: stop (fakeRunner defaults to success, so needsReboot reads true)
		{"start", "claude"},       // bounce: start
	}
	if got := firstSecond(f.calls); !reflect.DeepEqual(got, want) {
		t.Fatalf("stale-base re-apply sequence mismatch:\n got %v\nwant %v", got, want)
	}
	for _, c := range f.calls {
		if c[0] == "delete" {
			t.Fatalf("a stale base must be converged in place, never deleted: %v", f.calls)
		}
	}
	if written["sandbar-base"] != "newsha" {
		t.Errorf("re-applied base stamped %q, want newsha", written["sandbar-base"])
	}

	// The re-apply is the BASE phase, run through the same in-guest script as the
	// build: vars over stdin (never argv), no git identity on the base image, and
	// its own SAND_ANSIBLE_TASK_TOTAL marker — a third Ansible run down one pipe
	// whose banners would otherwise be counted against the previous run's total
	// (internal/ui/ansible.go).
	if len(f.streams) != 2 {
		t.Fatalf("got %d streamed stdins, want 2 (base re-apply, finalize)", len(f.streams))
	}
	if !strings.Contains(f.streams[0], "provision_phase: base") {
		t.Errorf("the re-apply must run the base phase:\n%s", f.streams[0])
	}
	if strings.Contains(f.streams[0], "user_git_user_name") {
		t.Errorf("the base re-apply must not carry the git identity:\n%s", f.streams[0])
	}
	reapply := findCall(t, f.calls, 0, "base re-apply shell", func(c []string) bool {
		return c[0] == "shell" && len(c) > 1 && c[1] == "sandbar-base"
	})
	script := f.calls[reapply][len(f.calls[reapply])-1]
	if !strings.Contains(script, "SAND_ANSIBLE_TASK_TOTAL=") {
		t.Errorf("the re-apply run must emit its own task-total marker, or the TUI's progress bar keeps counting against the previous run's total:\n%s", script)
	}
}

// TestCreateVM_ShrinkingToolsetPrintsAdvisoryOnReapply is the shrink-detection
// acceptance criterion: Ansible converges an in-place re-apply's ADDITIONS but
// cannot converge a REMOVAL (it will not uninstall a package whose task no
// longer applies), so a base stamped ddev+go+java that is converged toward a
// ddev+go selection (Java de-selected) must print a clear advisory that Java
// remains installed until the base is rebuilt — never leave stale software
// installed silently.
func TestCreateVM_ShrinkingToolsetPrintsAdvisoryOnReapply(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
	stubBaseVersion(t, "v2:deadbeef:ddev+go", nil, map[string]string{"sandbar-base": "v2:deadbeef:ddev+go+java"})
	stubConvergeableBase(t, "/playbook")

	cfg := testConfig()
	cfg.WithJava = false // de-selecting Java: a shrink relative to the stamped ddev+go+java

	var out bytes.Buffer
	if err := p.CreateVM(context.Background(), cfg, &out); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "java") {
		t.Errorf("advisory did not name the de-selected tool (java):\n%s", got)
	}
	if !strings.Contains(got, "Ansible cannot uninstall") {
		t.Errorf("advisory did not explain why the tool remains installed:\n%s", got)
	}
	if !strings.Contains(got, "--rebuild") {
		t.Errorf("advisory did not point at the fix (sand create --rebuild):\n%s", got)
	}
	// Only the de-selected tool is named — ddev and go are still selected, so
	// they must not appear as if THEY were the residue.
	if strings.Contains(got, "ddev were") || strings.Contains(got, "go were") {
		t.Errorf("advisory named a tool that was not de-selected:\n%s", got)
	}
}

// TestCreateVM_GrowingToolsetPrintsNoAdvisory is the converse: Ansible CAN
// converge an addition, so growing the selection (or leaving it unchanged)
// must never print the shrink advisory — there is no residue to warn about.
func TestCreateVM_GrowingToolsetPrintsNoAdvisory(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
	stubBaseVersion(t, "v2:deadbeef:ddev+go+java", nil, map[string]string{"sandbar-base": "v2:deadbeef:ddev+go"})
	stubConvergeableBase(t, "/playbook")

	var out bytes.Buffer
	if err := p.CreateVM(context.Background(), testConfig(), &out); err != nil { // default cfg: all three true
		t.Fatalf("CreateVM: %v", err)
	}

	if got := out.String(); strings.Contains(got, "Ansible cannot uninstall") {
		t.Errorf("a growing tool-set selection must not print the shrink advisory:\n%s", got)
	}
}

// TestCreateVM_ReapplyFailureDoesNotStampTheBase is the poisoning guard: a base
// whose re-apply FAILS is left unambiguously stale (its old stamp untouched), so
// the next create retries it. Stamping a half-converged base would be silent
// corruption — every clone taken from it afterwards would carry content the stamp
// swears it does not.
func TestCreateVM_ReapplyFailureDoesNotStampTheBase(t *testing.T) {
	f := &fakeRunner{
		status: map[string][]byte{"sandbar-base": []byte("Stopped\n")},
		failOn: func(c []string) bool { // the base playbook blows up mid-run
			return c[0] == "shell" && len(c) > 1 && c[1] == "sandbar-base"
		},
		failErr: errors.New("ansible: task failed"),
	}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
	written := stubBaseVersion(t, "newsha", nil, map[string]string{"sandbar-base": "oldsha"})
	stubConvergeableBase(t, "/playbook")

	err := p.CreateVM(context.Background(), testConfig(), io.Discard)
	if err == nil {
		t.Fatal("CreateVM must fail when the base re-apply fails")
	}
	if v, ok := written["sandbar-base"]; ok {
		t.Fatalf("a FAILED re-apply stamped the base %q — every later clone would be silently poisoned by a base that only claims to be current", v)
	}
	// And it must not have cloned from the half-converged base either.
	for _, c := range f.calls {
		if c[0] == "clone" {
			t.Fatalf("a failed re-apply must not be followed by a clone: %v", f.calls)
		}
	}
}

// TestCreateVM_StaleBaseWithAnUnconvergeableOverlayRebuilds: a re-apply cannot
// change the base's OVERLAY — the lima.yaml Lima wrote when the instance was
// created and re-applies to it on every start. Two of its fields decide whether an
// in-place converge is even meaningful, and a base that fails either one is rebuilt
// from scratch instead (which re-creates it under the current overlay):
//
//   - THE PLAYBOOK MOUNT. The guest rsyncs its playbook out of /mnt/playbook, i.e.
//     out of the host directory the base was CREATED with. A git worktree beside the
//     main tree — or a released binary, which extracts the embedded playbook to a
//     fresh temp dir on every run — gives the base a mount pointing somewhere else.
//     Re-applying there runs a DIFFERENT playbook and then stamps it with OUR
//     version: a base that claims content it does not have, cloned into every VM
//     from then on, and never detected again because the stamp matches.
//
//   - THE BOOTSTRAP SCRIPT. Lima's dependency script installs what the playbook is
//     allowed to assume (ansible-core, rsync, curl, gnupg — the base role shells out
//     to `gpg --dearmor` and names them as guaranteed). A base created under an older
//     script does not carry that guarantee: the run fails, the base is correctly left
//     stale, and the next create fails the same way. A wedge, not a rebuild.
func TestCreateVM_StaleBaseWithAnUnconvergeableOverlayRebuilds(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mount     string
		bootstrap string
	}{
		{"the base mounts another checkout", "/some/other/worktree", ""},
		{"the base's overlay cannot be read", "", ""},
		{"the base was created with an older bootstrap", "/playbook", "#!/bin/bash\napt-get install -y ansible\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
			p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
			written := stubBaseVersion(t, "newsha", nil, map[string]string{"sandbar-base": "oldsha"})
			bootstrap := tc.bootstrap
			if bootstrap == "" {
				bootstrap = currentBootstrap(t) // isolate the field under test
			}
			stubBaseOverlay(t, tc.mount, bootstrap)

			if err := p.CreateVM(context.Background(), testConfig(), io.Discard); err != nil {
				t.Fatalf("CreateVM: %v", err)
			}

			type call = struct{ first, second string }
			want := []call{
				{"list", "claude"},         // exists-guard: target absent
				{"list", "sandbar-base"},   // Status(base) -> Stopped
				{"delete", "sandbar-base"}, // cannot be converged in place: rebuild instead
				{"start", "--name"},        // BuildBase: Create(base) under the CURRENT overlay
				{"shell", "sandbar-base"},  // BuildBase: base provision
				{"shell", "sandbar-base"},  // BuildBase: harvest step 1 - clear apt lock/partial before the copy
				{"copy", "-v"},             // BuildBase: harvest step 2 - copy the archives out (seed is a no-op: nothing cached yet)
				{"stop", "sandbar-base"},   // BuildBase: stop base
				{"clone", "sandbar-base"},  // Clone
				{"edit", "--set"},          // Configure clone sizes
				{"start", "claude"},        // Start clone
				{"shell", "claude"},        // finalize provision
				{"shell", "claude"},        // needsReboot check
				{"stop", "claude"},         // bounce: stop (fakeRunner defaults to success, so needsReboot reads true)
				{"start", "claude"},        // bounce: start
			}
			if got := firstSecond(f.calls); !reflect.DeepEqual(got, want) {
				t.Fatalf("unconvergeable-overlay rebuild sequence mismatch:\n got %v\nwant %v", got, want)
			}
			// The force flag must be set when deleting the base.
			for _, c := range f.calls {
				if c[0] == "delete" && (len(c) < 3 || c[2] != "-f") {
					t.Errorf("base delete missing -f: %v", c)
				}
			}
			if written["sandbar-base"] != "newsha" {
				t.Errorf("rebuilt base stamped %q, want newsha", written["sandbar-base"])
			}
		})
	}
}

// TestCreateVM_RebuildDestroysEvenAnUpToDateBase: --rebuild is the escape hatch
// for a base the idempotent re-apply cannot fix, so it destroys and rebuilds the
// base from scratch regardless of what the version stamp says.
func TestCreateVM_RebuildDestroysEvenAnUpToDateBase(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
	written := stubBaseVersion(t, "samesha", nil, map[string]string{"sandbar-base": "samesha"}) // NOT stale
	stubConvergeableBase(t, "/playbook")

	err := p.CreateVMWithOptions(context.Background(), testConfig(), CreateOptions{Rebuild: true}, io.Discard)
	if err != nil {
		t.Fatalf("CreateVM(--rebuild): %v", err)
	}

	type call = struct{ first, second string }
	want := []call{
		{"list", "claude"},         // exists-guard: target absent
		{"list", "sandbar-base"},   // Status(base) -> Stopped
		{"delete", "sandbar-base"}, // --rebuild: force-deleted UNDER THE BASE LOCK
		{"start", "--name"},        // BuildBase: Create(base)
		{"shell", "sandbar-base"},  // BuildBase: base provision
		{"shell", "sandbar-base"},  // BuildBase: harvest step 1 - clear apt lock/partial before the copy
		{"copy", "-v"},             // BuildBase: harvest step 2 - copy the archives out (seed is a no-op: nothing cached yet)
		{"stop", "sandbar-base"},   // BuildBase: stop base
		{"clone", "sandbar-base"},  // Clone
		{"edit", "--set"},          // Configure clone sizes
		{"start", "claude"},        // Start clone
		{"shell", "claude"},        // finalize provision
		{"shell", "claude"},        // needsReboot check
		{"stop", "claude"},         // bounce: stop (fakeRunner defaults to success, so needsReboot reads true)
		{"start", "claude"},        // bounce: start
	}
	if got := firstSecond(f.calls); !reflect.DeepEqual(got, want) {
		t.Fatalf("--rebuild sequence mismatch:\n got %v\nwant %v", got, want)
	}
	if written["sandbar-base"] != "samesha" {
		t.Errorf("rebuilt base stamped %q, want samesha", written["sandbar-base"])
	}
}

// TestRebuildDeletesTheBaseOnlyWhileHoldingTheBaseLock is the ordering proof for
// the race this task closes.
//
// `sand create --rebuild` used to delete the base in the CLI layer
// (cmd/sand/create.go), BEFORE the provisioner — and therefore before the base
// lock was ever taken. Another create holding that lock could be mid-clone from
// the very base being force-deleted underneath it: the exact race baselock.go's
// doc comment says the lock exists to close. The destroy now lives inside
// ensureBaseStopped, which runs with the lock held.
//
// The proof does not stub the lock: it takes the REAL flock, exactly as a
// concurrent create would, and shows the rebuild cannot get past it.
func TestRebuildDeletesTheBaseOnlyWhileHoldingTheBaseLock(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
	// An up-to-date base: nothing but --rebuild can possibly delete it.
	stubBaseVersion(t, "samesha", nil, map[string]string{"sandbar-base": "samesha"})
	stubConvergeableBase(t, "/playbook")

	// Another create is preparing/cloning the base and holds its lock.
	release, err := lockBase(context.Background(), lima.LocalFiles(), "sandbar-base", io.Discard)
	if err != nil {
		t.Fatalf("lockBase: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- p.CreateVMWithOptions(context.Background(), testConfig(), CreateOptions{Rebuild: true}, io.Discard)
	}()

	// Wait until the rebuild is definitely running (its exists-guard has fired),
	// then give it several lock polls (baseLockPoll) to misbehave.
	waitFor(t, 2*time.Second, func() bool {
		return countCalls(f.snapshot(), func(c []string) bool { return c[0] == "list" && c[1] == "claude" }) == 1
	}, "the rebuild to reach the base lock")
	time.Sleep(4 * baseLockPoll)

	for _, c := range f.snapshot() {
		if c[0] == "delete" {
			release()
			t.Fatalf("--rebuild DELETED the base while another create held the base lock — it may have been mid-clone from it: %v", f.snapshot())
		}
	}

	f.mark("base-lock-released")
	release()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("CreateVM(--rebuild) after the lock was released: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CreateVM(--rebuild) never completed after the base lock was released")
	}

	// The recorded sequence proves the ordering: the delete lands strictly after
	// the lock became free.
	calls := f.snapshot()
	freed := findCall(t, calls, 0, "base-lock-released", func(c []string) bool { return c[0] == "base-lock-released" })
	del := findCall(t, calls, 0, "delete sandbar-base", func(c []string) bool {
		return c[0] == "delete" && len(c) > 1 && c[1] == "sandbar-base"
	})
	if del < freed {
		t.Fatalf("the base was deleted at call %d, BEFORE the base lock was released at %d: %v", del, freed, calls)
	}
}

// waitFor polls cond until it is true or the timeout expires, failing the test
// with what it was waiting for. Polling (rather than sleeping a fixed duration)
// keeps the base-lock tests fast when the code is right and unambiguous when it
// is not.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// TestCancelledReapplyReleasesTheBaseLock: a user who hits ctrl+c while the base
// is being re-applied must not wedge every other create on this machine. The lock
// is released on the way out of a cancelled run, so the next create takes it and
// proceeds — and, the re-apply having failed, it finds the base still stale and
// retries it rather than cloning a half-converged image.
func TestCancelledReapplyReleasesTheBaseLock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	f.hook = func(c []string) {
		if c[0] == "shell" && len(c) > 1 && c[1] == "sandbar-base" {
			cancel() // ctrl+c, mid-re-apply
		}
	}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
	written := stubBaseVersion(t, "newsha", nil, map[string]string{"sandbar-base": "oldsha"})
	stubConvergeableBase(t, "/playbook")

	if err := p.CreateVM(ctx, testConfig(), io.Discard); err == nil {
		t.Fatal("a cancelled re-apply must fail the create")
	}
	if v, ok := written["sandbar-base"]; ok {
		t.Fatalf("a CANCELLED re-apply stamped the base %q; it must be left stale so the next create retries it", v)
	}

	// The next create must not hang: it takes the freed lock and clones.
	f2 := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p2 := &Provisioner{Lima: lima.New(f2), PlaybookDir: "/playbook"}
	next := make(chan error, 1)
	go func() {
		cfg := testConfig()
		cfg.Name = "web"
		next <- p2.prepareBaseAndClone(context.Background(), cfg, CreateOptions{}, io.Discard, newPhaseTimer(io.Discard))
	}()
	select {
	case err := <-next:
		if err != nil {
			t.Fatalf("the create after a cancelled re-apply failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled re-apply did NOT release the base lock — every other create on this machine is wedged behind it")
	}
}

// TestCreateVM_FreshBaseReused: when the base's stamp matches the current
// playbook version, the base is reused as-is — no delete, no rebuild, and no
// re-apply either (the whole point of the stamp is to skip the Ansible run when
// there is provably nothing to converge).
func TestCreateVM_FreshBaseReused(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
	stubBaseVersion(t, "samesha", nil, map[string]string{"sandbar-base": "samesha"})
	stubConvergeableBase(t, "/playbook")

	if err := p.CreateVM(context.Background(), testConfig(), io.Discard); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	for _, c := range f.calls {
		if c[0] == "delete" {
			t.Fatalf("up-to-date base must not be deleted: %v", f.calls)
		}
		if c[0] == "start" && len(c) > 1 && c[1] == "--name" {
			t.Fatalf("up-to-date base must not be rebuilt: %v", f.calls)
		}
		if len(c) > 1 && c[1] == "sandbar-base" && (c[0] == "start" || c[0] == "shell") {
			t.Fatalf("up-to-date base must not be re-applied: %v", f.calls)
		}
	}
}

// TestCreateVM_FreshBaseUnderAgeThresholdIsNotRefreshed: a base whose CONTENT
// is current and whose BuiltAt is recent (well under baseMaxAge) must not be
// touched at all — same posture as TestCreateVM_FreshBaseReused, but pinning
// the AGE dimension explicitly rather than relying on stubBaseVersion's
// default.
func TestCreateVM_FreshBaseUnderAgeThresholdIsNotRefreshed(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
	stubBaseVersion(t, "samesha", nil, map[string]string{"sandbar-base": "samesha"})
	stubConvergeableBase(t, "/playbook")
	origBuiltAt := readBaseBuiltAtFn
	readBaseBuiltAtFn = func(lima.HostFiles, string) (time.Time, bool) { return time.Now().Add(-29 * 24 * time.Hour), true }
	t.Cleanup(func() { readBaseBuiltAtFn = origBuiltAt })

	if err := p.CreateVM(context.Background(), testConfig(), io.Discard); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	for _, c := range f.calls {
		if c[0] == "delete" {
			t.Fatalf("a base under the age threshold must not be deleted: %v", f.calls)
		}
		if len(c) > 1 && c[1] == "sandbar-base" && (c[0] == "start" || c[0] == "shell") {
			t.Fatalf("a base under the age threshold must not be refreshed: %v", f.calls)
		}
	}
}

// TestCreateVM_AgedBaseIsRefreshedInPlace is the acceptance criterion this task
// exists for: a base whose CONTENT is current but whose BuiltAt is older than
// baseMaxAge is started, upgraded (base_apt_upgrade emitted for that ONE
// shell), re-stamped, and stopped — via reapplyBase, the SAME machinery a
// playbook-version re-apply uses — and the CLONE's own finalize pass never
// carries base_apt_upgrade, so the clone itself runs no upgrade.
func TestCreateVM_AgedBaseIsRefreshedInPlace(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
	written := stubBaseVersion(t, "samesha", nil, map[string]string{"sandbar-base": "samesha"}) // NOT stale
	stubConvergeableBase(t, "/playbook")
	origBuiltAt := readBaseBuiltAtFn
	readBaseBuiltAtFn = func(lima.HostFiles, string) (time.Time, bool) { return time.Now().Add(-31 * 24 * time.Hour), true }
	t.Cleanup(func() { readBaseBuiltAtFn = origBuiltAt })

	if err := p.CreateVM(context.Background(), testConfig(), io.Discard); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	type call = struct{ first, second string }
	want := []call{
		{"list", "claude"},        // exists-guard: target absent
		{"list", "sandbar-base"},  // Status(base) -> Stopped
		{"start", "sandbar-base"}, // refresh: start the EXISTING base (no delete, no `start --name`)
		{"shell", "sandbar-base"}, // refresh: run the base playbook (apt upgrade) against it
		{"shell", "sandbar-base"}, // refresh: harvest step 1 - clear apt lock/partial before the copy
		{"copy", "-v"},            // refresh: harvest step 2 - copy the archives out (seed is a no-op: nothing cached yet)
		{"stop", "sandbar-base"},  // refresh: stop it again for cloning
		{"clone", "sandbar-base"}, // Clone
		{"edit", "--set"},         // Configure clone sizes
		{"start", "claude"},       // Start clone
		{"shell", "claude"},       // finalize provision
		{"shell", "claude"},       // needsReboot check
		{"stop", "claude"},        // bounce: stop (fakeRunner defaults to success, so needsReboot reads true)
		{"start", "claude"},       // bounce: start
	}
	if got := firstSecond(f.calls); !reflect.DeepEqual(got, want) {
		t.Fatalf("aged-base refresh sequence mismatch:\n got %v\nwant %v", got, want)
	}
	for _, c := range f.calls {
		if c[0] == "delete" {
			t.Fatalf("an aged but convergeable base must be refreshed in place, never deleted: %v", f.calls)
		}
	}
	if written["sandbar-base"] != "samesha" {
		t.Errorf("refreshed base stamped %q, want samesha", written["sandbar-base"])
	}

	if len(f.streams) != 2 {
		t.Fatalf("got %d streamed stdins, want 2 (base refresh + finalize)", len(f.streams))
	}
	if !strings.Contains(f.streams[0], "provision_phase: base") {
		t.Errorf("first provision is not the base phase:\n%s", f.streams[0])
	}
	if !strings.Contains(f.streams[0], "base_apt_upgrade: true") {
		t.Errorf("the base refresh's own provision must emit base_apt_upgrade: true:\n%s", f.streams[0])
	}
	if !strings.Contains(f.streams[1], "provision_phase: finalize") {
		t.Errorf("second provision is not the finalize phase:\n%s", f.streams[1])
	}
	if strings.Contains(f.streams[1], "base_apt_upgrade") {
		t.Errorf("the clone's finalize provision must never carry base_apt_upgrade:\n%s", f.streams[1])
	}
}

// TestCreateVM_AgedUnconvergeableBaseRebuilds: an aged base whose overlay
// cannot be converged in place (baseoverlay.go) must fall back to a full
// rebuild — the same fallback the playbook-version staleness path takes — not
// wedge waiting for a converge that can never succeed.
func TestCreateVM_AgedUnconvergeableBaseRebuilds(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
	written := stubBaseVersion(t, "samesha", nil, map[string]string{"sandbar-base": "samesha"}) // NOT stale
	stubBaseOverlay(t, "/some/other/worktree", currentBootstrap(t))                             // unconvergeable
	origBuiltAt := readBaseBuiltAtFn
	readBaseBuiltAtFn = func(lima.HostFiles, string) (time.Time, bool) { return time.Now().Add(-31 * 24 * time.Hour), true }
	t.Cleanup(func() { readBaseBuiltAtFn = origBuiltAt })

	if err := p.CreateVM(context.Background(), testConfig(), io.Discard); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	type call = struct{ first, second string }
	want := []call{
		{"list", "claude"},         // exists-guard: target absent
		{"list", "sandbar-base"},   // Status(base) -> Stopped
		{"delete", "sandbar-base"}, // cannot be refreshed in place: rebuild instead
		{"start", "--name"},        // BuildBase: Create(base) under the CURRENT overlay
		{"shell", "sandbar-base"},  // BuildBase: base provision
		{"shell", "sandbar-base"},  // BuildBase: harvest step 1 - clear apt lock/partial before the copy
		{"copy", "-v"},             // BuildBase: harvest step 2 - copy the archives out (seed is a no-op: nothing cached yet)
		{"stop", "sandbar-base"},   // BuildBase: stop base
		{"clone", "sandbar-base"},  // Clone
		{"edit", "--set"},          // Configure clone sizes
		{"start", "claude"},        // Start clone
		{"shell", "claude"},        // finalize provision
		{"shell", "claude"},        // needsReboot check
		{"stop", "claude"},         // bounce: stop (fakeRunner defaults to success, so needsReboot reads true)
		{"start", "claude"},        // bounce: start
	}
	if got := firstSecond(f.calls); !reflect.DeepEqual(got, want) {
		t.Fatalf("aged-unconvergeable-base rebuild sequence mismatch:\n got %v\nwant %v", got, want)
	}
	if written["sandbar-base"] != "samesha" {
		t.Errorf("rebuilt base stamped %q, want samesha", written["sandbar-base"])
	}
}

// TestConcurrentCreatesRefreshTheAgedBaseOnce is the double-checked-locking
// discipline this task requires for the AGE dimension, mirroring
// TestConcurrentCreatesReapplyTheStaleBaseOnce for the VERSION dimension: two
// creates race for the base lock against an aged (>30-day) but
// content-current base. The winner refreshes it and re-stamps a fresh
// BuiltAt; the loser, on acquiring the lock, MUST re-read that fresh
// timestamp and skip its own refresh — otherwise every queued create
// redundantly re-upgrades the same base.
func TestConcurrentCreatesRefreshTheAgedBaseOnce(t *testing.T) {
	r := &baseRaceRunner{buildDelay: 150 * time.Millisecond, built: true} // the base already exists
	p := &Provisioner{Lima: lima.New(r), PlaybookDir: t.TempDir()}
	stubConvergeableBase(t, p.PlaybookDir) // …created by this build of sand, from this playbook

	// The playbook content never changes in this test — both creates always find
	// the base's version CURRENT (baseStale = false) — so the only reason either
	// of them would touch the base at all is the age check.
	origVer, origRead, origWrite := playbookVersionFn, readBaseVersionFn, writeBaseVersionFn
	playbookVersionFn = func(string, string) (string, error) { return "v1", nil }
	readBaseVersionFn = func(lima.HostFiles, string) string { return "v1" }
	writeBaseVersionFn = func(lima.HostFiles, string, string, time.Time) error { return nil }
	t.Cleanup(func() {
		playbookVersionFn, readBaseVersionFn, writeBaseVersionFn = origVer, origRead, origWrite
	})

	// The base is 31 days old at the moment BOTH creates start; only the fresh
	// BuiltAt the winner writes can tell the loser it no longer has work to do.
	var bmu sync.Mutex
	builtAt := time.Now().Add(-31 * 24 * time.Hour)
	origBuiltAt := readBaseBuiltAtFn
	readBaseBuiltAtFn = func(lima.HostFiles, string) (time.Time, bool) {
		bmu.Lock()
		defer bmu.Unlock()
		return builtAt, true
	}
	t.Cleanup(func() { readBaseBuiltAtFn = origBuiltAt })
	// reapplyBase's stamp write is what a real refresh does to prove it happened;
	// model it here by advancing builtAt to now, exactly as the real
	// writeBaseVersion (RFC3339 "now") would.
	writeBaseVersionFn = func(lima.HostFiles, string, string, time.Time) error {
		bmu.Lock()
		builtAt = time.Now()
		bmu.Unlock()
		return nil
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	names := []string{"web", "api"}
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = p.prepareBaseAndClone(context.Background(),
				vm.CreateConfig{Name: names[i], BaseName: "sandbar-base"}, CreateOptions{}, io.Discard, newPhaseTimer(io.Discard))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("create %d failed: %v", i, err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reapplies != 1 {
		t.Errorf("the aged base was refreshed %d times, want exactly 1 — the waiter must re-read BuiltAt AFTER the lock, not act on one it cached before it (calls: %v)", r.reapplies, r.seq)
	}
	if r.builds != 0 {
		t.Errorf("the base was rebuilt from scratch %d times; an aged-but-current base is refreshed in place (calls: %v)", r.builds, r.seq)
	}
	if r.touchedDuringClone {
		t.Errorf("the base was started/provisioned/stopped while another create was cloning from it (calls: %v)", r.seq)
	}
}

// TestCreateVM_UnstampedBaseIsReappliedInPlace: a base with no recorded version
// (built before stamping, or by an unknown path) is treated as stale — and, like
// any other stale base whose playbook mount we can match, it is converged in
// place rather than destroyed.
func TestCreateVM_UnstampedBaseIsReappliedInPlace(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
	written := stubBaseVersion(t, "anysha", nil, map[string]string{}) // no stamp
	stubConvergeableBase(t, "/playbook")

	if err := p.CreateVM(context.Background(), testConfig(), io.Discard); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	for _, c := range f.calls {
		if c[0] == "delete" {
			t.Fatalf("an unstamped base must be converged in place, not deleted: %v", f.calls)
		}
		if c[0] == "start" && len(c) > 1 && c[1] == "--name" {
			t.Fatalf("an unstamped base must not be rebuilt from scratch: %v", f.calls)
		}
	}
	findCall(t, f.calls, 0, "base re-apply", func(c []string) bool {
		return c[0] == "shell" && len(c) > 1 && c[1] == "sandbar-base"
	})
	if written["sandbar-base"] != "anysha" {
		t.Errorf("re-applied base stamped %q, want anysha", written["sandbar-base"])
	}
}

// TestCreateVM_VersionErrorReusesBase: when the playbook version can't be
// determined (e.g. not a git checkout), the existing base is reused rather than
// rebuilt on every create.
func TestCreateVM_VersionErrorReusesBase(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
	stubBaseVersion(t, "", errors.New("not a git checkout"), map[string]string{"sandbar-base": "oldsha"})

	if err := p.CreateVM(context.Background(), testConfig(), io.Discard); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	for _, c := range f.calls {
		if c[0] == "delete" {
			t.Fatalf("base must be reused when version is unknown: %v", f.calls)
		}
	}
}

// TestRecreate deletes (force) then runs the full CreateVM sequence.
func TestRecreate(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	if err := p.Recreate(context.Background(), testConfig(), io.Discard); err != nil {
		t.Fatalf("Recreate: %v", err)
	}

	if len(f.calls) < 3 {
		t.Fatalf("Recreate made too few calls: %v", f.calls)
	}
	if got, want := f.calls[0], []string{"delete", "claude", "-f"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Recreate first call = %v, want %v", got, want)
	}
	// Recreate skips CreateVM's exists-guard (no Status(claude) re-check of the
	// just-deleted target): base status check, then clone.
	if got, want := f.calls[2], []string{"clone", "sandbar-base", "claude"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Recreate did not proceed to clone: calls=%v", f.calls)
	}
}

// hasTok reports whether argv contains the exact token tok.
func hasTok(argv []string, tok string) bool {
	for _, a := range argv {
		if a == tok {
			return true
		}
	}
	return false
}

// findCall returns the index of the first call at or after `from` for which
// match returns true, failing the test (with a label) when none matches. It is
// used to assert ordering by scanning for distinctive argv tokens.
func findCall(t *testing.T, calls [][]string, from int, what string, match func([]string) bool) int {
	t.Helper()
	for i := from; i < len(calls); i++ {
		if match(calls[i]) {
			return i
		}
	}
	t.Fatalf("could not find call %q at/after index %d in %v", what, from, calls)
	return -1
}

// TestReset_NoPreserve: a reset with no preservation is a clean recreate. With a
// stopped base and CloneURL set, it must delete -> ensure base -> clone ->
// configure -> start -> finalize -> stop -> start, and the finalize vars keep
// project_clone_url (the role re-clones the repo).
func TestReset_NoPreserve(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig()
	cfg.CloneURL = "https://github.com/lullabot/sandbar"

	if err := p.Reset(context.Background(), cfg, ResetOptions{}, io.Discard); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	want := [][]string{
		{"delete", "claude", "-f"},                          // destroy
		{"list", "sandbar-base", "--format", "{{.Status}}"}, // ensureBaseStopped
		{"clone", "sandbar-base", "claude"},                 // re-clone
		{"edit", "--set", `.cpus=4 | .memory="8GiB" | .disk="100GiB" | .mounts |= map(select(.writable != true))`, "claude"}, // configure size (and strip the base's writable apt-cache mount)
		{"start", "claude"}, // start clone
		{"shell", "claude", "sudo", "bash", "-c", inGuestScript},      // finalize
		{"shell", "claude", "test", "-e", "/var/run/reboot-required"}, // needsReboot check
		{"shell", "claude", "tmux", "has-session", "-t", "=main"},     // hasLiveTmux check (fakeRunner defaults to success, so both read true)
		{"stop", "claude"},  // bounce: stop
		{"start", "claude"}, // bounce: start
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("Reset(no preserve) call sequence mismatch:\n got %v\nwant %v", f.calls, want)
	}

	// Only finalize streamed stdin, and it keeps the clone URL.
	if len(f.streams) != 1 {
		t.Fatalf("got %d streamed stdins, want 1 (finalize)", len(f.streams))
	}
	if !strings.Contains(f.streams[0], "project_clone_url") {
		t.Errorf("no-preserve finalize must keep project_clone_url:\n%s", f.streams[0])
	}
}

// TestReset_NoBounceWithoutRebootMarker mirrors
// TestCreateVM_NoBounceWithoutRebootMarker for Reset: a freshly finalized
// clone that never asked for a reboot must not be bounced.
func TestReset_NoBounceWithoutRebootMarker(t *testing.T) {
	f := &fakeRunner{
		status: map[string][]byte{"sandbar-base": []byte("Stopped\n")},
		failOn: func(c []string) bool {
			return len(c) > 0 && c[0] == "shell" && hasTok(c, "/var/run/reboot-required")
		},
		failErr: errors.New("exit status 1"),
	}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	if err := p.Reset(context.Background(), testConfig(), ResetOptions{}, io.Discard); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	for _, c := range f.calls {
		if c[0] == "stop" {
			t.Fatalf("no reboot-required marker: Reset must not bounce (stop/start) the VM: %v", f.calls)
		}
	}
	// The tmux check is only meaningful ahead of a bounce; without a reboot
	// pending there is nothing to warn about, so it must not even run.
	for _, c := range f.calls {
		if hasTok(c, "tmux") {
			t.Fatalf("hasLiveTmux must not be probed when no bounce is needed: %v", f.calls)
		}
	}
}

// TestReset_WarnsButStillBouncesOverLiveTmuxSession is the tmux-safety
// acceptance criterion: when a reboot IS required and the guest's
// persistent `main` tmux session is up (fakeRunner's default: every call
// succeeds, so `tmux has-session -t =main` reads as live), Reset must not
// bounce SILENTLY. It emits a loud step() warning naming the destructive
// consequence before doing it — Reset chooses "warn and proceed" over
// "refuse" (see the code comment on Reset's bounce), since by the time the
// bounce runs, Reset's own destructive work (delete + reclone + restore +
// finalize) is already committed and cannot be cleanly walked back.
func TestReset_WarnsButStillBouncesOverLiveTmuxSession(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	var out bytes.Buffer
	if err := p.Reset(context.Background(), testConfig(), ResetOptions{}, &out); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	// The bounce still happens — Reset does not refuse.
	stop := findCall(t, f.calls, 0, "bounce stop", func(c []string) bool { return c[0] == "stop" })
	findCall(t, f.calls, stop+1, "bounce start", func(c []string) bool { return c[0] == "start" })

	// The tmux session was checked, and its presence was surfaced loudly.
	tmuxCheck := findCall(t, f.calls, 0, "hasLiveTmux probe", func(c []string) bool {
		return c[0] == "shell" && hasTok(c, "tmux") && hasTok(c, "has-session")
	})
	if tmuxCheck >= stop {
		t.Fatalf("hasLiveTmux must be checked BEFORE the bounce, got check at %d, stop at %d: %v", tmuxCheck, stop, f.calls)
	}
	if got := out.String(); !strings.Contains(got, "tmux") || !strings.Contains(got, "claude") {
		t.Errorf("Reset must warn loudly about the live tmux session before bouncing through it, got:\n%s", got)
	}
}

// TestReset_BothPreserve: with both preserves on, the reset stages out the
// Claude login and the project tree, recreates the VM, restores Claude BEFORE
// finalize, finalizes WITHOUT project_clone_url (so the role skips its clone over
// the restored tree), restores the project AFTER finalize, re-approves its .env
// via direnv, then bounces. Assertions focus on ordering and the clone-skip.
func TestReset_BothPreserve(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig()
	cfg.User = "andrew"
	cfg.CloneURL = "https://github.com/lullabot/sandbar"
	opts := ResetOptions{PreserveClaude: true, PreserveProject: true}

	if err := p.Reset(context.Background(), cfg, opts, io.Discard); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	calls := f.calls

	// Stage-out: getent resolves home, then two `tar -czf -` archives (claude,
	// then project under github.com/lullabot).
	getent := findCall(t, calls, 0, "getent", func(c []string) bool { return hasTok(c, "getent") })
	outClaude := findCall(t, calls, getent+1, "stage-out claude (-czf .claude)", func(c []string) bool {
		return hasTok(c, "-czf") && hasTok(c, ".claude")
	})
	outProject := findCall(t, calls, outClaude+1, "stage-out project (-czf github.com/lullabot)", func(c []string) bool {
		return hasTok(c, "-czf") && hasTok(c, "github.com/lullabot")
	})

	// Recreate sized: delete -> ensure base -> clone -> configure -> start.
	del := findCall(t, calls, outProject+1, "delete", func(c []string) bool { return c[0] == "delete" })
	clone := findCall(t, calls, del+1, "clone", func(c []string) bool { return c[0] == "clone" })
	configure := findCall(t, calls, clone+1, "configure (edit --set)", func(c []string) bool {
		return c[0] == "edit" && hasTok(c, "--set")
	})
	startClone := findCall(t, calls, configure+1, "start clone", func(c []string) bool {
		return c[0] == "start" && hasTok(c, "claude")
	})

	// Claude restore (extract + chown) BEFORE finalize.
	inClaude := findCall(t, calls, startClone+1, "stage-in claude (-xzf)", func(c []string) bool {
		return hasTok(c, "-xzf")
	})
	chownClaude := findCall(t, calls, inClaude+1, "chown claude", func(c []string) bool {
		return hasTok(c, "chown") && hasTok(c, "/home/andrew/.claude")
	})
	finalize := findCall(t, calls, chownClaude+1, "finalize (bash -c)", func(c []string) bool {
		return hasTok(c, "bash") && hasTok(c, "-c")
	})

	// Project restore AFTER finalize: extract + chown + direnv allow.
	inProject := findCall(t, calls, finalize+1, "stage-in project (-xzf)", func(c []string) bool {
		return hasTok(c, "-xzf")
	})
	chownProject := findCall(t, calls, inProject+1, "chown project", func(c []string) bool {
		return hasTok(c, "chown") && hasTok(c, "/home/andrew/github.com/lullabot")
	})
	direnv := findCall(t, calls, chownProject+1, "direnv allow", func(c []string) bool {
		return hasTok(c, "direnv") && hasTok(c, "allow")
	})

	// Bounce: stop -> start.
	stop := findCall(t, calls, direnv+1, "stop", func(c []string) bool { return c[0] == "stop" })
	findCall(t, calls, stop+1, "start (bounce)", func(c []string) bool { return c[0] == "start" })

	// The finalize vars must NOT carry project_clone_url (clone is skipped).
	var finStream string
	for _, s := range f.streams {
		if strings.Contains(s, "provision_phase: finalize") {
			finStream = s
		}
	}
	if finStream == "" {
		t.Fatalf("no finalize stdin captured; streams=%v", f.streams)
	}
	if strings.Contains(finStream, "project_clone_url") {
		t.Errorf("preserve-project finalize must skip project_clone_url:\n%s", finStream)
	}
}

// countCalls reports how many recorded calls satisfy match.
func countCalls(calls [][]string, match func([]string) bool) int {
	n := 0
	for _, c := range calls {
		if match(c) {
			n++
		}
	}
	return n
}

func isTarOut(c []string) bool { return hasTok(c, "-czf") }
func isTarIn(c []string) bool  { return hasTok(c, "-xzf") }

// finalizeStream returns the streamed stdin of the finalize provision (the one
// carrying provision_phase: finalize), failing the test if none was captured.
func finalizeStream(t *testing.T, streams []string) string {
	t.Helper()
	for _, s := range streams {
		if strings.Contains(s, "provision_phase: finalize") {
			return s
		}
	}
	t.Fatalf("no finalize stdin captured; streams=%v", streams)
	return ""
}

// stageDirs lists the leaked host staging directories (sand-reset-*) so a
// test can assert the reset cleans up after itself on success or leaves the dir
// (for recovery) on a post-staging failure.
func stageDirs(t *testing.T) []string {
	t.Helper()
	g, err := filepath.Glob(filepath.Join(os.TempDir(), "sand-reset-*"))
	if err != nil {
		t.Fatalf("glob stage dirs: %v", err)
	}
	return g
}

// TestReset_ClaudeOnly: preserving only the Claude login stages out ~/.claude
// (one tar archive, no project archive), restores it BEFORE finalize, and leaves
// the finalize project_clone_url in place (the repo is re-cloned, not preserved).
func TestReset_ClaudeOnly(t *testing.T) {
	before := len(stageDirs(t))
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig()
	cfg.CloneURL = "https://github.com/lullabot/sandbar"

	if err := p.Reset(context.Background(), cfg, ResetOptions{PreserveClaude: true}, io.Discard); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	// Exactly one stage-out (claude) and one stage-in (claude); the project tree
	// is never archived.
	if n := countCalls(f.calls, isTarOut); n != 1 {
		t.Fatalf("claude-only reset should stage out exactly once, got %d", n)
	}
	if n := countCalls(f.calls, isTarIn); n != 1 {
		t.Fatalf("claude-only reset should stage in exactly once, got %d", n)
	}
	for _, c := range f.calls {
		if hasTok(c, "-czf") && hasTok(c, "github.com/lullabot") {
			t.Fatalf("claude-only reset must not stage out the project tree: %v", c)
		}
		if hasTok(c, "direnv") {
			t.Fatalf("claude-only reset must not run direnv: %v", c)
		}
	}

	// Claude restore (-xzf) must land BEFORE finalize so the playbook re-applies
	// settings.json on top.
	inClaude := findCall(t, f.calls, 0, "stage-in claude", isTarIn)
	findCall(t, f.calls, inClaude+1, "finalize after restore", func(c []string) bool {
		return hasTok(c, "bash") && hasTok(c, "-c")
	})

	// The repo is re-cloned (not preserved), so finalize keeps project_clone_url.
	if !strings.Contains(finalizeStream(t, f.streams), "project_clone_url") {
		t.Errorf("claude-only finalize should keep project_clone_url (repo re-cloned)")
	}

	// The host staging dir is removed after a successful reset.
	if after := len(stageDirs(t)); after != before {
		t.Errorf("stage dir leaked after success: had %d, now %d", before, after)
	}
}

// TestReset_ProjectOnly: preserving only the project tree stages it out (no
// Claude archive), skips the finalize clone (restored tree is authoritative),
// restores AFTER finalize, and re-approves the .env via direnv.
func TestReset_ProjectOnly(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig()
	cfg.CloneURL = "https://github.com/lullabot/sandbar"

	if err := p.Reset(context.Background(), cfg, ResetOptions{PreserveProject: true}, io.Discard); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	// One project stage-out, no Claude stage-out (.claude is never archived).
	if n := countCalls(f.calls, isTarOut); n != 1 {
		t.Fatalf("project-only reset should stage out exactly once, got %d", n)
	}
	for _, c := range f.calls {
		if hasTok(c, "-czf") && hasTok(c, ".claude") {
			t.Fatalf("project-only reset must not stage out ~/.claude: %v", c)
		}
	}

	// The project restore (-xzf) must land AFTER finalize, followed by direnv allow.
	finalize := findCall(t, f.calls, 0, "finalize", func(c []string) bool {
		return hasTok(c, "bash") && hasTok(c, "-c")
	})
	inProject := findCall(t, f.calls, finalize+1, "stage-in project after finalize", isTarIn)
	findCall(t, f.calls, inProject+1, "direnv allow", func(c []string) bool {
		return hasTok(c, "direnv") && hasTok(c, "allow")
	})

	// Preserving the tree means finalize must NOT re-clone over it.
	if strings.Contains(finalizeStream(t, f.streams), "project_clone_url") {
		t.Errorf("project-only finalize must skip project_clone_url (tree preserved)")
	}
}

// TestReset_PreserveProject_NoOrgURL guards the coordinator-applied fix: when
// PreserveProject is requested but the CloneURL has no org component (nothing to
// stage), the reset stages nothing for the project AND keeps project_clone_url so
// the finalize role re-clones normally instead of silently dropping the repo.
func TestReset_PreserveProject_NoOrgURL(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig()
	cfg.CloneURL = "https://github.com/justrepo" // no org segment => cloneOrgRelDir ok=false

	if err := p.Reset(context.Background(), cfg, ResetOptions{PreserveProject: true}, io.Discard); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	// Nothing was archived (no org dir to stage), and nothing restored.
	if n := countCalls(f.calls, isTarOut); n != 0 {
		t.Fatalf("no-org reset must not stage out anything, got %d tar archives", n)
	}
	if n := countCalls(f.calls, isTarIn); n != 0 {
		t.Fatalf("no-org reset must not stage in anything, got %d extracts", n)
	}
	for _, c := range f.calls {
		if hasTok(c, "direnv") {
			t.Fatalf("no-org reset must not run direnv: %v", c)
		}
	}
	// The repo must still be cloned by finalize (fallback to the role's clone).
	if !strings.Contains(finalizeStream(t, f.streams), "project_clone_url") {
		t.Errorf("no-org PreserveProject must fall back to the role's clone (keep project_clone_url)")
	}
}

// TestReset_StageOutFailureAbortsWithoutDelete is the load-bearing safety
// property: if stage-out fails, the reset must NOT delete the source VM (its data
// is still only inside it), and the error must name the host staging dir so the
// user can recover whatever was written.
func TestReset_StageOutFailureAbortsWithoutDelete(t *testing.T) {
	f := &fakeRunner{
		status:  map[string][]byte{"sandbar-base": []byte("Stopped\n")},
		failOn:  isTarOut, // the stage-out tar fails
		failErr: errors.New("disk full on host"),
	}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig()
	err := p.Reset(context.Background(), cfg, ResetOptions{PreserveClaude: true}, io.Discard)
	if err == nil {
		t.Fatal("Reset should fail when stage-out fails")
	}
	// The VM must not have been deleted — losing data we failed to copy out.
	for _, c := range f.calls {
		if c[0] == "delete" {
			t.Fatalf("stage-out failure must not delete the source VM; calls=%v", f.calls)
		}
	}
	// The error must point the user at the preserved staging dir, and it must exist.
	const marker = "your data is preserved at "
	i := strings.Index(err.Error(), marker)
	if i < 0 {
		t.Fatalf("error must name the recovery dir, got %q", err.Error())
	}
	path := strings.TrimSpace(strings.SplitN(err.Error()[i+len(marker):], ":", 2)[0])
	if st, statErr := os.Stat(path); statErr != nil || !st.IsDir() {
		t.Fatalf("named recovery dir %q should exist: %v", path, statErr)
	}
	_ = os.RemoveAll(path) // this failure path intentionally leaves the dir; clean up the test artifact
}

// TestReset_StartsStoppedSourceForStaging: when a preserve option is set but the
// source VM is stopped, the reset must start it before staging (tar reads from a
// live guest), and that start must precede both the stage-out and the delete.
func TestReset_StartsStoppedSourceForStaging(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{
		"sandbar-base": []byte("Stopped\n"),
		"claude":       []byte("Stopped\n"), // source VM is down
	}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig()
	if err := p.Reset(context.Background(), cfg, ResetOptions{PreserveClaude: true}, io.Discard); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	startSrc := findCall(t, f.calls, 0, "start stopped source", func(c []string) bool {
		return c[0] == "start" && hasTok(c, "claude")
	})
	stageOut := findCall(t, f.calls, 0, "stage-out", isTarOut)
	del := findCall(t, f.calls, 0, "delete", func(c []string) bool { return c[0] == "delete" })
	if !(startSrc < stageOut && startSrc < del) {
		t.Fatalf("source start (idx %d) must precede stage-out (%d) and delete (%d)", startSrc, stageOut, del)
	}
}

// The task-total guard is SHELL, so it is tested by running it in a shell. Reading
// the Go around it is exactly how the bug below survived review.
//
// `grep -c` prints "0" and exits 1 when it matches nothing, and the script's
// `|| true` swallows the exit status — so $listed is the STRING "0": non-empty,
// perfectly numeric, and therefore straight past a guard that only rejected empty
// and non-numeric values. total became 0+1 = 1, the guest announced ONE task, and
// the very first TASK banner rendered the tile's build bar at 100% for the whole
// build. A bar pinned full and lying is strictly worse than the indeterminate 0%
// bar the fallback exists to give.
func TestTaskTotalGuard(t *testing.T) {
	for _, tc := range []struct {
		listed string
		want   string
	}{
		{"72", "SAND_ANSIBLE_TASK_TOTAL=73"},  // the happy path: +1 for gather_facts
		{"0", "SAND_ANSIBLE_TASK_TOTAL=0"},    // grep -c matched nothing — THE BUG
		{"", "SAND_ANSIBLE_TASK_TOTAL=0"},     // no output at all
		{"boom", "SAND_ANSIBLE_TASK_TOTAL=0"}, // not a number
	} {
		t.Run("listed="+tc.listed, func(t *testing.T) {
			out, err := exec.Command("/bin/sh", "-c", "listed='"+tc.listed+"'\n"+taskTotalGuard).CombinedOutput()
			if err != nil {
				t.Fatalf("guard failed to run: %v\n%s", err, out)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Fatalf("listed=%q → %q, want %q", tc.listed, got, tc.want)
			}
		})
	}
}

// baseRaceRunner models the one thing that matters here: `limactl list <base>`
// reports what the base ACTUALLY is at the moment it is asked. It does not exist
// until a base build starts; while that build runs Lima reports it Running (a
// booted guest with Ansible inside it); once the build finishes it is Stopped.
type baseRaceRunner struct {
	mu                 sync.Mutex
	builds             int      // how many times the base image was built from scratch
	reapplies          int      // how many times the existing base was converged in place
	stops              int      // how many times the base image was stopped
	seq                []string // the order of base-affecting calls, for diagnosis
	building           bool
	built              bool
	cloning            int
	stoppedDuringBuild bool
	deletedDuringClone bool
	touchedDuringClone bool
	buildDelay         time.Duration
}

func (r *baseRaceRunner) baseStatus() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.building:
		return "Running" // a base mid-build IS running: Ansible is inside it
	case r.built:
		return "Stopped"
	default:
		return ""
	}
}

// cloningNow reports whether a clone is reading the base's disk right now. A test
// uses it to release a second create at the ONE instant that matters — mid-clone —
// rather than firing both off at once and hoping the dangerous interleaving is the
// one that happens to occur.
func (r *baseRaceRunner) cloningNow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cloning > 0
}

func (r *baseRaceRunner) Output(_ context.Context, args ...string) ([]byte, error) {
	// Delete is a BUFFERED call (Client.run -> Runner.Output), not a streamed one.
	// Recording it in Stream, where it never arrives, is a fake that proves nothing.
	if len(args) > 1 && args[0] == "delete" && args[1] == "sandbar-base" {
		r.mu.Lock()
		if r.cloning > 0 {
			r.deletedDuringClone = true // the clone is reading the disk being deleted
		}
		r.built = false
		r.seq = append(r.seq, "delete-base")
		r.mu.Unlock()
		return nil, nil
	}
	if len(args) > 2 && args[0] == "list" && args[1] == "sandbar-base" {
		s := r.baseStatus()
		if s == "" {
			return nil, errors.New("instance not found")
		}
		return []byte(s + "\n"), nil
	}
	return nil, nil
}

func (r *baseRaceRunner) StreamOut(context.Context, io.Reader, io.Writer, ...string) error {
	return nil
}

// Stream is the one the lifecycle calls go through (Client.runStream).
func (r *baseRaceRunner) Stream(_ context.Context, _ io.Reader, _ io.Writer, args ...string) error {
	isBase := func() bool {
		for _, a := range args {
			if a == "sandbar-base" {
				return true
			}
		}
		return false
	}

	// ANY call that touches the base while another create is cloning from it is a
	// lock failure — the clone is reading that disk. `clone` itself names the base
	// (clone sandbar-base web), so it is not one of them.
	if args[0] != "clone" && isBase() {
		r.mu.Lock()
		if r.cloning > 0 {
			r.touchedDuringClone = true
		}
		r.mu.Unlock()
	}

	switch {
	case args[0] == "start" && len(args) > 2 && args[1] == "--name" && args[2] == "sandbar-base":
		r.mu.Lock()
		r.builds++
		r.building = true
		r.seq = append(r.seq, "build-base")
		r.mu.Unlock()

		time.Sleep(r.buildDelay) // the base build is slow; that is the whole window

		r.mu.Lock()
		r.building = false
		r.built = true
		r.mu.Unlock()
	case args[0] == "start" && len(args) == 2 && args[1] == "sandbar-base":
		// The in-place re-apply: the existing base is started and the base playbook
		// is re-run against it. Slow, like the build — and, like the build, it must
		// hold the lock for the whole of it.
		r.mu.Lock()
		r.reapplies++
		r.seq = append(r.seq, "reapply-base")
		r.mu.Unlock()

		time.Sleep(r.buildDelay)
	case args[0] == "clone":
		r.mu.Lock()
		r.cloning++
		r.seq = append(r.seq, "clone-from-base")
		r.mu.Unlock()

		time.Sleep(r.buildDelay) // a clone reads the base's disk for a long time

		r.mu.Lock()
		r.cloning--
		r.mu.Unlock()
	case args[0] == "stop" && isBase():
		r.mu.Lock()
		r.stops++
		if r.building {
			// A stop landing while the base is MID-BUILD kills that build. BuildBase's
			// own closing stop does not count: by then it has set building=false.
			r.stoppedDuringBuild = true
		}
		r.seq = append(r.seq, "stop-base")
		r.mu.Unlock()
	}
	return nil
}

// TWO CREATES AT ONCE MUST NOT BOTH BUILD THE BASE IMAGE — and, worse, the second
// must not STOP the base out from under the first one's build.
//
// ensureBaseStopped reads the base's status and then acts on it, with nothing
// between the two. Two provisions running concurrently (which the board exists to
// allow) both see the base missing and both build it, under the same instance name;
// and once one of them is building, Lima reports that base as Running — so the other
// falls into the "not Stopped" branch and stops it, killing the build it is waiting
// on. The board made this reachable: before it, a build froze the keyboard and there
// could only ever be one.
func TestConcurrentCreatesBuildTheBaseOnce(t *testing.T) {
	r := &baseRaceRunner{buildDelay: 150 * time.Millisecond}
	p := &Provisioner{Lima: lima.New(r), PlaybookDir: t.TempDir()}

	// Neither the version stamp nor the playbook matters here; pin them so the
	// stale-base path does not fire. Also pin every base to freshly built, so the
	// unrelated age-refresh check does not fire either.
	origVer, origRead, origWrite := playbookVersionFn, readBaseVersionFn, writeBaseVersionFn
	playbookVersionFn = func(string, string) (string, error) { return "v1", nil }
	readBaseVersionFn = func(lima.HostFiles, string) string { return "v1" }
	writeBaseVersionFn = func(lima.HostFiles, string, string, time.Time) error { return nil }
	t.Cleanup(func() {
		playbookVersionFn, readBaseVersionFn, writeBaseVersionFn = origVer, origRead, origWrite
	})
	stubFreshBuiltAt(t)

	names := []string{"web", "api"}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = p.prepareBaseAndClone(context.Background(), vm.CreateConfig{Name: names[i], BaseName: "sandbar-base"}, CreateOptions{}, io.Discard, newPhaseTimer(io.Discard))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("create %d failed: %v", i, err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.builds != 1 {
		t.Errorf("the base image was built %d times, want exactly 1 (calls: %v)", r.builds, r.seq)
	}
	if r.stoppedDuringBuild {
		t.Errorf("the base image was stopped WHILE IT WAS BEING BUILT — the second create killed the first one's base build (calls: %v)", r.seq)
	}
	if r.deletedDuringClone {
		t.Errorf("the base image was DELETED while another create was cloning from it — the lock did not cover the clone (calls: %v)", r.seq)
	}
}

// TWO CONCURRENT CREATES MUST CONVERGE A STALE BASE ONCE, NOT TWICE — and the
// second one must not re-run a playbook the first one has just finished applying.
//
// This is the double-checked-locking discipline ensureBaseStopped exists to
// enforce: the base's status AND its version stamp are read AFTER the lock is
// acquired, never cached before it. A create that blocked for minutes behind
// another's re-apply re-reads the stamp the moment it gets in, finds the base
// current, and clones straight away. Hoist those reads out of the locked section —
// read staleness before you queue — and the waiter re-applies an already-converged
// base, doubling the cost of every concurrent create.
func TestConcurrentCreatesReapplyTheStaleBaseOnce(t *testing.T) {
	r := &baseRaceRunner{buildDelay: 150 * time.Millisecond, built: true} // the base already exists
	p := &Provisioner{Lima: lima.New(r), PlaybookDir: t.TempDir()}
	stubConvergeableBase(t, p.PlaybookDir) // …created by this build of sand, from this playbook

	// The base is stamped v1; the playbook is now v2. It is stale for BOTH creates
	// at the moment they start — only the stamp the winner writes can tell the
	// loser it no longer has work to do.
	var vmu sync.Mutex
	stamped := "v1"
	origVer, origRead, origWrite := playbookVersionFn, readBaseVersionFn, writeBaseVersionFn
	playbookVersionFn = func(string, string) (string, error) { return "v2", nil }
	readBaseVersionFn = func(lima.HostFiles, string) string {
		vmu.Lock()
		defer vmu.Unlock()
		return stamped
	}
	writeBaseVersionFn = func(_ lima.HostFiles, _, v string, _ time.Time) error {
		vmu.Lock()
		stamped = v
		vmu.Unlock()
		return nil
	}
	t.Cleanup(func() {
		playbookVersionFn, readBaseVersionFn, writeBaseVersionFn = origVer, origRead, origWrite
	})
	// The version dimension is what this test exercises; keep every base
	// reporting a fresh build time so the unrelated age-refresh check cannot
	// also fire and inflate the reapply count this test asserts on.
	stubFreshBuiltAt(t)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	names := []string{"web", "api"}
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = p.prepareBaseAndClone(context.Background(),
				vm.CreateConfig{Name: names[i], BaseName: "sandbar-base"}, CreateOptions{}, io.Discard, newPhaseTimer(io.Discard))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("create %d failed: %v", i, err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reapplies != 1 {
		t.Errorf("the stale base was re-applied %d times, want exactly 1 — the waiter must re-read the stamp AFTER the lock, not act on one it cached before it (calls: %v)", r.reapplies, r.seq)
	}
	if r.builds != 0 {
		t.Errorf("the base was rebuilt from scratch %d times; a stale base is converged in place (calls: %v)", r.builds, r.seq)
	}
	if r.touchedDuringClone {
		t.Errorf("the base was started/provisioned/stopped while another create was cloning from it (calls: %v)", r.seq)
	}
}

// A --rebuild MUST NOT DESTROY A BASE ANOTHER CREATE IS CLONING FROM.
//
// This is the race this task closes. `sand create --rebuild` deleted the base in
// the CLI layer, before the provisioner and therefore before the base lock was
// ever taken, while a concurrent create held that lock and spent 40-60s cloning
// the very disk being deleted. Neither create is doing anything wrong; the destroy
// was simply outside the critical section. Now it happens inside
// ensureBaseStopped, under the lock, so it can only ever run when no clone is in
// flight.
func TestARebuildCannotDestroyTheBaseAnotherCreateIsCloning(t *testing.T) {
	r := &baseRaceRunner{buildDelay: 200 * time.Millisecond, built: true} // an existing, up-to-date base
	p := &Provisioner{Lima: lima.New(r), PlaybookDir: t.TempDir()}
	stubConvergeableBase(t, p.PlaybookDir)

	// The base is CURRENT: nothing but --rebuild can delete it, so any delete this
	// test sees is the rebuild's. Also pin it to freshly built so the unrelated
	// age-refresh check does not fire and start/shell/stop it before the clone.
	origVer, origRead, origWrite := playbookVersionFn, readBaseVersionFn, writeBaseVersionFn
	playbookVersionFn = func(string, string) (string, error) { return "v1", nil }
	readBaseVersionFn = func(lima.HostFiles, string) string { return "v1" }
	writeBaseVersionFn = func(lima.HostFiles, string, string, time.Time) error { return nil }
	t.Cleanup(func() {
		playbookVersionFn, readBaseVersionFn, writeBaseVersionFn = origVer, origRead, origWrite
	})
	stubFreshBuiltAt(t)

	var wg sync.WaitGroup
	errs := make([]error, 2)

	// The first create takes the lock and starts its (slow) clone.
	wg.Add(1)
	go func() {
		defer wg.Done()
		errs[0] = p.prepareBaseAndClone(context.Background(),
			vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}, CreateOptions{}, io.Discard, newPhaseTimer(io.Discard))
	}()

	// The rebuild arrives at the ONE moment that is fatal: while that clone is
	// reading the base's disk. Firing both goroutines off together would leave it to
	// chance whether the destroy lands inside the clone at all — and a race test
	// that only sometimes reaches the race is a test that only sometimes fails.
	waitFor(t, 5*time.Second, r.cloningNow, "the first create to start cloning from the base")

	wg.Add(1)
	go func() {
		defer wg.Done()
		errs[1] = p.prepareBaseAndClone(context.Background(),
			vm.CreateConfig{Name: "api", BaseName: "sandbar-base"}, CreateOptions{Rebuild: true}, io.Discard, newPhaseTimer(io.Discard))
	}()
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("create %d failed: %v", i, err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deletedDuringClone {
		t.Fatalf("--rebuild DELETED the base while another create was cloning from it (calls: %v)", r.seq)
	}
	if r.touchedDuringClone {
		t.Fatalf("--rebuild rebuilt the base while another create was cloning from it (calls: %v)", r.seq)
	}
	if r.builds != 1 {
		t.Errorf("the base was built %d times, want exactly 1 (the rebuild's) (calls: %v)", r.builds, r.seq)
	}
}

// THE CLONE MUST BE INSIDE THE BASE LOCK, not just the decision that precedes it.
//
// The lock was taken and released inside ensureBaseStopped, so it guarded the choice
// to build or rebuild the base and then let go — while the 40-60s clone that READS
// that base ran unprotected. That leaves the stale-base path free to delete the base
// out from under a clone, and the version it compares against is not a constant:
// playbookVersionFn is a content hash of the playbook fileset, so
// editing any playbook file while a create is cloning FLIPS IT AT RUNTIME.
//
// So: create A builds the base and starts its (slow) clone. The playbook changes.
// Create B arrives, finds the base stale, and force-deletes the instance A is reading
// its disk from. A's clone fails, or lands truncated.
//
// The stale base here takes the REBUILD branch, not the in-place re-apply: the mount
// is left unstubbed, so basePlaybookMount finds no instance file for it under the
// test's LIMA_HOME and the base cannot be proved to mount this playbook (basemount.go).
// That is deliberate — the branch that DELETES is the one this test is about.
func TestASecondCreateCannotDeleteTheBaseWhileTheFirstIsCloning(t *testing.T) {
	r := &baseRaceRunner{buildDelay: 200 * time.Millisecond}
	p := &Provisioner{Lima: lima.New(r), PlaybookDir: t.TempDir()}

	// The playbook version FLIPS once the first build has stamped it — exactly what an
	// edit to the playbook checkout does mid-create.
	var vmu sync.Mutex
	version := "v1"
	stamped := ""
	origVer, origRead, origWrite := playbookVersionFn, readBaseVersionFn, writeBaseVersionFn
	playbookVersionFn = func(string, string) (string, error) {
		vmu.Lock()
		defer vmu.Unlock()
		return version, nil
	}
	readBaseVersionFn = func(lima.HostFiles, string) string {
		vmu.Lock()
		defer vmu.Unlock()
		return stamped
	}
	writeBaseVersionFn = func(_ lima.HostFiles, _, v string, _ time.Time) error {
		vmu.Lock()
		stamped = v
		version = "v2" // the tree changes the moment the base is built
		vmu.Unlock()
		return nil
	}
	t.Cleanup(func() {
		playbookVersionFn, readBaseVersionFn, writeBaseVersionFn = origVer, origRead, origWrite
	})
	stubFreshBuiltAt(t)

	var wg sync.WaitGroup
	for _, name := range []string{"web", "api"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_ = p.prepareBaseAndClone(context.Background(),
				vm.CreateConfig{Name: name, BaseName: "sandbar-base"}, CreateOptions{}, io.Discard, newPhaseTimer(io.Discard))
		}(name)
	}
	wg.Wait()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deletedDuringClone {
		t.Fatalf("the base was DELETED while another create was cloning from it — the lock did not cover the clone (calls: %v)", r.seq)
	}
}

// TestReapplyBase_ContentOnlyReapplyPreservesTheAptClock pins the distinction
// between the two things the stamp records: WHAT the base contains (the version)
// and WHEN its packages were last known current (BuiltAt, the clock
// baseNeedsRefresh measures the 30-day apt-upgrade age against).
//
// A playbook edit converges content and upgrades nothing, so it must carry the
// prior BuiltAt forward. Stamping "now" for it silently restarts the apt clock —
// and since a developer (or CI) touches the playbook far more often than every 30
// days, the refresh would never fire. With finalize's apt upgrade gone, that means
// no VM ever receives a security update again. This is the guard for that.
func TestReapplyBase_ContentOnlyReapplyPreservesTheAptClock(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	var gotBuiltAt time.Time
	origVer, origRead, origWrite := playbookVersionFn, readBaseVersionFn, writeBaseVersionFn
	playbookVersionFn = func(string, string) (string, error) { return "v2:newsha:ddev+go+java", nil }
	readBaseVersionFn = func(lima.HostFiles, string) string { return "v2:oldsha:ddev+go+java" } // stale CONTENT
	writeBaseVersionFn = func(_ lima.HostFiles, _, _ string, builtAt time.Time) error { gotBuiltAt = builtAt; return nil }
	t.Cleanup(func() {
		playbookVersionFn, readBaseVersionFn, writeBaseVersionFn = origVer, origRead, origWrite
	})

	// The base's packages were last refreshed 10 days ago: stale content, but the
	// apt clock has NOT run out (baseMaxAge is 30 days), so this re-apply upgrades
	// nothing and must leave the clock alone.
	tenDaysAgo := time.Now().Add(-10 * 24 * time.Hour)
	origBuiltAt := readBaseBuiltAtFn
	readBaseBuiltAtFn = func(lima.HostFiles, string) (time.Time, bool) { return tenDaysAgo, true }
	t.Cleanup(func() { readBaseBuiltAtFn = origBuiltAt })
	stubConvergeableBase(t, "/playbook")

	if err := p.CreateVM(context.Background(), testConfig(), io.Discard); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	if !gotBuiltAt.Equal(tenDaysAgo) {
		t.Errorf("a content-only re-apply stamped BuiltAt %v, want the prior %v carried forward.\n"+
			"Restarting the clock here means the 30-day apt refresh never fires on an "+
			"actively-edited playbook, and no VM ever gets a security update.", gotBuiltAt, tenDaysAgo)
	}
}

// A ^C (or a failure) during the CLONE leaves a half-written instance directory,
// and that is not just untidy: a directory with a disk but no lima.yaml makes
// every later `limactl list` FATAL — the board cannot render, and sand is wedged
// by a VM that was never created. The run that made the mess must clear it.
//
// The nastiest half of this is the fallback: limactl cannot DELETE an instance it
// cannot LOAD, so the very directory that wedges the tool is the one `limactl
// delete` refuses to touch. Here limactl's delete fails (as it does on a dir with
// no lima.yaml) and the directory must still go.
func TestCreateVM_CanceledCloneRemovesTheHalfWrittenInstanceDir(t *testing.T) {
	limaHomeDir := t.TempDir()
	t.Setenv("LIMA_HOME", limaHomeDir)

	// The half-written instance: a disk, a cidata.iso, and no lima.yaml — exactly
	// what a killed `limactl clone` leaves behind.
	dir := filepath.Join(limaHomeDir, testConfig().Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("seed instance dir: %v", err)
	}
	for _, f := range []string{"disk", "cidata.iso"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", f, err)
		}
	}

	f := &fakeRunner{
		status: map[string][]byte{"sandbar-base": []byte("Stopped\n")},
		// The clone dies (the ^C), and delete fails the way it does on a dir limactl
		// cannot load — so only the directory removal can save us.
		failOn: func(args []string) bool {
			return len(args) > 0 && (args[0] == "clone" || args[0] == "delete")
		},
		failErr: errors.New("context canceled"),
	}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: t.TempDir()}
	stubBaseVersion(t, "v2:same:claude+ddev+go+java", nil, map[string]string{"sandbar-base": "v2:same:claude+ddev+go+java"})

	err := p.CreateVM(context.Background(), testConfig(), io.Discard)
	if err == nil {
		t.Fatal("a failed clone must return an error")
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Errorf("the half-written instance dir %s survived a failed clone — every later `limactl list` will be fatal", dir)
	}
}

// …but a VM whose PLAYBOOK failed is KEPT. It booted, its lima.yaml is valid,
// `limactl list` is happy with it, and its retained log is the whole point of a
// failed run: deleting it would throw away the evidence the user needs.
func TestCreateVM_FailedPlaybookKeepsTheVM(t *testing.T) {
	limaHomeDir := t.TempDir()
	t.Setenv("LIMA_HOME", limaHomeDir)

	dir := filepath.Join(limaHomeDir, testConfig().Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("seed instance dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lima.yaml"), []byte("cpus: 2\n"), 0o644); err != nil {
		t.Fatalf("seed lima.yaml: %v", err)
	}

	f := &fakeRunner{
		status: map[string][]byte{"sandbar-base": []byte("Stopped\n")},
		// The finalize playbook fails — it runs over `limactl shell`.
		failOn: func(args []string) bool {
			return len(args) > 0 && args[0] == "shell"
		},
		failErr: errors.New("ansible: task failed"),
	}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: t.TempDir()}
	stubBaseVersion(t, "v2:same:claude+ddev+go+java", nil, map[string]string{"sandbar-base": "v2:same:claude+ddev+go+java"})

	if err := p.CreateVM(context.Background(), testConfig(), io.Discard); err == nil {
		t.Fatal("a failed playbook must return an error")
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Errorf("the VM was removed after its playbook failed: %v — a booted VM with a valid lima.yaml is inspectable, and its log is the point of keeping a failed run", statErr)
	}
}

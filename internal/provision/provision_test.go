package provision

import (
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
type fakeRunner struct {
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

func (f *fakeRunner) Output(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
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

func (f *fakeRunner) Stream(_ context.Context, stdin io.Reader, out io.Writer, args ...string) error {
	f.calls = append(f.calls, args)
	if stdin != nil {
		data, _ := io.ReadAll(stdin)
		f.streams = append(f.streams, string(data))
	}
	return f.callErr(args)
}

func (f *fakeRunner) StreamOut(ctx context.Context, stdin io.Reader, out io.Writer, args ...string) error {
	// StageOut now streams through StreamOut; mirror Stream so the reset tests'
	// recorded call sequence and staged-stdin capture are unchanged.
	return f.Stream(ctx, stdin, out, args...)
}

func testConfig() vm.CreateConfig {
	cfg := vm.DefaultCreateConfig() // Name=claude, BaseName=claude-base
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
	f := &fakeRunner{status: map[string][]byte{"claude-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	if err := p.CreateVM(context.Background(), testConfig(), io.Discard); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	want := [][]string{
		{"list", "claude", "--format", "{{.Status}}"},                            // exists-guard: target absent
		{"list", "claude-base", "--format", "{{.Status}}"},                       // Status(base) -> Stopped
		{"clone", "claude-base", "claude"},                                       // Clone
		{"edit", "--set", `.cpus=4 | .memory="8GiB" | .disk="100GiB"`, "claude"}, // Configure clone sizes
		{"start", "claude"}, // Start
		{"shell", "claude", "sudo", "bash", "-c", inGuestScript}, // finalize provision
		{"stop", "claude"},  // bounce: stop
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
	f := &fakeRunner{status: map[string][]byte{"claude-base": []byte("Stopped\n")}}
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
	f := &fakeRunner{status: map[string][]byte{"claude-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	if err := p.CreateVM(context.Background(), testConfig(), io.Discard); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	for _, call := range f.calls {
		if len(call) > 0 && call[0] == "shell" {
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
		{"list", "claude"},       // exists-guard: target absent
		{"list", "claude-base"},  // Status(base) -> absent
		{"start", "--name"},      // BuildBase: Create(base)
		{"shell", "claude-base"}, // BuildBase: base provision
		{"stop", "claude-base"},  // BuildBase: stop base
		{"clone", "claude-base"}, // Clone
		{"edit", "--set"},        // Configure clone sizes
		{"start", "claude"},      // Start clone
		{"shell", "claude"},      // finalize provision
		{"stop", "claude"},       // bounce: stop
		{"start", "claude"},      // bounce: start
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
	playbookVersionFn = func(string) (string, error) { return current, currentErr }
	readBaseVersionFn = func(base string) string { return stamped[base] }
	writeBaseVersionFn = func(base, v string) error { written[base] = v; return nil }
	t.Cleanup(func() {
		playbookVersionFn, readBaseVersionFn, writeBaseVersionFn = origVer, origRead, origWrite
	})
	return written
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

// TestCreateVM_StaleBaseRebuilds: an existing base whose recorded playbook
// version differs from the current one is force-deleted and rebuilt before the
// clone, and the rebuilt base is stamped with the new version.
func TestCreateVM_StaleBaseRebuilds(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"claude-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
	written := stubBaseVersion(t, "newsha", nil, map[string]string{"claude-base": "oldsha"})

	if err := p.CreateVM(context.Background(), testConfig(), io.Discard); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	type call = struct{ first, second string }
	want := []call{
		{"list", "claude"},        // exists-guard: target absent
		{"list", "claude-base"},   // Status(base) -> Stopped
		{"delete", "claude-base"}, // stale base force-deleted
		{"start", "--name"},       // BuildBase: Create(base)
		{"shell", "claude-base"},  // BuildBase: base provision
		{"stop", "claude-base"},   // BuildBase: stop base
		{"clone", "claude-base"},  // Clone
		{"edit", "--set"},         // Configure clone sizes
		{"start", "claude"},       // Start clone
		{"shell", "claude"},       // finalize provision
		{"stop", "claude"},        // bounce: stop
		{"start", "claude"},       // bounce: start
	}
	if got := firstSecond(f.calls); !reflect.DeepEqual(got, want) {
		t.Fatalf("stale-base rebuild sequence mismatch:\n got %v\nwant %v", got, want)
	}
	// The force flag must be set when deleting the stale base.
	for _, c := range f.calls {
		if c[0] == "delete" && (len(c) < 3 || c[2] != "-f") {
			t.Errorf("stale base delete missing -f: %v", c)
		}
	}
	if written["claude-base"] != "newsha" {
		t.Errorf("rebuilt base stamped %q, want newsha", written["claude-base"])
	}
}

// TestCreateVM_FreshBaseReused: when the base's stamp matches the current
// playbook version, the base is reused as-is — no delete, no rebuild.
func TestCreateVM_FreshBaseReused(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"claude-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
	stubBaseVersion(t, "samesha", nil, map[string]string{"claude-base": "samesha"})

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
	}
}

// TestCreateVM_UnstampedBaseRebuilds: a base with no recorded version (built
// before stamping, or by an unknown path) is treated as stale and rebuilt.
func TestCreateVM_UnstampedBaseRebuilds(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"claude-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
	stubBaseVersion(t, "anysha", nil, map[string]string{}) // no stamp

	if err := p.CreateVM(context.Background(), testConfig(), io.Discard); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	var deleted, rebuilt bool
	for _, c := range f.calls {
		if c[0] == "delete" && len(c) > 1 && c[1] == "claude-base" {
			deleted = true
		}
		if c[0] == "start" && len(c) > 1 && c[1] == "--name" {
			rebuilt = true
		}
	}
	if !deleted || !rebuilt {
		t.Fatalf("unstamped base should be deleted+rebuilt (deleted=%v rebuilt=%v): %v", deleted, rebuilt, f.calls)
	}
}

// TestCreateVM_VersionErrorReusesBase: when the playbook version can't be
// determined (e.g. not a git checkout), the existing base is reused rather than
// rebuilt on every create.
func TestCreateVM_VersionErrorReusesBase(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"claude-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
	stubBaseVersion(t, "", errors.New("not a git checkout"), map[string]string{"claude-base": "oldsha"})

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
	f := &fakeRunner{status: map[string][]byte{"claude-base": []byte("Stopped\n")}}
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
	if got, want := f.calls[2], []string{"clone", "claude-base", "claude"}; !reflect.DeepEqual(got, want) {
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
	f := &fakeRunner{status: map[string][]byte{"claude-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig()
	cfg.CloneURL = "https://github.com/lullabot/sandbar"

	if err := p.Reset(context.Background(), cfg, ResetOptions{}, io.Discard); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	want := [][]string{
		{"delete", "claude", "-f"},                                               // destroy
		{"list", "claude-base", "--format", "{{.Status}}"},                       // ensureBaseStopped
		{"clone", "claude-base", "claude"},                                       // re-clone
		{"edit", "--set", `.cpus=4 | .memory="8GiB" | .disk="100GiB"`, "claude"}, // configure size
		{"start", "claude"},                                                      // start clone
		{"shell", "claude", "sudo", "bash", "-c", inGuestScript},                 // finalize
		{"stop", "claude"},                                                       // bounce: stop
		{"start", "claude"},                                                      // bounce: start
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

// TestReset_BothPreserve: with both preserves on, the reset stages out the
// Claude login and the project tree, recreates the VM, restores Claude BEFORE
// finalize, finalizes WITHOUT project_clone_url (so the role skips its clone over
// the restored tree), restores the project AFTER finalize, re-approves its .env
// via direnv, then bounces. Assertions focus on ordering and the clone-skip.
func TestReset_BothPreserve(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"claude-base": []byte("Stopped\n")}}
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
	f := &fakeRunner{status: map[string][]byte{"claude-base": []byte("Stopped\n")}}
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
	f := &fakeRunner{status: map[string][]byte{"claude-base": []byte("Stopped\n")}}
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
	f := &fakeRunner{status: map[string][]byte{"claude-base": []byte("Stopped\n")}}
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
		status:  map[string][]byte{"claude-base": []byte("Stopped\n")},
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
		"claude-base": []byte("Stopped\n"),
		"claude":      []byte("Stopped\n"), // source VM is down
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
	builds             int      // how many times the base image was built
	stops              int      // how many times the base image was stopped
	seq                []string // the order of base-affecting calls, for diagnosis
	building           bool
	built              bool
	cloning            int
	stoppedDuringBuild bool
	deletedDuringClone bool
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

func (r *baseRaceRunner) Output(_ context.Context, args ...string) ([]byte, error) {
	// Delete is a BUFFERED call (Client.run -> Runner.Output), not a streamed one.
	// Recording it in Stream, where it never arrives, is a fake that proves nothing.
	if len(args) > 1 && args[0] == "delete" && args[1] == "claude-base" {
		r.mu.Lock()
		if r.cloning > 0 {
			r.deletedDuringClone = true // the clone is reading the disk being deleted
		}
		r.built = false
		r.seq = append(r.seq, "delete-base")
		r.mu.Unlock()
		return nil, nil
	}
	if len(args) > 2 && args[0] == "list" && args[1] == "claude-base" {
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
			if a == "claude-base" {
				return true
			}
		}
		return false
	}
	switch {
	case args[0] == "start" && len(args) > 2 && args[1] == "--name" && args[2] == "claude-base":
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
	// stale-base path does not fire.
	origVer, origRead, origWrite := playbookVersionFn, readBaseVersionFn, writeBaseVersionFn
	playbookVersionFn = func(string) (string, error) { return "v1", nil }
	readBaseVersionFn = func(string) string { return "v1" }
	writeBaseVersionFn = func(string, string) error { return nil }
	t.Cleanup(func() {
		playbookVersionFn, readBaseVersionFn, writeBaseVersionFn = origVer, origRead, origWrite
	})

	names := []string{"web", "api"}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = p.prepareBaseAndClone(context.Background(), vm.CreateConfig{Name: names[i], BaseName: "claude-base"}, io.Discard, newPhaseTimer(io.Discard))
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
func TestASecondCreateCannotDeleteTheBaseWhileTheFirstIsCloning(t *testing.T) {
	r := &baseRaceRunner{buildDelay: 200 * time.Millisecond}
	p := &Provisioner{Lima: lima.New(r), PlaybookDir: t.TempDir()}

	// The playbook version FLIPS once the first build has stamped it — exactly what an
	// edit to the playbook checkout does mid-create.
	var vmu sync.Mutex
	version := "v1"
	stamped := ""
	origVer, origRead, origWrite := playbookVersionFn, readBaseVersionFn, writeBaseVersionFn
	playbookVersionFn = func(string) (string, error) {
		vmu.Lock()
		defer vmu.Unlock()
		return version, nil
	}
	readBaseVersionFn = func(string) string {
		vmu.Lock()
		defer vmu.Unlock()
		return stamped
	}
	writeBaseVersionFn = func(_, v string) error {
		vmu.Lock()
		stamped = v
		version = "v2" // the tree changes the moment the base is built
		vmu.Unlock()
		return nil
	}
	t.Cleanup(func() {
		playbookVersionFn, readBaseVersionFn, writeBaseVersionFn = origVer, origRead, origWrite
	})

	var wg sync.WaitGroup
	for _, name := range []string{"web", "api"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_ = p.prepareBaseAndClone(context.Background(),
				vm.CreateConfig{Name: name, BaseName: "claude-base"}, io.Discard, newPhaseTimer(io.Discard))
		}(name)
	}
	wg.Wait()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deletedDuringClone {
		t.Fatalf("the base was DELETED while another create was cloning from it — the lock did not cover the clone (calls: %v)", r.seq)
	}
}

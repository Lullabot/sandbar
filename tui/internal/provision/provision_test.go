package provision

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/deviantintegral/claude-code-ansible/tui/internal/lima"
	"github.com/deviantintegral/claude-code-ansible/tui/internal/vm"
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
}

func (f *fakeRunner) Output(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	// Status queries look like `list <name> --format {{.Status}}`. Return the
	// per-instance canned status; an unset instance reads as absent (empty).
	if len(args) >= 2 && args[0] == "list" && args[1] != "--format" {
		return f.status[args[1]], f.err
	}
	return f.outputs[args[0]], f.err
}

func (f *fakeRunner) Stream(_ context.Context, stdin io.Reader, out io.Writer, args ...string) error {
	f.calls = append(f.calls, args)
	if stdin != nil {
		data, _ := io.ReadAll(stdin)
		f.streams = append(f.streams, string(data))
	}
	// guestHome reads the home dir from stdout of `shell <name> getent passwd <user>`
	// (fields user:x:uid:gid:gecos:home:shell); emit a canned passwd line so the
	// staging path resolves to /home/andrew in the reset tests.
	if out != nil {
		for _, a := range args {
			if a == "getent" {
				_, _ = io.WriteString(out, "andrew:x:1000:1000::/home/andrew:/bin/bash\n")
				break
			}
		}
	}
	return f.err
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
	cfg.CloneURL = "https://github.com/deviantintegral/claude-code-ansible"

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
	cfg.CloneURL = "https://github.com/deviantintegral/claude-code-ansible"
	opts := ResetOptions{PreserveClaude: true, PreserveProject: true}

	if err := p.Reset(context.Background(), cfg, opts, io.Discard); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	calls := f.calls

	// Stage-out: getent resolves home, then two `tar -czf -` archives (claude,
	// then project under github.com/deviantintegral).
	getent := findCall(t, calls, 0, "getent", func(c []string) bool { return hasTok(c, "getent") })
	outClaude := findCall(t, calls, getent+1, "stage-out claude (-czf .claude)", func(c []string) bool {
		return hasTok(c, "-czf") && hasTok(c, ".claude")
	})
	outProject := findCall(t, calls, outClaude+1, "stage-out project (-czf github.com/deviantintegral)", func(c []string) bool {
		return hasTok(c, "-czf") && hasTok(c, "github.com/deviantintegral")
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
		return hasTok(c, "chown") && hasTok(c, "/home/andrew/github.com/deviantintegral")
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

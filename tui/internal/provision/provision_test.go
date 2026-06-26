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
	streams []string // stdin contents captured from Stream calls, in order
	outputs map[string][]byte
	err     error
}

func (f *fakeRunner) Output(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	return f.outputs[args[0]], f.err
}

func (f *fakeRunner) Stream(_ context.Context, stdin io.Reader, _ io.Writer, args ...string) error {
	f.calls = append(f.calls, args)
	if stdin != nil {
		data, _ := io.ReadAll(stdin)
		f.streams = append(f.streams, string(data))
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
	f := &fakeRunner{outputs: map[string][]byte{"list": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	if err := p.CreateVM(context.Background(), testConfig(), io.Discard); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	want := [][]string{
		{"list", "claude-base", "--format", "{{.Status}}"},       // Status(base) -> Stopped
		{"clone", "claude-base", "claude"},                       // Clone
		{"start", "claude"},                                      // Start
		{"shell", "claude", "sudo", "bash", "-c", inGuestScript}, // finalize provision
		{"stop", "claude"},                                       // bounce: stop
		{"start", "claude"},                                      // bounce: start
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
	f := &fakeRunner{outputs: map[string][]byte{}} // list -> "" => base absent
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
		{"list", "claude-base"},  // Status(base) -> absent
		{"start", "--name"},      // BuildBase: Create(base)
		{"shell", "claude-base"}, // BuildBase: base provision
		{"stop", "claude-base"},  // BuildBase: stop base
		{"clone", "claude-base"}, // Clone
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

// TestRecreate deletes (force) then runs the full CreateVM sequence.
func TestRecreate(t *testing.T) {
	f := &fakeRunner{outputs: map[string][]byte{"list": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	if err := p.Recreate(context.Background(), testConfig(), io.Discard); err != nil {
		t.Fatalf("Recreate: %v", err)
	}

	if len(f.calls) == 0 {
		t.Fatal("Recreate made no calls")
	}
	if got, want := f.calls[0], []string{"delete", "claude", "-f"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Recreate first call = %v, want %v", got, want)
	}
	// Followed by the CreateVM sequence (clone is the first lifecycle op after the
	// stopped-base status check).
	if got, want := f.calls[2], []string{"clone", "claude-base", "claude"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Recreate did not proceed to clone: calls=%v", f.calls)
	}
}

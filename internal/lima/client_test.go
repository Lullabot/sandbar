package lima

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// fakeRunner records the argv of every call and returns canned bytes/errors so
// tests never spawn a real limactl. Outputs are keyed by the first argument.
type fakeRunner struct {
	calls   [][]string
	outputs map[string][]byte
	err     error
}

func (f *fakeRunner) Output(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	return f.outputs[args[0]], f.err
}

func (f *fakeRunner) Stream(_ context.Context, _ io.Reader, _ io.Writer, args ...string) error {
	f.calls = append(f.calls, args)
	return f.err
}

func (f *fakeRunner) StreamOut(_ context.Context, _ io.Reader, _ io.Writer, args ...string) error {
	f.calls = append(f.calls, args)
	return f.err
}

func TestListParsesFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "list.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	f := &fakeRunner{outputs: map[string][]byte{"list": data}}
	c := New(f)

	vms, err := c.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}

	// The List call must use the JSON format, not a Go template.
	if got, want := f.calls[0], []string{"list", "--format", "json"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("List argv = %v, want %v", got, want)
	}

	if len(vms) != 2 {
		t.Fatalf("got %d VMs, want 2", len(vms))
	}

	want := []struct {
		Name   string
		Status string
		CPUs   int
	}{
		{Name: "claude", Status: "Running", CPUs: 4},
		{Name: "claude-base", Status: "Stopped", CPUs: 2},
	}
	for i, w := range want {
		if vms[i].Name != w.Name || vms[i].Status != w.Status || vms[i].CPUs != w.CPUs {
			t.Errorf("vm[%d] = {%q %q %d}, want {%q %q %d}",
				i, vms[i].Name, vms[i].Status, vms[i].CPUs, w.Name, w.Status, w.CPUs)
		}
	}

	// Byte-valued fields are carried through as their raw string form.
	if vms[0].Memory != "8589934592" {
		t.Errorf("vm[0].Memory = %q, want %q", vms[0].Memory, "8589934592")
	}
	if vms[0].Arch != "aarch64" {
		t.Errorf("vm[0].Arch = %q, want %q", vms[0].Arch, "aarch64")
	}
}

// TestListIgnoresNonJSONLines guards the parser hardening: a stray non-JSON line
// on stdout (a leaked log line, a deprecation notice) must be skipped rather than
// failing the whole listing.
func TestListIgnoresNonJSONLines(t *testing.T) {
	noisy := []byte(`time="2026-06-26T00:00:00Z" level=info msg="loading config"
{"name":"claude","status":"Running","cpus":2,"memory":42,"disk":99}

some trailing warning
`)
	f := &fakeRunner{outputs: map[string][]byte{"list": noisy}}

	vms, err := New(f).List()
	if err != nil {
		t.Fatalf("List() should skip non-JSON noise, got error: %v", err)
	}
	if len(vms) != 1 || vms[0].Name != "claude" {
		t.Fatalf("got %+v, want a single VM named claude", vms)
	}
}

// TestExecRunnerSeparatesStderr is the regression test for the original bug
// report: limactl logs to stderr ("time=… level=… msg=…"), and merging that into
// stdout corrupted the JSON the List parser consumed. We stand up a tiny stub
// that mimics limactl — a logrus line on stderr, the JSON object on stdout — and
// assert the real execRunner returns clean stdout so List parses it.
func TestExecRunnerSeparatesStderr(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "limactl-stub.sh")
	script := `#!/bin/sh
echo 'time="2026-06-26T00:00:00Z" level=info msg="something on stderr"' >&2
echo '{"name":"claude","status":"Running","cpus":4,"memory":8589934592,"disk":107374182400,"arch":"aarch64"}'
`
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	c := New(&execRunner{bin: stub})
	vms, err := c.List()
	if err != nil {
		t.Fatalf("List() against stub limactl: %v", err)
	}
	if len(vms) != 1 || vms[0].Name != "claude" || vms[0].Status != "Running" {
		t.Fatalf("got %+v, want one Running VM named claude", vms)
	}
}

func TestMethodArgv(t *testing.T) {
	cases := []struct {
		name string
		call func(*Client)
		want []string
	}{
		{"status", func(c *Client) { _, _ = c.Status("vm1") }, []string{"list", "vm1", "--format", "{{.Status}}"}},
		{"start", func(c *Client) { _ = c.Start("vm1") }, []string{"start", "vm1"}},
		{"stop", func(c *Client) { _ = c.Stop("vm1") }, []string{"stop", "vm1"}},
		{"delete", func(c *Client) { _ = c.Delete("vm1", false) }, []string{"delete", "vm1"}},
		{"delete-force", func(c *Client) { _ = c.Delete("vm1", true) }, []string{"delete", "vm1", "-f"}},
		{"clone", func(c *Client) { _ = c.Clone("base", "vm1") }, []string{"clone", "base", "vm1"}},
		{"configure", func(c *Client) { _ = c.Configure("vm1", 4, "8GiB", "100GiB") },
			[]string{"edit", "--set", `.cpus=4 | .memory="8GiB" | .disk="100GiB" | .mounts |= map(select(.writable != true))`, "vm1"}},
		{"create", func(c *Client) { _ = c.Create("vm1", "/tmp/overlay.yaml") }, []string{"start", "--name", "vm1", "--tty=false", "/tmp/overlay.yaml"}},
		{"shell", func(c *Client) { _ = c.Shell(context.Background(), "vm1", nil, io.Discard, "ls", "-la") }, []string{"shell", "vm1", "ls", "-la"}},
		// The streaming variants build the same argv as their buffered counterparts;
		// they differ only in routing output to the writer (and honouring ctx).
		{"start-streaming", func(c *Client) { _ = c.StartStreaming(context.Background(), "vm1", io.Discard) }, []string{"start", "vm1"}},
		{"stop-streaming", func(c *Client) { _ = c.StopStreaming(context.Background(), "vm1", io.Discard) }, []string{"stop", "vm1"}},
		{"clone-streaming", func(c *Client) { _ = c.CloneStreaming(context.Background(), "base", "vm1", io.Discard) }, []string{"clone", "base", "vm1"}},
		{"create-streaming", func(c *Client) { _ = c.CreateStreaming(context.Background(), "vm1", "/tmp/overlay.yaml", io.Discard) }, []string{"start", "--name", "vm1", "--tty=false", "/tmp/overlay.yaml"}},
		// Copy builds `copy -v --backend=scp [-r] <src> <dst>`. A host->guest
		// upload has no -r; a guest->host recursive download inserts -r before the
		// endpoints, and GuestPath prefixes the instance with "<vm>:".
		//
		// The backend is pinned, and this pins the pin: under --backend=auto, limactl
		// picks rsync whenever the guest has it, and Lima's rsync backend copies a
		// directory's CONTENTS rather than the directory — so the same upload lands in
		// two different places depending on the guest's packages. See Copy's doc.
		{"copy-upload", func(c *Client) {
			_ = c.Copy(context.Background(), io.Discard, false, "/host/file.txt", GuestPath("vm1", "/home/u/dir"))
		}, []string{"copy", "-v", "--backend=scp", "/host/file.txt", "vm1:/home/u/dir"}},
		{"copy-download-recursive", func(c *Client) {
			_ = c.Copy(context.Background(), io.Discard, true, GuestPath("vm1", "/home/u/src"), "/host/dst")
		}, []string{"copy", "-v", "--backend=scp", "-r", "vm1:/home/u/src", "/host/dst"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{outputs: map[string][]byte{}}
			c := New(f)
			tc.call(c)
			if len(f.calls) != 1 {
				t.Fatalf("got %d calls, want 1: %v", len(f.calls), f.calls)
			}
			if got := f.calls[0]; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("argv = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPreflightArgvAndErrors(t *testing.T) {
	// Happy path: both probes succeed, two calls in order.
	f := &fakeRunner{outputs: map[string][]byte{}}
	if err := New(f).Preflight(); err != nil {
		t.Fatalf("Preflight() error: %v", err)
	}
	want := [][]string{{"--version"}, {"clone", "--help"}}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("Preflight argv = %v, want %v", f.calls, want)
	}

	// Missing binary surfaces a friendly "not found" error.
	missing := &fakeRunner{outputs: map[string][]byte{}, err: exec.ErrNotFound}
	err := New(missing).Preflight()
	if err == nil || !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("Preflight() with missing binary = %v, want exec.ErrNotFound wrapped", err)
	}
}

// lima-vm/lima#5236: while another instance is mid-clone or mid-delete, its
// directory exists without a lima.yaml, and `limactl list` does not skip it — it
// exits 1 and prints nothing, so the whole listing is lost. That is not a failure
// worth reporting to the user, and List says so with a sentinel.
func TestListRacedInstanceDirIsDistinguishedFromARealFailure(t *testing.T) {
	// limactl's real stderr in that window, folded into the error by Runner.Output.
	const cloneWindow = `exit status 1: time="..." level=warning msg="The directory ` +
		"`/home/u/.lima`" + ` does not look like a valid Lima directory: stat /home/u/.lima/web/lima.yaml: no such file or directory"
time="..." level=fatal msg="unable to load instance web: open /home/u/.lima/web/lima.yaml: no such file or directory"`

	_, err := New(errRunner{msg: cloneWindow}).List()
	if !errors.Is(err, ErrListRacedInstanceDir) {
		t.Fatalf("a clone/delete window must be reported as ErrListRacedInstanceDir, got %v", err)
	}

	// Anything else is a real failure and must NOT be swallowed as one.
	for _, real := range []string{
		"exit status 1: limactl not found",
		`exit status 1: level=fatal msg="failed to connect to the daemon"`,
	} {
		_, err := New(errRunner{msg: real}).List()
		if err == nil {
			t.Fatalf("%q should fail", real)
		}
		if errors.Is(err, ErrListRacedInstanceDir) {
			t.Fatalf("a genuine failure must not be mistaken for a clone window: %q", real)
		}
	}
}

// errRunner fails every command with a fixed message.
type errRunner struct{ msg string }

func (r errRunner) Output(context.Context, ...string) ([]byte, error) {
	return nil, errors.New(r.msg)
}
func (r errRunner) Stream(context.Context, io.Reader, io.Writer, ...string) error    { return nil }
func (r errRunner) StreamOut(context.Context, io.Reader, io.Writer, ...string) error { return nil }

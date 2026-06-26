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
		{"create", func(c *Client) { _ = c.Create("vm1", "/tmp/overlay.yaml") }, []string{"start", "--name", "vm1", "--tty=false", "/tmp/overlay.yaml"}},
		{"shell", func(c *Client) { _ = c.Shell(context.Background(), "vm1", nil, io.Discard, "ls", "-la") }, []string{"shell", "vm1", "ls", "-la"}},
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

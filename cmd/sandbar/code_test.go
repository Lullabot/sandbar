package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/sshconfig"
	"github.com/lullabot/sandbar/internal/vm"
	"github.com/lullabot/sandbar/internal/vscode"
)

// codeEnv isolates every file `sandbar code` can write — ~/.ssh/config under
// HOME, the managed ssh config under XDG_CONFIG_HOME, the managed-VM index
// under XDG_DATA_HOME — and replaces the code lookup and launcher, so no test
// edits the developer's ssh setup or opens VS Code. It returns a pointer to
// the argv the fake launcher received (nil if it was never called).
func codeEnv(t *testing.T, codePath string) *[]string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	oldFind, oldLaunch := vscode.FindCode, vscode.Launch
	t.Cleanup(func() { vscode.FindCode, vscode.Launch = oldFind, oldLaunch })
	vscode.FindCode = func() string { return codePath }
	var launched []string
	vscode.Launch = func(argv []string) error {
		launched = argv
		return nil
	}
	return &launched
}

// writeIncludePresent gives the isolated HOME a ~/.ssh/config that already
// includes the managed file, so a test about launching is not also about
// the consent prompt.
func writeIncludePresent(t *testing.T) {
	t.Helper()
	path := sshconfig.UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(sshconfig.IncludeLine()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func codeProvider(status string) (*providerfake.Provider, *bool) {
	called := false
	return &providerfake.Provider{
		GetFunc: func(name string) (vm.VM, error) {
			return vm.VM{Name: name, Status: status}, nil
		},
		EditorSSHFunc: func(_ context.Context, v vm.VM) (sshconfig.Entry, error) {
			called = true
			return sshconfig.Entry{Alias: "lima-" + v.Name, Include: "/lima/" + v.Name + "/ssh.config"}, nil
		},
		GuestHomeFunc: func(vm.VM) string { return "/home/dev.linux" },
	}, &called
}

func TestCodeLaunchesOnProjectCheckout(t *testing.T) {
	launched := codeEnv(t, "/usr/bin/code")
	writeIncludePresent(t)
	reg := registry.NewEmpty()
	cfg := vm.DefaultCreateConfig()
	cfg.Name = "web"
	cfg.CloneURL = "https://github.com/acme/widgets"
	if err := reg.AddScoped(cfg, registry.LocalScope); err != nil {
		t.Fatal(err)
	}
	p, _ := codeProvider(limaRunning)

	var out bytes.Buffer
	if err := doCode(context.Background(), p, reg, registry.LocalScope, "web", strings.NewReader(""), false, &out); err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/code", "--remote", "ssh-remote+lima-web", "/home/dev.linux/github.com/acme/widgets"}
	if !reflect.DeepEqual(*launched, want) {
		t.Fatalf("launched %q, want %q", *launched, want)
	}
	if !strings.Contains(out.String(), "Opening VS Code on lima-web (/home/dev.linux/github.com/acme/widgets)") {
		t.Fatalf("output = %q", out.String())
	}
}

// markerProvider adds a provenance marker to the fake, the authority the
// clone URL is read from ahead of the managed index.
type markerProvider struct {
	*providerfake.Provider
	cloneURL string
}

func (p markerProvider) ProvenanceOf(context.Context, string) (provider.Provenance, bool, error) {
	return provider.Provenance{Config: vm.CreateConfig{CloneURL: p.cloneURL}}, true, nil
}
func (p markerProvider) Provenance(context.Context) (map[string]provider.Provenance, error) {
	return nil, nil
}
func (p markerProvider) MarkManaged(context.Context, string, provider.Provenance) error {
	panic("sandbar code wrote a marker")
}
func (p markerProvider) Unmark(context.Context, string) error {
	panic("sandbar code removed a marker")
}

func TestCodeReadsCloneURLFromMarkerFirst(t *testing.T) {
	launched := codeEnv(t, "/usr/bin/code")
	writeIncludePresent(t)
	reg := registry.NewEmpty()
	cfg := vm.DefaultCreateConfig()
	cfg.Name = "web"
	cfg.CloneURL = "https://github.com/stale/index"
	if err := reg.AddScoped(cfg, registry.LocalScope); err != nil {
		t.Fatal(err)
	}
	fake, _ := codeProvider(limaRunning)
	p := markerProvider{Provider: fake, cloneURL: "https://gitlab.com/acme/api.git"}

	if err := doCode(context.Background(), p, reg, registry.LocalScope, "web", strings.NewReader(""), false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := (*launched)[len(*launched)-1]; got != "/home/dev.linux/gitlab.com/acme/api" {
		t.Fatalf("folder = %q, want the marker's checkout", got)
	}
}

func TestCodeWithoutCodeCommandNamesAlias(t *testing.T) {
	launched := codeEnv(t, "")
	writeIncludePresent(t)
	p, _ := codeProvider(limaRunning)

	var out bytes.Buffer
	if err := doCode(context.Background(), p, registry.NewEmpty(), registry.LocalScope, "web", strings.NewReader(""), false, &out); err != nil {
		t.Fatal(err)
	}
	if *launched != nil {
		t.Fatalf("launched %q without a code command", *launched)
	}
	if !strings.Contains(out.String(), `"Remote-SSH: Connect to Host…" and choose lima-web`) {
		t.Fatalf("output = %q", out.String())
	}
}

func TestCodeRefusesStoppedVM(t *testing.T) {
	launched := codeEnv(t, "/usr/bin/code")
	p, editorCalled := codeProvider("Stopped")

	err := doCode(context.Background(), p, registry.NewEmpty(), registry.LocalScope, "web", strings.NewReader("y\n"), true, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "start it first") {
		t.Fatalf("err = %v, want a not-running refusal", err)
	}
	if *editorCalled || *launched != nil {
		t.Fatal("a stopped VM must not get an entry or a launch")
	}
	// Refused before the consent prompt, so ~/.ssh/config was never created.
	if _, err := os.Stat(sshconfig.UserConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("~/.ssh/config touched for a refused VM: %v", err)
	}
}

func TestCodeIncludeConsent(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		interactive bool
		wantAdded   bool
		wantOut     string
	}{
		{"yes on a terminal", "y\n", true, true, "Add " + "Include"},
		{"no on a terminal", "n\n", true, false, "add this line at the top"},
		{"not a terminal never edits", "y\n", false, false, "add this line at the top"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			codeEnv(t, "/usr/bin/code")
			p, _ := codeProvider(limaRunning)
			var out bytes.Buffer
			if err := doCode(context.Background(), p, registry.NewEmpty(), registry.LocalScope, "web", strings.NewReader(c.input), c.interactive, &out); err != nil {
				t.Fatal(err)
			}
			st, err := sshconfig.IncludeStatus()
			if err != nil {
				t.Fatal(err)
			}
			if st.Present != c.wantAdded {
				t.Fatalf("Include present = %v, want %v; output:\n%s", st.Present, c.wantAdded, out.String())
			}
			if !strings.Contains(out.String(), c.wantOut) {
				t.Fatalf("output lacks %q:\n%s", c.wantOut, out.String())
			}
			if !c.wantAdded && !strings.Contains(out.String(), sshconfig.IncludeLine()) {
				t.Fatalf("output does not show the line to add by hand:\n%s", out.String())
			}
		})
	}
}

func TestCodeSymlinkedConfigIsNeverEdited(t *testing.T) {
	codeEnv(t, "/usr/bin/code")
	real := filepath.Join(t.TempDir(), "dotfiles-ssh-config")
	if err := os.WriteFile(real, []byte("Host x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := sshconfig.UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	p, _ := codeProvider(limaRunning)
	var out bytes.Buffer
	// "y" is offered but must never be read: a symlink is not asked about.
	if err := doCode(context.Background(), p, registry.NewEmpty(), registry.LocalScope, "web", strings.NewReader("y\n"), true, &out); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(real); string(data) != "Host x\n" {
		t.Fatalf("symlink target edited: %q", data)
	}
	if strings.Contains(out.String(), "[y/N]") || !strings.Contains(out.String(), sshconfig.IncludeLine()) {
		t.Fatalf("want the line printed without a prompt:\n%s", out.String())
	}
}

// TestIsTerminalRejectsDevNull pins the case the character-device bit gets
// wrong: stdin redirected from /dev/null must not be taken for a terminal, or
// an unattended run asks a question it can never get an answer to. A regular
// file is the plain non-terminal control.
func TestIsTerminalRejectsDevNull(t *testing.T) {
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("no %s: %v", os.DevNull, err)
	}
	defer null.Close()
	if isTerminal(null) {
		t.Fatalf("isTerminal(%s) = true; want false", os.DevNull)
	}
	f, err := os.CreateTemp(t.TempDir(), "plain")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Fatal("isTerminal(regular file) = true; want false")
	}
}

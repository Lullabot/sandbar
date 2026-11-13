package vscode

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/sshconfig"
	"github.com/lullabot/sandbar/internal/vm"
)

// isolate points every path this package can write (the managed ssh config
// under XDG_CONFIG_HOME, ~/.ssh/config under HOME) at temp dirs, and pins the
// code lookup so no test depends on whether VS Code is installed here.
func isolate(t *testing.T, codeOnPath string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	oldLook, oldGOOS, oldStat := lookPath, goos, statFile
	t.Cleanup(func() { lookPath, goos, statFile = oldLook, oldGOOS, oldStat })
	lookPath = func(string) (string, error) {
		if codeOnPath == "" {
			return "", errors.New("not found")
		}
		return codeOnPath, nil
	}
	goos = "linux"
	statFile = func(string) error { return os.ErrNotExist }
}

func fakeProvider(entry sshconfig.Entry, err error) *providerfake.Provider {
	return &providerfake.Provider{
		EditorSSHFunc: func(context.Context, vm.VM) (sshconfig.Entry, error) { return entry, err },
		GuestHomeFunc: func(vm.VM) string { return "/home/dev.linux" },
	}
}

func TestPrepareOpensProjectCheckoutAndWritesEntry(t *testing.T) {
	isolate(t, "/usr/bin/code")
	p := fakeProvider(sshconfig.Entry{Alias: "lima-web", Include: "/lima/web/ssh.config"}, nil)

	res, err := Prepare(context.Background(), p, vm.VM{Name: "web"}, "https://github.com/acme/widgets.git")
	if err != nil {
		t.Fatal(err)
	}
	wantFolder := "/home/dev.linux/github.com/acme/widgets"
	if res.Alias != "lima-web" || res.Folder != wantFolder || res.CodePath != "/usr/bin/code" {
		t.Fatalf("result = %+v", res)
	}
	wantArgv := []string{"/usr/bin/code", "--remote", "ssh-remote+lima-web", wantFolder}
	if !reflect.DeepEqual(res.LaunchArgv, wantArgv) {
		t.Fatalf("argv = %q, want %q", res.LaunchArgv, wantArgv)
	}
	// The far side of the claim: the managed file ssh will read holds the entry.
	data, err := os.ReadFile(sshconfig.ManagedPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "/lima/web/ssh.config") {
		t.Fatalf("managed file lacks the entry:\n%s", data)
	}
}

func TestPrepareWithoutCloneURLOpensHome(t *testing.T) {
	isolate(t, "/usr/bin/code")
	p := fakeProvider(sshconfig.Entry{Alias: "lima-web", Include: "/lima/web/ssh.config", AuthorizedKey: "/k.pub"}, nil)
	res, err := Prepare(context.Background(), p, vm.VM{Name: "web"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Folder != "/home/dev.linux" || res.AuthorizedKey != "/k.pub" {
		t.Fatalf("result = %+v", res)
	}
}

func TestPrepareFallsBackToGuestUserHome(t *testing.T) {
	isolate(t, "/usr/bin/code")
	p := fakeProvider(sshconfig.Entry{Alias: "lima-web", Include: "/x"}, nil)
	p.GuestHomeFunc = func(vm.VM) string { return "" }
	p.GuestUserFunc = func(vm.VM) string { return "dev" }
	res, err := Prepare(context.Background(), p, vm.VM{Name: "web"}, "")
	if err != nil || res.Folder != "/home/dev" {
		t.Fatalf("Prepare = %+v, %v", res, err)
	}
}

func TestPrepareWithoutCodeReturnsNoArgv(t *testing.T) {
	isolate(t, "")
	p := fakeProvider(sshconfig.Entry{Alias: "lima-web", Include: "/x"}, nil)
	res, err := Prepare(context.Background(), p, vm.VM{Name: "web"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.CodePath != "" || res.LaunchArgv != nil {
		t.Fatalf("want no code, got %+v", res)
	}
	// The entry is still written: the fallback tells the user to pick the alias.
	if _, err := os.Stat(sshconfig.ManagedPath()); err != nil {
		t.Fatalf("managed file not written: %v", err)
	}
}

func TestFindCodeUsesMacAppBundle(t *testing.T) {
	isolate(t, "")
	goos = "darwin"
	statFile = func(p string) error {
		if p == macAppCode {
			return nil
		}
		return os.ErrNotExist
	}
	if got := FindCode(); got != macAppCode {
		t.Fatalf("FindCode() = %q, want %q", got, macAppCode)
	}
	goos = "linux"
	if got := FindCode(); got != "" {
		t.Fatalf("non-darwin FindCode() = %q, want empty", got)
	}
}

func TestPrepareEditorSSHErrorWritesNothing(t *testing.T) {
	isolate(t, "/usr/bin/code")
	p := fakeProvider(sshconfig.Entry{}, errors.New("no guest address"))
	if _, err := Prepare(context.Background(), p, vm.VM{Name: "web"}, ""); err == nil || !strings.Contains(err.Error(), "no guest address") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(sshconfig.ManagedPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed file written despite error: %v", err)
	}
}

func TestClassifyInclude(t *testing.T) {
	cases := []struct {
		st   sshconfig.IncludeState
		want IncludeAction
	}{
		{sshconfig.IncludeState{Present: true, Symlink: true}, IncludePresent},
		{sshconfig.IncludeState{Writable: true}, IncludeAsk},
		{sshconfig.IncludeState{Exists: true, Writable: true}, IncludeAsk},
		{sshconfig.IncludeState{Exists: true, Symlink: true, Writable: true}, IncludeManual},
		{sshconfig.IncludeState{Exists: true}, IncludeManual},
	}
	for _, c := range cases {
		if got := ClassifyInclude(c.st); got != c.want {
			t.Errorf("ClassifyInclude(%+v) = %v, want %v", c.st, got, c.want)
		}
	}
}

func TestLaunchStartsDetached(t *testing.T) {
	// Launch is a var so front-end tests never open VS Code; starting a
	// harmless binary checks the detached path itself works.
	if err := Launch([]string{"true"}); err != nil {
		t.Fatalf("Launch(true) = %v", err)
	}
	if err := Launch(nil); err == nil {
		t.Fatal("Launch(nil) should fail")
	}
}

package vscode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/profiles"
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

var (
	localProf = profiles.Profile{ID: profiles.LocalProfileID, Name: "local", Type: profiles.TypeLocal}
	miniProf  = profiles.Profile{ID: "mini", Name: "Mac Mini", Type: profiles.TypeRemoteSSH}
)

func fakeProvider(entry sshconfig.Entry, err error) *providerfake.Provider {
	return &providerfake.Provider{
		EditorSSHFunc: func(context.Context, vm.VM) (sshconfig.Entry, error) { return entry, err },
		GuestHomeFunc: func(vm.VM) string { return "/home/dev.linux" },
	}
}

func TestPrepareOpensProjectCheckoutAndWritesEntry(t *testing.T) {
	isolate(t, "/usr/bin/code")
	p := fakeProvider(sshconfig.Entry{Include: "/lima/web/ssh.config"}, nil)

	res, err := Prepare(context.Background(), p, localProf, vm.VM{Name: "web"}, "https://github.com/acme/widgets.git")
	if err != nil {
		t.Fatal(err)
	}
	wantFolder := "/home/dev.linux/github.com/acme/widgets"
	if res.Alias != "lima-web" || res.Folder != wantFolder {
		t.Fatalf("result = %+v", res)
	}
	wantArgv := []string{"/usr/bin/code", "--remote", "ssh-remote+lima-web", wantFolder}
	if !reflect.DeepEqual(res.LaunchArgv, wantArgv) {
		t.Fatalf("argv = %q, want %q", res.LaunchArgv, wantArgv)
	}
	// The far side of the claim: the managed file ssh will read holds the
	// entry, under the alias the caller chose.
	data, err := os.ReadFile(sshconfig.ManagedPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "/lima/web/ssh.config") || !strings.Contains(string(data), "# sandbar begin lima-web") {
		t.Fatalf("managed file lacks the entry:\n%s", data)
	}
}

func TestPrepareWithoutCloneURLOpensHome(t *testing.T) {
	isolate(t, "/usr/bin/code")
	p := fakeProvider(sshconfig.Entry{Include: "/lima/web/ssh.config", AuthorizedKey: "/k.pub"}, nil)
	res, err := Prepare(context.Background(), p, localProf, vm.VM{Name: "web"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Folder != "/home/dev.linux" || res.AuthorizedKey != "/k.pub" {
		t.Fatalf("result = %+v", res)
	}
}

// A guest home the provider reported with the entry is used as-is: reading
// it again would be another round trip to the remote host.
func TestPrepareUsesEntryGuestHome(t *testing.T) {
	isolate(t, "/usr/bin/code")
	p := fakeProvider(sshconfig.Entry{HostName: "127.0.0.1", GuestHome: "/home/remote"}, nil)
	p.GuestHomeFunc = func(vm.VM) string {
		t.Fatal("GuestHome must not be read when the entry carries the home")
		return ""
	}
	res, err := Prepare(context.Background(), p, miniProf, vm.VM{Name: "web"}, "")
	if err != nil || res.Folder != "/home/remote" {
		t.Fatalf("Prepare = %+v, %v", res, err)
	}
}

func TestPrepareFallsBackToGuestUserHome(t *testing.T) {
	isolate(t, "/usr/bin/code")
	p := fakeProvider(sshconfig.Entry{Include: "/x"}, nil)
	p.GuestHomeFunc = func(vm.VM) string { return "" }
	p.GuestUserFunc = func(vm.VM) string { return "dev" }
	res, err := Prepare(context.Background(), p, localProf, vm.VM{Name: "web"}, "")
	if err != nil || res.Folder != "/home/dev" {
		t.Fatalf("Prepare = %+v, %v", res, err)
	}
}

func TestPrepareWithoutCodeReturnsNoArgv(t *testing.T) {
	isolate(t, "")
	p := fakeProvider(sshconfig.Entry{Include: "/x"}, nil)
	res, err := Prepare(context.Background(), p, localProf, vm.VM{Name: "web"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.LaunchArgv != nil {
		t.Fatalf("want no code, got %+v", res)
	}
	// The entry is still written: the fallback tells the user to pick the alias.
	if _, err := os.Stat(sshconfig.ManagedPath()); err != nil {
		t.Fatalf("managed file not written: %v", err)
	}
}

// An entry with nothing to connect to is refused rather than written as a
// bare Host line.
func TestPrepareRefusesEmptyEntry(t *testing.T) {
	isolate(t, "/usr/bin/code")
	p := fakeProvider(sshconfig.Entry{}, nil)
	if _, err := Prepare(context.Background(), p, localProf, vm.VM{Name: "web"}, ""); err == nil {
		t.Fatal("Prepare accepted an empty entry")
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
	if _, err := Prepare(context.Background(), p, localProf, vm.VM{Name: "web"}, ""); err == nil || !strings.Contains(err.Error(), "no guest address") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(sshconfig.ManagedPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed file written despite error: %v", err)
	}
}

// pinLaunch replaces Launch for one test, recording each argv and failing
// with err.
func pinLaunch(t *testing.T, err error) *[][]string {
	t.Helper()
	old := Launch
	t.Cleanup(func() { Launch = old })
	var got [][]string
	Launch = func(argv []string) error {
		got = append(got, argv)
		return err
	}
	return &got
}

// Open reports the authorized key ahead of the outcome, and launches with
// the argv Prepare computed.
func TestOpenLaunches(t *testing.T) {
	isolate(t, "/usr/bin/code")
	launched := pinLaunch(t, nil)
	p := fakeProvider(sshconfig.Entry{HostName: "127.0.0.1", AuthorizedKey: "/k.pub"}, nil)

	lines, ok, err := Open(context.Background(), p, miniProf, vm.VM{Name: "web"}, "")
	if err != nil || !ok {
		t.Fatalf("Open = %v, %v, %v", lines, ok, err)
	}
	want := []string{"Authorized /k.pub in the guest.", "Opening VS Code on sandbar-mini-web (/home/dev.linux)"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
	if len(*launched) != 1 || (*launched)[0][0] != "/usr/bin/code" {
		t.Fatalf("launches = %v", *launched)
	}
}

// Without a `code` command nothing is launched and the last line names the
// alias to pick in VS Code.
func TestOpenWithoutCodeNamesTheAlias(t *testing.T) {
	isolate(t, "")
	launched := pinLaunch(t, nil)
	lines, ok, err := Open(context.Background(), fakeProvider(sshconfig.Entry{Include: "/x"}, nil), localProf, vm.VM{Name: "web"}, "")
	if err != nil || ok {
		t.Fatalf("Open = %v, %v, %v", lines, ok, err)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "Remote-SSH: Connect to Host") || !strings.Contains(lines[0], "lima-web") {
		t.Fatalf("lines = %q", lines)
	}
	if len(*launched) != 0 {
		t.Fatalf("launched without a code command: %v", *launched)
	}
}

// A failed launch is an error naming the command, and keeps the lines already
// worth reporting.
func TestOpenLaunchFailure(t *testing.T) {
	isolate(t, "/usr/bin/code")
	pinLaunch(t, errors.New("exec format error"))
	p := fakeProvider(sshconfig.Entry{HostName: "127.0.0.1", AuthorizedKey: "/k.pub"}, nil)
	lines, ok, err := Open(context.Background(), p, miniProf, vm.VM{Name: "web"}, "")
	if err == nil || ok || !strings.Contains(err.Error(), "start /usr/bin/code") {
		t.Fatalf("Open = %v, %v, %v", lines, ok, err)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "/k.pub") {
		t.Fatalf("lines = %q, want the authorized-key note", lines)
	}
}

// pinSSH stands in for the ssh CheckInclude's probe runs: applied says
// whether ssh reports the managed file as applied, and err, when set, is what
// running ssh fails with instead.
func pinSSH(t *testing.T, applied *bool, err *error) {
	t.Helper()
	old := sshconfig.QuerySSH
	t.Cleanup(func() { sshconfig.QuerySSH = old })
	sshconfig.QuerySSH = func(context.Context, ...string) ([]byte, error) {
		if *err != nil {
			return nil, *err
		}
		if *applied {
			return []byte("hostname include-check.sandbar.invalid\n"), nil
		}
		return []byte("hostname " + sshconfig.IncludeProbeHost + "\n"), nil
	}
}

func TestCheckInclude(t *testing.T) {
	isolate(t, "")
	applied, sshErr := false, error(nil)
	pinSSH(t, &applied, &sshErr)
	ctx := context.Background()
	path := sshconfig.UserConfigPath()

	if got, why := CheckInclude(ctx); got != IncludeAsk || why != "" {
		t.Fatalf("missing, creatable config: CheckInclude = %v, %q; want IncludeAsk", got, why)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// An Include line ssh does not apply: asked, with the reason.
	misplaced := "Host x\n  " + sshconfig.IncludeLine() + "\n"
	if err := os.WriteFile(path, []byte(misplaced), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, why := CheckInclude(ctx); got != IncludeAsk || !strings.Contains(why, "is not applied (it may be inside a Host or Match block)") {
		t.Fatalf("misplaced: CheckInclude = %v, %q; want IncludeAsk explaining why", got, why)
	}
	// ssh says it applies (an Include under "Host *", or via another file).
	applied = true
	if got, _ := CheckInclude(ctx); got != IncludePresent {
		t.Fatalf("applied: CheckInclude = %v, want IncludePresent", got)
	}
	// No ssh to ask: the line in the file is taken at its word, as before.
	applied, sshErr = false, sshconfig.ErrNoSSH
	if got, _ := CheckInclude(ctx); got != IncludePresent {
		t.Fatalf("no ssh, line present: CheckInclude = %v, want IncludePresent", got)
	}
	if err := os.WriteFile(path, []byte("Host x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, why := CheckInclude(ctx); got != IncludeAsk || why != "" {
		t.Fatalf("no ssh, no line: CheckInclude = %v, %q; want a plain IncludeAsk", got, why)
	}
	sshErr = nil

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "dotfiles-config")
	if err := os.WriteFile(target, []byte("Host x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if got, why := CheckInclude(ctx); got != IncludeManual || !strings.Contains(why, "symlink") {
		t.Fatalf("symlink: CheckInclude = %v, %q; want IncludeManual naming the symlink", got, why)
	}
	if err := os.WriteFile(target, []byte(misplaced), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, why := CheckInclude(ctx); got != IncludeManual || !strings.Contains(why, "not applied") || !strings.Contains(why, "symlink") {
		t.Fatalf("symlink, misplaced: CheckInclude = %v, %q; want IncludeManual giving both reasons", got, why)
	}
	// Applied wins even through a symlink: there is nothing to edit.
	applied = true
	if got, _ := CheckInclude(ctx); got != IncludePresent {
		t.Fatalf("included: CheckInclude = %v, want IncludePresent", got)
	}
	applied = false

	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("Host x\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	if got, why := CheckInclude(ctx); got != IncludeManual || !strings.Contains(why, "not writable") {
		t.Fatalf("read-only: CheckInclude = %v, %q; want IncludeManual saying not writable", got, why)
	}
}

// Alias keeps Lima's own name on Local (its Include matches only that) and
// namespaces every other profile's VMs by the profile's ID, so a rename
// changes nothing.
func TestAlias(t *testing.T) {
	local := profiles.Profile{ID: profiles.LocalProfileID, Name: "my-laptop", Type: profiles.TypeLocal}
	if got := Alias(local, "web"); got != "lima-web" {
		t.Fatalf("Alias(local) = %q", got)
	}
	for _, typ := range []profiles.Type{profiles.TypeRemoteSSH, profiles.TypeProxmox} {
		for _, name := range []string{"Home PVE", "Renamed"} {
			if got := Alias(profiles.Profile{ID: "0123456789abcdef", Name: name, Type: typ}, "web"); got != "sandbar-0123456789abcdef-web" {
				t.Fatalf("Alias(%s, %q) = %q", typ, name, got)
			}
		}
	}
}

// seedEntries writes one entry per alias.
func seedEntries(t *testing.T, aliases ...string) {
	t.Helper()
	for _, a := range aliases {
		if err := sshconfig.Upsert(sshconfig.Entry{Alias: a, HostName: "10.0.0.1"}); err != nil {
			t.Fatal(err)
		}
	}
}

// managedAliases is every VM alias in the managed file.
func managedAliases(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(sshconfig.ManagedPath())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if a, ok := strings.CutPrefix(l, "# sandbar begin "); ok {
			out = append(out, a)
		}
	}
	return out
}

// The cleanups stay inside one profile's namespace: Local owns the lima-
// blocks, a remote profile the blocks under its ID, and neither touches the
// other's — nor a different profile whose ID merely shares a VM's name.
func TestCleanupsStayInTheProfilesNamespace(t *testing.T) {
	isolate(t, "")
	other := profiles.Profile{ID: "other", Type: profiles.TypeProxmox}
	seedEntries(t, "lima-web", "lima-api", "sandbar-mini-web", "sandbar-mini-api", "sandbar-other-web")

	if got, err := RemoveEntries(miniProf, "web"); err != nil || strings.Join(got, ",") != "sandbar-mini-web" {
		t.Fatalf("RemoveEntries = %v, %v", got, err)
	}
	if got, err := PruneEntries(localProf, []vm.VM{{Name: "web"}}); err != nil || strings.Join(got, ",") != "lima-api" {
		t.Fatalf("PruneEntries(local) = %v, %v", got, err)
	}
	if got, err := RemoveProfileEntries(other); err != nil || strings.Join(got, ",") != "sandbar-other-web" {
		t.Fatalf("RemoveProfileEntries = %v, %v", got, err)
	}
	if got := strings.Join(managedAliases(t), ","); got != "lima-web,sandbar-mini-api" {
		t.Fatalf("left %q", got)
	}
	// A profile with no ID owns nothing, rather than every "sandbar--" alias.
	if got, err := RemoveProfileEntries(profiles.Profile{Type: profiles.TypeProxmox}); err != nil || got != nil {
		t.Fatalf("ID-less profile removed %v, %v", got, err)
	}
}

// Opening a VM prunes its profile's entries for VMs the fresh listing no
// longer has, keeps the one it just wrote, and reports what went.
func TestOpenPrunesStaleEntriesOfTheProfile(t *testing.T) {
	isolate(t, "")
	seedEntries(t, "sandbar-mini-gone", "sandbar-mini-api", "sandbar-other-gone", "lima-gone")
	p := fakeProvider(sshconfig.Entry{HostName: "127.0.0.1"}, nil)
	p.ListFunc = func() ([]vm.VM, error) { return []vm.VM{{Name: "web"}, {Name: "api"}}, nil }

	lines, _, err := Open(context.Background(), p, miniProf, vm.VM{Name: "web"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(managedAliases(t), ","); got != "sandbar-mini-api,sandbar-other-gone,lima-gone,sandbar-mini-web" {
		t.Fatalf("left %q", got)
	}
	if len(lines) != 2 || lines[0] != "Removed ssh host entries for VMs that no longer exist: sandbar-mini-gone." {
		t.Fatalf("lines = %q", lines)
	}
}

// A listing that fails, or that does not even contain the VM being opened,
// prunes nothing and does not fail the open.
func TestOpenSkipsPruneOnUntrustworthyListing(t *testing.T) {
	for name, list := range map[string]func() ([]vm.VM, error){
		"error":          func() ([]vm.VM, error) { return nil, errors.New("limactl list: instance mid-clone") },
		"missing the VM": func() ([]vm.VM, error) { return []vm.VM{{Name: "api"}}, nil },
		"empty listing":  func() ([]vm.VM, error) { return nil, nil },
	} {
		t.Run(name, func(t *testing.T) {
			isolate(t, "")
			seedEntries(t, "sandbar-mini-gone")
			p := fakeProvider(sshconfig.Entry{HostName: "127.0.0.1"}, nil)
			p.ListFunc = list
			if _, _, err := Open(context.Background(), p, miniProf, vm.VM{Name: "web"}, ""); err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(managedAliases(t), ","); got != "sandbar-mini-gone,sandbar-mini-web" {
				t.Fatalf("left %q", got)
			}
		})
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

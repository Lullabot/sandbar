package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/sshconfig"
	"github.com/lullabot/sandbar/internal/vm"
	"github.com/lullabot/sandbar/internal/vscode"
)

// codeEnv isolates everything the `c` verb touches on the host — the managed
// ssh config (XDG_CONFIG_HOME, via isolateHostState), ~/.ssh/config (HOME) —
// and replaces VS Code's discovery and launch, so no test reads the
// developer's ssh setup or opens an editor. It returns the argv each launch
// received.
func codeEnv(t *testing.T, codePath string) (home string, launched *[][]string) {
	t.Helper()
	isolateHostState(t)
	home = t.TempDir()
	t.Setenv("HOME", home)

	oldFind, oldLaunch := vscode.FindCode, vscode.Launch
	t.Cleanup(func() { vscode.FindCode, vscode.Launch = oldFind, oldLaunch })
	vscode.FindCode = func() string { return codePath }
	var got [][]string
	vscode.Launch = func(argv []string) error {
		got = append(got, append([]string(nil), argv...))
		return nil
	}
	return home, &got
}

// codeModel is a single local member whose running VM "web" was created from
// a GitHub repository, so the folder VS Code opens is the project checkout.
func codeModel(t *testing.T) model {
	t.Helper()
	p := &providerfake.Provider{
		EditorSSHFunc: func(_ context.Context, v vm.VM) (sshconfig.Entry, error) {
			return sshconfig.Entry{Include: "/lima/" + v.Name + "/ssh.config"}, nil
		},
		GuestHomeFunc: func(vm.VM) string { return "/home/tester" },
	}
	m := New(singleFleet(p, registry.LocalScope)).(model)
	m = resized(m, 120, 40)
	if err := m.reg.AddScoped(vm.CreateConfig{Name: "web", BaseName: "sandbar-base", CloneURL: "https://github.com/acme/widget.git"}, registry.LocalScope); err != nil {
		t.Fatalf("seed web: %v", err)
	}
	return putOnBoard(t, m, vm.VM{Name: "web", Status: "Running"})
}

// runCode drives a dispatched command to its codeResultMsg and through
// Update, the way the program would. beginAction batches the operation with
// the spinner tick, so the batch is unwrapped.
func runCode(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a dispatched command, got nil")
	}
	msg := cmd()
	cmds := []tea.Cmd{func() tea.Msg { return msg }}
	if batch, ok := msg.(tea.BatchMsg); ok {
		cmds = batch
	}
	for _, c := range cmds {
		if c == nil {
			continue
		}
		if res, ok := c().(codeResultMsg); ok {
			next, _ := m.Update(res)
			return next.(model)
		}
	}
	t.Fatalf("no codeResultMsg came out of the dispatched command (got %T)", msg)
	return m
}

// pressCode presses `c` on the focused tile and delivers the Include check it
// dispatches back through Update, as the program would: the check runs ssh,
// so the key itself only starts it. It returns what the check's answer led
// to — the consent overlay (with no command), or the operation to run.
func pressCode(t *testing.T, m model) (model, tea.Cmd) {
	t.Helper()
	m, cmd := pressDispatch(t, m, runeKey('c'))
	if cmd == nil {
		t.Fatal("c dispatched nothing; the Include check is missing")
	}
	if m.confirm != nil {
		t.Fatal("the overlay must wait for the Include check's answer")
	}
	checked, ok := cmd().(codeIncludeCheckedMsg)
	if !ok {
		t.Fatal("c should dispatch the Include check first")
	}
	// The handler directly, not Update: Update batches its refresh and
	// heartbeat ticks onto every message, which would hide whether the answer
	// itself dispatched anything (see pressDispatch).
	next, cmd := m.handleCodeIncludeChecked(checked)
	return next.(model), cmd
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func loggedText(m model) string {
	var b strings.Builder
	for _, msg := range m.messages {
		b.WriteString(msg.text + "\n")
	}
	return b.String()
}

// Consent given: the user config gains the Include at the top, the managed
// file gains the VM's block, and VS Code is launched on the project checkout.
func TestCodeVerbConsentAddsIncludeWritesEntryAndLaunches(t *testing.T) {
	home, launched := codeEnv(t, "/fake/bin/code")
	userCfg := filepath.Join(home, ".ssh", "config")
	if err := os.MkdirAll(filepath.Dir(userCfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userCfg, []byte("Host existing\n  User someone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := codeModel(t)

	m, cmd := pressCode(t, m)
	if m.confirm == nil {
		t.Fatal("a missing, writable Include should raise the consent overlay")
	}
	if cmd != nil {
		t.Fatal("nothing may run before the user answers the overlay")
	}
	if view := m.confirmView(); !strings.Contains(view, "[y] add") || !strings.Contains(view, "[n] skip") {
		t.Fatalf("overlay should offer add/skip, got %q", view)
	}
	if !strings.Contains(loggedText(m), sshconfig.IncludeLine()) {
		t.Fatalf("the exact line being consented to should be logged in full, got %q", loggedText(m))
	}

	m, cmd = pressDispatch(t, m, runeKey('y'))
	m = runCode(t, m, cmd)

	cfg := readFile(t, userCfg)
	if !strings.HasPrefix(strings.TrimLeft(firstNonComment(cfg), " "), sshconfig.IncludeLine()) {
		t.Fatalf("~/.ssh/config should start with the Include, got:\n%s", cfg)
	}
	if !strings.Contains(cfg, "Host existing") {
		t.Fatalf("the user's own entries must survive, got:\n%s", cfg)
	}
	managed := readFile(t, sshconfig.ManagedPath())
	if !strings.Contains(managed, "lima-web") || !strings.Contains(managed, "/lima/web/ssh.config") {
		t.Fatalf("managed file should hold web's entry, got:\n%s", managed)
	}
	want := [][]string{{"/fake/bin/code", "--remote", "ssh-remote+lima-web", "/home/tester/github.com/acme/widget"}}
	if !reflect.DeepEqual(*launched, want) {
		t.Fatalf("launch argv = %v, want %v", *launched, want)
	}
	if m.acting {
		t.Fatal("the spinner should stop once the result lands")
	}
	if got := m.lastMessage(); got != "Opening VS Code on lima-web (/home/tester/github.com/acme/widget)…" {
		t.Fatalf("last message = %q", got)
	}
}

// firstNonComment is the first line of an ssh config that is not a comment or
// blank — the one OpenSSH acts on first.
func firstNonComment(s string) string {
	for _, l := range strings.Split(s, "\n") {
		t := strings.TrimSpace(l)
		if t != "" && !strings.HasPrefix(t, "#") {
			return t
		}
	}
	return ""
}

// Consent declined: ~/.ssh/config is untouched, but the entry is still
// written and VS Code still opens — declining is about the edit, not the
// editor — and the user is told the line to add.
func TestCodeVerbDeclineLeavesConfigAndStillLaunches(t *testing.T) {
	home, launched := codeEnv(t, "/fake/bin/code")
	m := codeModel(t)

	m, _ = pressCode(t, m)
	if m.confirm == nil {
		t.Fatal("a missing ~/.ssh/config that can be created should raise the overlay")
	}
	m, cmd := pressDispatch(t, m, runeKey('n'))
	if m.confirm != nil {
		t.Fatal("n should close the overlay")
	}
	m = runCode(t, m, cmd)

	if _, err := os.Stat(filepath.Join(home, ".ssh", "config")); !os.IsNotExist(err) {
		t.Fatalf("declining must not create ~/.ssh/config (stat err %v)", err)
	}
	if !strings.Contains(readFile(t, sshconfig.ManagedPath()), "lima-web") {
		t.Fatal("the entry should be written even without the Include")
	}
	if len(*launched) != 1 {
		t.Fatalf("VS Code should still be launched, got %v", *launched)
	}
	log := loggedText(m)
	if !strings.Contains(log, "Left ~/.ssh/config unchanged") || !strings.Contains(log, "will not find the VM's host alias") {
		t.Fatalf("the log should say the alias will not resolve and give the line, got:\n%s", log)
	}
}

// esc backs out of the verb entirely: unlike `n`, which only declines the
// edit, it writes nothing and opens nothing — otherwise a user who pressed
// `c` by mistake would have no key that stops VS Code from opening.
func TestCodeVerbEscAbandonsTheVerb(t *testing.T) {
	home, launched := codeEnv(t, "/fake/bin/code")
	m := codeModel(t)

	m, _ = pressCode(t, m)
	if m.confirm == nil {
		t.Fatal("a missing ~/.ssh/config that can be created should raise the overlay")
	}
	m, cmd := pressDispatch(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.confirm != nil {
		t.Fatal("esc should close the overlay")
	}
	if cmd != nil {
		t.Fatal("esc must not dispatch the operation")
	}
	if m.acting {
		t.Fatal("esc must not start the spinner")
	}
	if len(*launched) != 0 {
		t.Fatalf("VS Code must not be launched, got %v", *launched)
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh", "config")); !os.IsNotExist(err) {
		t.Fatalf("esc must not create ~/.ssh/config (stat err %v)", err)
	}
	if _, err := os.Stat(sshconfig.ManagedPath()); !os.IsNotExist(err) {
		t.Fatalf("esc must not write the managed file (stat err %v)", err)
	}
}

// A symlinked ~/.ssh/config (a dotfiles checkout) is never offered for
// editing: no overlay, the target is untouched, and the result carries the
// line to add by hand.
func TestCodeVerbSymlinkedConfigSkipsOverlay(t *testing.T) {
	home, launched := codeEnv(t, "/fake/bin/code")
	target := filepath.Join(home, "dotfiles-ssh-config")
	if err := os.WriteFile(target, []byte("Host x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, ".ssh", "config")); err != nil {
		t.Fatal(err)
	}
	m := codeModel(t)

	m, cmd := pressCode(t, m)
	if m.confirm != nil {
		t.Fatal("a symlinked config must not raise the consent overlay")
	}
	m = runCode(t, m, cmd)

	if got := readFile(t, target); got != "Host x\n" {
		t.Fatalf("symlink target changed: %q", got)
	}
	if len(*launched) != 1 {
		t.Fatalf("VS Code should be launched, got %v", *launched)
	}
	log := loggedText(m)
	if !strings.Contains(log, "because it is a symlink") || !strings.Contains(log, sshconfig.IncludeLine()) {
		t.Fatalf("the log should explain the symlink and give the line, got:\n%s", log)
	}
}

// No `code` command: nothing is launched and the user is told which alias to
// pick in Remote-SSH.
func TestCodeVerbWithoutCodeCommandNamesTheAlias(t *testing.T) {
	_, launched := codeEnv(t, "")
	m := codeModel(t)
	m, _ = pressCode(t, m)
	m, cmd := pressDispatch(t, m, runeKey('n'))
	m = runCode(t, m, cmd)

	if len(*launched) != 0 {
		t.Fatalf("nothing should be launched without a code command, got %v", *launched)
	}
	if got := m.lastMessage(); !strings.Contains(got, "Remote-SSH: Connect to Host") || !strings.Contains(got, "lima-web") {
		t.Fatalf("last message should name the alias for Remote-SSH, got %q", got)
	}
}

// The verb is offered only on a running VM that no build owns.
func TestCodeVerbOfferedOnlyOnRunningIdleVM(t *testing.T) {
	codeEnv(t, "/fake/bin/code")
	m := codeModel(t)
	has := func(m model) bool {
		for _, b := range m.boardHelp() {
			if b.Help().Key == "c" {
				return true
			}
		}
		return false
	}
	if !has(m) {
		t.Fatal("c code should be offered on a running VM")
	}
	stopped := putOnBoard(t, m, vm.VM{Name: "web", Status: "Stopped"})
	if has(stopped) {
		t.Fatal("c code must not be offered on a stopped VM")
	}
	if _, cmd := pressDispatch(t, stopped, runeKey('c')); cmd != nil {
		t.Fatal("c on a stopped VM must dispatch nothing")
	}
}

// The check's answer is acted on for the VM `c` was pressed on, even if the
// ring has moved since; and an answer that arrives while another question is
// up does not replace it.
func TestCodeVerbCheckIsPinnedToTheVM(t *testing.T) {
	_, launched := codeEnv(t, "/fake/bin/code")
	m := codeModel(t)
	m = putOnBoard(t, m, vm.VM{Name: "api", Status: "Running"})
	m.focusVM.Name = "web"

	m, cmd := pressDispatch(t, m, runeKey('c'))
	checked := cmd().(codeIncludeCheckedMsg)
	m.focusVM.Name = "api"
	next, _ := m.handleCodeIncludeChecked(checked)
	m = next.(model)
	if m.confirm == nil || m.confirm.vmName != "web" {
		t.Fatalf("the overlay should be about web, the VM c was pressed on; got %+v", m.confirm)
	}
	m, cmd = pressDispatch(t, m, runeKey('n'))
	m = runCode(t, m, cmd)
	if len(*launched) != 1 || (*launched)[0][2] != "ssh-remote+lima-web" {
		t.Fatalf("launch = %v, want web's alias", *launched)
	}

	// A second answer landing while a question is already open leaves it be.
	other := &confirmState{prompt: "Delete \"api\"?", vmName: "api"}
	m.confirm = other
	next, cmd = m.handleCodeIncludeChecked(checked)
	m = next.(model)
	if m.confirm != other || cmd != nil {
		t.Fatal("a late check answer must not replace an open question")
	}
	if !strings.Contains(m.lastMessage(), "press c again") {
		t.Fatalf("the dropped answer should be reported, got %q", m.lastMessage())
	}
}

// An Include line ssh does not apply raises the overlay with the reason
// logged first, so the question does not read as sandbar not having seen it.
func TestCodeVerbMisplacedIncludeExplainsTheQuestion(t *testing.T) {
	home, _ := codeEnv(t, "/fake/bin/code")
	old := sshconfig.QuerySSH
	t.Cleanup(func() { sshconfig.QuerySSH = old })
	sshconfig.QuerySSH = func(context.Context, ...string) ([]byte, error) {
		return []byte("hostname " + sshconfig.IncludeProbeHost + "\n"), nil
	}
	userCfg := filepath.Join(home, ".ssh", "config")
	if err := os.MkdirAll(filepath.Dir(userCfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userCfg, []byte("Host x\n  "+sshconfig.IncludeLine()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, cmd := pressCode(t, codeModel(t))
	if m.confirm == nil || cmd != nil {
		t.Fatal("a misplaced Include should raise the consent overlay")
	}
	if !strings.Contains(loggedText(m), "is not applied (it may be inside a Host or Match block)") {
		t.Fatalf("the log should say why it is asking, got:\n%s", loggedText(m))
	}
}

// deliverCleanup runs what a result handed off the Update goroutine and feeds
// the entry-removal outcome back through Update, as the program would.
// Anything else batched beside it runs against the fakes and is dropped.
func deliverCleanup(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	if cmd == nil {
		t.Fatal("the result dispatched nothing; the entry cleanup is missing")
	}
	// Update batches its own follow-ups around the handler's, so the removal
	// can be nested a level or two down.
	var find func(tea.Cmd) (editorEntriesRemovedMsg, bool)
	find = func(c tea.Cmd) (editorEntriesRemovedMsg, bool) {
		if c == nil {
			return editorEntriesRemovedMsg{}, false
		}
		switch msg := c().(type) {
		case editorEntriesRemovedMsg:
			return msg, true
		case tea.BatchMsg:
			for _, inner := range msg {
				if res, ok := find(inner); ok {
					return res, true
				}
			}
		}
		return editorEntriesRemovedMsg{}, false
	}
	res, ok := find(cmd)
	if !ok {
		t.Fatal("no editorEntriesRemovedMsg came out of the result")
	}
	next, _ := m.Update(res)
	return next.(model)
}

// twoProfileFleet is a local member and a remote one, each with a VM "web".
func twoProfileFleet(t *testing.T) (model, registry.Scope) {
	t.Helper()
	remote := registry.Scope{Provider: "lima-remote", RemoteTarget: "u@mini:22"}
	fleet := provider.Fleet{
		{Profile: profiles.Profile{ID: profiles.LocalProfileID, Name: "local", Type: profiles.TypeLocal, Enabled: true}, Prov: &providerfake.Provider{}, Scope: registry.LocalScope},
		{Profile: profiles.Profile{ID: "mini", Name: "Mac Mini", Type: profiles.TypeRemoteSSH, Enabled: true}, Prov: &providerfake.Provider{}, Scope: remote},
	}
	for _, e := range []sshconfig.Entry{
		{Alias: sshconfig.LimaAlias("web"), Include: "/lima/web/ssh.config"},
		{Alias: sshconfig.ProfileAlias("mini", "web"), HostName: "127.0.0.1", User: "tester"},
	} {
		if err := sshconfig.Upsert(e); err != nil {
			t.Fatal(err)
		}
	}
	return New(fleet).(model), remote
}

// A board delete removes the VM's host entry, by the alias it was written
// under (vscode.Alias): Lima's name on the Local profile, the
// profile-namespaced one elsewhere — and never a same-named VM's entry under
// the other profile.
func TestDeleteRemovesTheVMsSSHEntry(t *testing.T) {
	codeEnv(t, "")
	m, remote := twoProfileFleet(t)

	next, cmd := m.Update(actionDoneMsg{action: "delete", name: "web", scope: remote})
	m = deliverCleanup(t, next.(model), cmd)
	managed := readFile(t, sshconfig.ManagedPath())
	if strings.Contains(managed, sshconfig.ProfileAlias("mini", "web")) {
		t.Fatalf("the remote VM's entry should be gone, got:\n%s", managed)
	}
	if !strings.Contains(managed, "lima-web") {
		t.Fatalf("the local same-named VM's entry must survive, got:\n%s", managed)
	}

	next, cmd = m.Update(actionDoneMsg{action: "delete", name: "web", scope: registry.LocalScope})
	m = deliverCleanup(t, next.(model), cmd)
	if strings.Contains(readFile(t, sshconfig.ManagedPath()), "lima-web") {
		t.Fatal("the local VM's entry should be gone after its delete")
	}
	if got := m.lastMessage(); got != "delete web ok" {
		t.Fatalf("a clean delete should report plainly, got %q", got)
	}
}

// A VM the registry knew that vanished outside sandbar loses its entry when
// the board's reconcile drops it, under its own profile only.
func TestReconcileRemovesDroppedVMsSSHEntries(t *testing.T) {
	codeEnv(t, "")
	m, remote := twoProfileFleet(t)
	if err := m.reg.AddScoped(vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}, remote); err != nil {
		t.Fatal(err)
	}
	next, cmd := m.Update(vmsLoadedMsg{scope: remote, vms: nil})
	deliverCleanup(t, next.(model), cmd)
	managed := readFile(t, sshconfig.ManagedPath())
	if strings.Contains(managed, sshconfig.ProfileAlias("mini", "web")) || !strings.Contains(managed, "lima-web") {
		t.Fatalf("want only the remote web's entry removed, got:\n%s", managed)
	}
}

// Deleting a profile removes every entry under its ID — no later delete or
// reconcile could, since the ID never comes back — and leaves Local's alone.
func TestProfileDeleteRemovesItsSSHEntries(t *testing.T) {
	codeEnv(t, "")
	p := seedRemoteProfile(t, "build-host", "example.com", "dev", 22)
	_, scope, err := buildProfileProvider(p)
	if err != nil {
		t.Fatal(err)
	}
	m := New(provider.Fleet{
		{Profile: profiles.Profile{ID: profiles.LocalProfileID, Type: profiles.TypeLocal, Enabled: true}, Prov: &providerfake.Provider{}, Scope: registry.LocalScope},
		{Profile: p, Prov: &providerfake.Provider{}, Scope: scope},
	}).(model)
	for _, alias := range []string{"lima-web", vscode.Alias(p, "web"), vscode.Alias(p, "api")} {
		if err := sshconfig.Upsert(sshconfig.Entry{Alias: alias, HostName: "10.0.0.1"}); err != nil {
			t.Fatal(err)
		}
	}
	cmd := m.deleteProfile(p.ID)
	deliverCleanup(t, m, cmd)
	managed := readFile(t, sshconfig.ManagedPath())
	if strings.Contains(managed, sshconfig.ProfileAliasPrefix(p.ID)) || !strings.Contains(managed, "# sandbar end lima-web") {
		t.Fatalf("want only the deleted profile's entries removed, got:\n%s", managed)
	}
}

// A failed entry removal is a warning naming the entries, never silence: the
// stale entry may point at an address another machine now holds.
func TestDeleteEntryRemovalFailureWarns(t *testing.T) {
	codeEnv(t, "")
	m := codeModel(t)
	next, _ := m.Update(editorEntriesRemovedMsg{what: "delete web: ssh host entry lima-web", err: errors.New("disk full")})
	if got := loggedText(next.(model)); !strings.Contains(got, "delete web: ssh host entry lima-web not removed: disk full") {
		t.Fatalf("log = %q", got)
	}
}

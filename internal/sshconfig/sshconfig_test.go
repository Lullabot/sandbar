package sshconfig

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// isolate points HOME and XDG_CONFIG_HOME at fresh temp dirs so no test can
// read or rewrite the developer's real ~/.ssh/config or sandbar config.
func isolate(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	return home
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestRenderIncludeEntry(t *testing.T) {
	got, err := Entry{Alias: "lima-foo", Include: "/home/u/.lima/foo/ssh.config", HostName: "ignored"}.Render()
	if err != nil {
		t.Fatal(err)
	}
	want := "# sandbar begin lima-foo\nMatch all\nInclude \"/home/u/.lima/foo/ssh.config\"\n# sandbar end lima-foo\n"
	if got != want {
		t.Fatalf("render:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderHostEntry(t *testing.T) {
	e := Entry{
		Alias:             "sandbar-pve-foo",
		HostName:          "10.0.0.5",
		User:              "dev",
		Port:              2222,
		IdentityFile:      "/home/u/my keys/id_ed25519",
		IdentitiesOnly:    true,
		EphemeralHostKeys: true,
		AuthorizedKey:     "ssh-ed25519 AAAA never-rendered",
	}
	got, err := e.Render()
	if err != nil {
		t.Fatal(err)
	}
	want := `# sandbar begin sandbar-pve-foo
Match all
Host sandbar-pve-foo
  HostName 10.0.0.5
  Port 2222
  User dev
  IdentityFile "/home/u/my keys/id_ed25519"
  IdentitiesOnly yes
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
  LogLevel ERROR
# sandbar end sandbar-pve-foo
`
	if got != want {
		t.Fatalf("render:\n%s\nwant:\n%s", got, want)
	}
	// Port 22 is ssh's default; writing it would only be noise.
	e.Port = 22
	got, _ = e.Render()
	if strings.Contains(got, "Port") {
		t.Fatalf("port 22 should be omitted:\n%s", got)
	}
}

// TestRenderRejectsInjection: every value comes from a VM, a profile or a
// guest, and a newline in any of them would let that source write its own
// directives (a ProxyCommand, say) into the user's ssh configuration.
func TestRenderRejectsInjection(t *testing.T) {
	cases := map[string]Entry{
		"alias newline":       {Alias: "foo\nHost *", HostName: "h"},
		"alias space":         {Alias: "foo bar", HostName: "h"},
		"alias wildcard":      {Alias: "*", HostName: "h"},
		"alias empty":         {Alias: "", HostName: "h"},
		"hostname newline":    {Alias: "a", HostName: "h\n  ProxyCommand evil"},
		"hostname space":      {Alias: "a", HostName: "h ProxyCommand"},
		"user cr":             {Alias: "a", User: "u\rx"},
		"identity newline":    {Alias: "a", IdentityFile: "/k\nProxyCommand evil"},
		"identity quote":      {Alias: "a", IdentityFile: `/k" ProxyCommand "evil`},
		"include tab-control": {Alias: "a", Include: "/p\x00x"},
		"include quote":       {Alias: "a", Include: `/p"x`},
		"proxy newline":       {Alias: "a", ProxyCommand: "ssh -W %h:%p j\nUser root"},
		"proxy escape":        {Alias: "a", ProxyCommand: "ssh \x1b[2J"},
		"port range":          {Alias: "a", Port: 70000},
		"unicode line sep":    {Alias: "a", HostName: "h x"},
	}
	for name, e := range cases {
		if _, err := e.Render(); err == nil {
			t.Errorf("%s: rendered without error", name)
		}
	}
	isolate(t)
	if err := Upsert(Entry{Alias: "a", HostName: "h\nHost *"}); err == nil {
		t.Fatal("Upsert accepted an injecting entry")
	}
	if _, err := os.Stat(ManagedPath()); !os.IsNotExist(err) {
		t.Fatalf("a rejected entry must not create the managed file (stat err %v)", err)
	}
}

func TestUpsertReplaceRemoveRoundTrip(t *testing.T) {
	isolate(t)
	a := Entry{Alias: "sandbar-pve-a", HostName: "10.0.0.1", User: "dev"}
	b := Entry{Alias: "lima-b", Include: "/l/b/ssh.config"}
	for _, e := range []Entry{a, b} {
		if err := Upsert(e); err != nil {
			t.Fatal(err)
		}
	}
	fi, err := os.Stat(ManagedPath())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("managed file mode %v, want 0600", fi.Mode().Perm())
	}

	// Replacing a must keep it in place (before b) and leave one copy.
	a.HostName = "10.0.0.9"
	if err := Upsert(a); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, ManagedPath())
	if strings.Count(got, "# sandbar begin sandbar-pve-a") != 1 || strings.Contains(got, "10.0.0.1\n") {
		t.Fatalf("replace left a stale or duplicate block:\n%s", got)
	}
	if strings.Index(got, "sandbar-pve-a") > strings.Index(got, "lima-b") || !strings.Contains(got, "HostName 10.0.0.9") {
		t.Fatalf("replace moved or lost the block:\n%s", got)
	}

	// Upserting identical content is a byte-for-byte no-op.
	if err := Upsert(a); err != nil {
		t.Fatal(err)
	}
	if again := readFile(t, ManagedPath()); again != got {
		t.Fatalf("idempotent upsert changed the file:\n%s\nwas:\n%s", again, got)
	}

	if err := Remove("sandbar-pve-a"); err != nil {
		t.Fatal(err)
	}
	got = readFile(t, ManagedPath())
	if strings.Contains(got, "sandbar-pve-a") || !strings.Contains(got, "# sandbar end lima-b") {
		t.Fatalf("remove dropped the wrong block:\n%s", got)
	}
	// Removing an absent alias, or from an absent file, is not an error.
	if err := Remove("nope"); err != nil {
		t.Fatal(err)
	}
	if err := Remove("lima-b"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFile(t, ManagedPath()), "sandbar begin") {
		t.Fatal("blocks remain after removing every alias")
	}
}

// TestUpsertRepairsTruncatedBlock: a begin marker with no matching end (a
// hand edit gone wrong) must not swallow the next entry or survive as junk.
func TestUpsertRepairsTruncatedBlock(t *testing.T) {
	isolate(t)
	path := ManagedPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	broken := "# sandbar begin x\nMatch all\nHost x\n  HostName old\n" +
		"# sandbar begin y\nMatch all\nInclude \"/y\"\n# sandbar end y\n"
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Upsert(Entry{Alias: "x", HostName: "new"}); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if strings.Contains(got, "old") || !strings.Contains(got, "# sandbar end y") || strings.Count(got, "begin x") != 1 {
		t.Fatalf("truncated block not repaired cleanly:\n%s", got)
	}
}

func TestAliases(t *testing.T) {
	for in, want := range map[string]string{
		"Home Proxmox": "sandbar-home-proxmox-vm1",
		"--Mac_Mini!!": "sandbar-mac-mini-vm1",
		"":             "sandbar-profile-vm1",
		"Ünïcode":      "sandbar-n-code-vm1",
	} {
		if got := ProfileAlias(in, "vm1"); got != want {
			t.Errorf("ProfileAlias(%q) = %q, want %q", in, got, want)
		}
	}
	if got := LimaAlias("foo"); got != "lima-foo" {
		t.Errorf("LimaAlias = %q", got)
	}
}

func TestIncludeStatusDetection(t *testing.T) {
	home := isolate(t)
	st, err := IncludeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if st.Present || st.Exists || st.Symlink || !st.Writable {
		t.Fatalf("missing config: got %+v", st)
	}

	rel := "~/" + strings.TrimPrefix(ManagedPath(), home+"/")
	for _, line := range []string{
		"Include " + ManagedPath(),
		`  include "` + ManagedPath() + `"`,
		"INCLUDE=" + rel,
		"Include ~/.lima/*/ssh.config " + rel,
	} {
		writeUserConfig(t, line+"\nHost x\n")
		if st, _ := IncludeStatus(); !st.Present {
			t.Errorf("%q not detected", line)
		}
	}
	for _, line := range []string{
		"# Include " + ManagedPath(),
		"Include " + ManagedPath() + ".bak",
		"IncludeFoo " + ManagedPath(),
	} {
		writeUserConfig(t, line+"\n")
		if st, _ := IncludeStatus(); st.Present {
			t.Errorf("%q falsely detected", line)
		}
	}
}

func writeUserConfig(t *testing.T, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(UserConfigPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(UserConfigPath(), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAddIncludeCreatesMissingConfig(t *testing.T) {
	home := isolate(t)
	if err := AddInclude(); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{filepath.Join(home, ".ssh"): 0o700, UserConfigPath(): 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode %v, want %v", path, fi.Mode().Perm(), want)
		}
	}
	got := readFile(t, UserConfigPath())
	if !strings.HasPrefix(got, "# Added by sandbar") || !strings.Contains(got, IncludeLine()+"\n") {
		t.Fatalf("unexpected config:\n%s", got)
	}
	// A second call must not stack another copy.
	if err := AddInclude(); err != nil {
		t.Fatal(err)
	}
	if again := readFile(t, UserConfigPath()); again != got {
		t.Fatalf("AddInclude is not idempotent:\n%s", again)
	}
}

func TestAddIncludeRefusesSymlinkAndUnwritable(t *testing.T) {
	isolate(t)
	real := filepath.Join(t.TempDir(), "dotfiles-ssh-config")
	if err := os.WriteFile(real, []byte("Host x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(UserConfigPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, UserConfigPath()); err != nil {
		t.Fatal(err)
	}
	st, err := IncludeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Symlink || !st.Exists {
		t.Fatalf("symlink not reported: %+v", st)
	}
	if err := AddInclude(); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink: want an error naming the symlink, got %v", err)
	}
	if readFile(t, real) != "Host x\n" {
		t.Fatal("the symlink target was modified")
	}

	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	if err := os.Remove(UserConfigPath()); err != nil {
		t.Fatal(err)
	}
	writeUserConfig(t, "Host x\n")
	if err := os.Chmod(UserConfigPath(), 0o400); err != nil {
		t.Fatal(err)
	}
	if st, _ := IncludeStatus(); st.Writable {
		t.Fatal("read-only config reported writable")
	}
	if err := AddInclude(); err == nil || !strings.Contains(err.Error(), "not writable") {
		t.Fatalf("unwritable: want an error saying it is not writable, got %v", err)
	}
}

// TestSSHResolvesManagedEntries is the boundary the feature actually cares
// about: the real ssh client, reading a user config that had Host blocks of
// its own before sandbar prepended the Include, resolves both a local-Lima
// Include entry and a Host-options entry from ONE managed file. It is what
// proves the prepend and the per-block "Match all" scope reset work; a text
// comparison of the files could not.
func TestSSHResolvesManagedEntries(t *testing.T) {
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("ssh not installed")
	}
	home := isolate(t)

	// Lima's own per-instance file, as limactl writes it.
	limaDir := filepath.Join(home, "lima home", "foo")
	if err := os.MkdirAll(limaDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(limaDir, "ssh.config"),
		[]byte("Host lima-foo\n  Hostname 127.0.0.1\n  Port 60022\n  User u\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The pre-existing user config ends inside a Host block: had the Include
	// been appended, it would only apply to "work".
	writeUserConfig(t, "Host work\n  HostName work.example.com\n  User me\n")

	// A key path with a space (needs quoting) and a percent sign (a token
	// introducer ssh would otherwise try to expand).
	key := filepath.Join(home, "my keys%1", "id_ed25519")
	pve := ProfileAlias("Home PVE", "bar")
	if err := Upsert(Entry{
		Alias:             pve,
		HostName:          "127.0.0.1",
		User:              "dev",
		Port:              2200,
		IdentityFile:      key,
		IdentitiesOnly:    true,
		EphemeralHostKeys: true,
		ProxyCommand:      "ssh -p 2222 jump.example.com -W %h:%p",
	}); err != nil {
		t.Fatal(err)
	}
	// The Include entry goes AFTER the Host entry: without the per-block
	// "Match all" it would be scoped to the Proxmox alias and lima-foo would
	// not resolve.
	if err := Upsert(Entry{Alias: LimaAlias("foo"), Include: filepath.Join(limaDir, "ssh.config")}); err != nil {
		t.Fatal(err)
	}
	if err := AddInclude(); err != nil {
		t.Fatal(err)
	}

	resolve := func(alias string) map[string]string {
		t.Helper()
		out, err := exec.Command(sshBin, "-F", UserConfigPath(), "-G", alias).CombinedOutput()
		if err != nil {
			t.Fatalf("ssh -G %s: %v\n%s", alias, err, out)
		}
		kv := map[string]string{}
		for _, line := range strings.Split(string(out), "\n") {
			k, v, _ := strings.Cut(line, " ")
			if _, seen := kv[k]; !seen { // first identityfile wins
				kv[k] = v
			}
		}
		return kv
	}
	check := func(alias string, want map[string]string) {
		t.Helper()
		got := resolve(alias)
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %s = %q, want %q", alias, k, got[k], v)
			}
		}
	}
	check("lima-foo", map[string]string{"hostname": "127.0.0.1", "port": "60022", "user": "u"})
	check(pve, map[string]string{
		"hostname": "127.0.0.1",
		"port":     "2200",
		"user":     "dev",
		// -G prints IdentityFile before token expansion, so the doubled
		// percent shows here; at connect time ssh expands it back to the
		// literal path (an undoubled "%1" makes ssh fail: "unknown key").
		"identityfile":          strings.ReplaceAll(key, "%", "%%"),
		"identitiesonly":        "yes",
		"stricthostkeychecking": "false",
		"userknownhostsfile":    "/dev/null",
		"loglevel":              "ERROR",
		"proxycommand":          "ssh -p 2222 jump.example.com -W %h:%p",
	})
	// The user's own block still resolves, untouched by the prepend.
	check("work", map[string]string{"hostname": "work.example.com", "user": "me"})
}

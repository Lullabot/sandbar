package provider

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/sshconfig"
	"github.com/lullabot/sandbar/internal/sshx"
	"github.com/lullabot/sandbar/internal/vm"
)

const testPubKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGZha2VrZXlmb3J0ZXN0aW5nb25seQ dev@laptop"

// guestShellRunner stands in for `limactl shell` on the remote host. Rather
// than only recording the argv, it RUNS the guest command locally with HOME
// pointed at a temp "guest home", so the test checks what the literal script
// actually does to authorized_keys — the far side the feature exists to change
// — not merely that some script was sent.
type guestShellRunner struct {
	t         *testing.T
	guestHome string
	calls     [][]string
	stdins    []string
}

func (r *guestShellRunner) Output(context.Context, ...string) ([]byte, error) { return nil, nil }

func (r *guestShellRunner) StreamOut(context.Context, io.Reader, io.Writer, ...string) error {
	return nil
}

func (r *guestShellRunner) Stream(ctx context.Context, stdin io.Reader, out io.Writer, args ...string) error {
	r.calls = append(r.calls, args)
	var in []byte
	if stdin != nil {
		var err error
		if in, err = io.ReadAll(stdin); err != nil {
			return err
		}
	}
	r.stdins = append(r.stdins, string(in))
	if len(args) < 3 || args[0] != "shell" {
		r.t.Fatalf("unexpected limactl call %v", args)
	}
	cmd := exec.CommandContext(ctx, args[2], args[3:]...)
	cmd.Env = []string{"HOME=" + r.guestHome, "PATH=" + os.Getenv("PATH")}
	cmd.Stdin = bytes.NewReader(in)
	cmd.Stdout, cmd.Stderr = out, out
	return cmd.Run()
}

// newRemoteForEditorTest builds a remote-Lima provider whose guest transport
// is guestShellRunner and whose host files are local, so the VM's instance
// directory (and the guest user in its ssh.config) can live in a temp dir.
func newRemoteForEditorTest(t *testing.T, conn lima.SSHConfig) (*remoteLimaProvider, *guestShellRunner, vm.VM) {
	t.Helper()
	r := &guestShellRunner{t: t, guestHome: t.TempDir()}
	core := lima.New(r)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ssh.config"), []byte("Host lima-web\n  User dev\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &remoteLimaProvider{
		limaProvider: &limaProvider{core: core, hostFiles: lima.LocalFiles()},
		conn:         conn,
		profileName:  "Mac Mini",
	}
	return p, r, vm.VM{Name: "web", Dir: dir}
}

// writeKeyPair creates a fake key pair at base (private) and base.pub.
func writeKeyPair(t *testing.T, base, pub string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(base), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base, []byte("PRIVATE KEY MATERIAL\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pub != "" {
		if err := os.WriteFile(base+".pub", []byte(pub+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRemoteLimaEditorSSHAuthorizesKeyAndProxies is the end-to-end check: the
// workstation public key reaches the guest's authorized_keys on stdin only
// (never in the argv, which ends up in process listings and debug logs), a
// second open adds no duplicate, and the entry tunnels through the remote host
// to Lima's own per-instance ssh config.
func TestRemoteLimaEditorSSHAuthorizesKeyAndProxies(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	key := filepath.Join(home, "keys", "mini key")
	writeKeyPair(t, key, testPubKey)

	p, r, v := newRemoteForEditorTest(t, lima.SSHConfig{Host: "mini.local", User: "andrew", Port: 2222, IdentityPath: key})
	// A realistic remote instance path for the ProxyCommand; the guest user is
	// still read from the seeded local ssh.config.
	local := v.Dir
	v.Dir = "/Users/andrew/.lima/web"
	p.hostFiles = remapFiles{from: v.Dir, to: local}

	var got sshconfig.Entry
	for i := 0; i < 2; i++ {
		var err error
		got, err = p.EditorSSH(context.Background(), v)
		if err != nil {
			t.Fatalf("EditorSSH #%d: %v", i+1, err)
		}
	}

	wantProxy := `ssh -p 2222 -i '` + key + `' andrew@mini.local -- 'ssh -F /Users/andrew/.lima/web/ssh.config -W 127.0.0.1:22 lima-web'`
	want := sshconfig.Entry{
		Alias:             "sandbar-mac-mini-web",
		HostName:          "127.0.0.1",
		User:              "dev",
		IdentityFile:      key,
		ProxyCommand:      wantProxy,
		IdentitiesOnly:    true,
		EphemeralHostKeys: true,
		AuthorizedKey:     key + ".pub",
	}
	if got != want {
		t.Fatalf("EditorSSH =\n %+v\nwant\n %+v", got, want)
	}

	for i, args := range r.calls {
		if strings.Contains(strings.Join(args, " "), "AAAAC3Nza") {
			t.Fatalf("call %d carries key material in its argv: %v", i, args)
		}
		if r.stdins[i] != testPubKey+"\n" {
			t.Fatalf("call %d stdin = %q, want the public key line", i, r.stdins[i])
		}
	}
	ak := filepath.Join(r.guestHome, ".ssh", "authorized_keys")
	data, err := os.ReadFile(ak)
	if err != nil {
		t.Fatalf("authorized_keys not written: %v", err)
	}
	if n := strings.Count(string(data), testPubKey); n != 1 {
		t.Fatalf("authorized_keys holds %d copies of the key after two opens, want 1:\n%s", n, data)
	}
	for path, mode := range map[string]os.FileMode{filepath.Dir(ak): 0o700, ak: 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != mode {
			t.Fatalf("%s mode = %v, want %v", path, fi.Mode().Perm(), mode)
		}
	}

	if _, err := got.Render(); err != nil {
		t.Fatalf("the entry must render: %v", err)
	}
}

// TestRemoteLimaEditorSSHProxyCommandResolves runs the rendered entry through
// the real ssh client, with a stand-in `ssh` first on PATH that records the
// argv the ProxyCommand starts it with. Two of the three parsers involved are
// therefore real — ssh's own %-token expansion (a literal percent, here in a
// Lima home path, must be doubled in the file and arrive single) and /bin/sh —
// and the third, the remote login shell, is reproduced with sh's own word
// splitting. `ssh -G` would not do: it prints a ProxyCommand unexpanded.
func TestRemoteLimaEditorSSHProxyCommandResolves(t *testing.T) {
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("ssh not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeKeyPair(t, filepath.Join(home, ".ssh", "id_ed25519"), testPubKey)

	p, _, v := newRemoteForEditorTest(t, lima.SSHConfig{Host: "mini.local", User: "dev", Port: 2222})
	remoteDir := "/srv/lima 100%/my web"
	local := v.Dir
	v.Dir = remoteDir
	p.hostFiles = remapFiles{from: remoteDir, to: local}

	e, err := p.EditorSSH(context.Background(), v)
	if err != nil {
		t.Fatalf("EditorSSH: %v", err)
	}
	block, err := e.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	cfg := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(cfg, []byte(block), 0o600); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	record := filepath.Join(t.TempDir(), "argv")
	fake := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + sshx.Quote(record) + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sshBin, "-F", cfg, e.Alias, "true")
	cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"}
	out, _ := cmd.CombinedOutput() // the stand-in exits 1, so ssh fails by design
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("ProxyCommand never ran the stand-in ssh: %v\nssh said: %s\nblock:\n%s", err, out, block)
	}
	outer := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	wantOuter := []string{"-p", "2222", "dev@mini.local", "--", `ssh -F '/srv/lima 100%/my web/ssh.config' -W 127.0.0.1:22 lima-web`}
	if !slices.Equal(outer, wantOuter) {
		t.Fatalf("outer ssh argv = %q\nwant %q\nblock:\n%s", outer, wantOuter, block)
	}

	// The remote shell re-parses the one command argument into the inner argv.
	inner, err := exec.Command("sh", "-c", `eval "set -- $1"; printf '%s\n' "$@"`, "sh", outer[len(outer)-1]).Output()
	if err != nil {
		t.Fatal(err)
	}
	wantInner := "ssh\n-F\n/srv/lima 100%/my web/ssh.config\n-W\n127.0.0.1:22\nlima-web\n"
	if string(inner) != wantInner {
		t.Fatalf("remote shell would run %q, want %q", inner, wantInner)
	}
}

// TestRemoteLimaEditorSSHRefusesUnquotableValues proves a quote or backslash
// in anything the ProxyCommand carries is refused up front, before the guest
// is touched. OpenSSH validates a config line's quoting with its own rules,
// under which a backslash inside single quotes is an escape, so the nested
// POSIX quoting such a value would need is rejected as "invalid quotes" —
// and that error would break every Host in the managed file, not just this one.
func TestRemoteLimaEditorSSHRefusesUnquotableValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		conn lima.SSHConfig
		dir  string
		key  string // identity file name under HOME, if any
	}{
		{"dir", lima.SSHConfig{Host: "mini.local"}, "/srv/it's/web", ""},
		{"user", lima.SSHConfig{Host: "mini.local", User: `o'brien`}, "", ""},
		{"identity", lima.SSHConfig{Host: "mini.local"}, "", `a"b`},
		{"host", lima.SSHConfig{Host: `mini\local`}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			writeKeyPair(t, filepath.Join(home, ".ssh", "id_ed25519"), testPubKey)
			if tc.key != "" {
				tc.conn.IdentityPath = filepath.Join(home, tc.key)
				writeKeyPair(t, tc.conn.IdentityPath, testPubKey)
			}
			p, r, v := newRemoteForEditorTest(t, tc.conn)
			if tc.dir != "" {
				p.hostFiles = remapFiles{from: tc.dir, to: v.Dir}
				v.Dir = tc.dir
			}
			if _, err := p.EditorSSH(context.Background(), v); err == nil || !strings.Contains(err.Error(), "quote or backslash") {
				t.Fatalf("EditorSSH err = %v, want a quote/backslash refusal", err)
			}
			if len(r.calls) != 0 {
				t.Fatalf("guest was contacted: %v", r.calls)
			}
		})
	}
}

// TestRemoteLimaEditorSSHKeySelection covers which workstation key is chosen
// and the refusals: a profile identity without its .pub, no default key pair
// at all, and a .pub that is not a single public-key line (which the guest
// script would otherwise append verbatim to authorized_keys).
func TestRemoteLimaEditorSSHKeySelection(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, home string) string // returns profile identity path
		wantKey string                                 // relative to home; "" means an error
		wantErr string
	}{
		{"profile identity", func(t *testing.T, home string) string {
			writeKeyPair(t, filepath.Join(home, ".ssh", "id_ed25519"), testPubKey)
			writeKeyPair(t, filepath.Join(home, "k", "mini"), testPubKey)
			return filepath.Join(home, "k", "mini")
		}, "k/mini", ""},
		{"profile identity without pub", func(t *testing.T, home string) string {
			writeKeyPair(t, filepath.Join(home, ".ssh", "id_ed25519"), testPubKey)
			writeKeyPair(t, filepath.Join(home, "k", "mini"), "")
			return filepath.Join(home, "k", "mini")
		}, "", "mini.pub"},
		{"first complete default pair", func(t *testing.T, home string) string {
			writeKeyPair(t, filepath.Join(home, ".ssh", "id_ed25519"), "") // no .pub: skipped
			writeKeyPair(t, filepath.Join(home, ".ssh", "id_ecdsa"), "ecdsa-sha2-nistp256 AAAAE2VjZHNh x")
			writeKeyPair(t, filepath.Join(home, ".ssh", "id_rsa"), "ssh-rsa AAAAB3Nza x")
			return ""
		}, ".ssh/id_ecdsa", ""},
		{"no key pair", func(t *testing.T, home string) string { return "" }, "", "identity path"},
		{"multi-line pub", func(t *testing.T, home string) string {
			writeKeyPair(t, filepath.Join(home, ".ssh", "id_ed25519"), testPubKey+"\ncommand=\"rm -rf ~\" "+testPubKey)
			return ""
		}, "", "public key"},
		{"not a public key", func(t *testing.T, home string) string {
			writeKeyPair(t, filepath.Join(home, ".ssh", "id_ed25519"), "-----BEGIN OPENSSH PRIVATE KEY-----")
			return ""
		}, "", "public key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			id := tc.setup(t, home)
			p, r, v := newRemoteForEditorTest(t, lima.SSHConfig{Host: "mini.local", IdentityPath: id})

			got, err := p.EditorSSH(context.Background(), v)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("EditorSSH err = %v, want one mentioning %q", err, tc.wantErr)
				}
				if len(r.calls) != 0 {
					t.Fatalf("a refused key still reached the guest: %v", r.calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("EditorSSH: %v", err)
			}
			if want := filepath.Join(home, tc.wantKey); got.IdentityFile != want || got.AuthorizedKey != want+".pub" {
				t.Fatalf("key = %q / %q, want %q", got.IdentityFile, got.AuthorizedKey, want)
			}
		})
	}
}

// TestRemoteLimaEditorSSHGuestFailure proves a failed authorization is an
// error carrying the guest's output, and no entry: an alias whose key was
// never authorized would only fail later with a bare "Permission denied".
func TestRemoteLimaEditorSSHGuestFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeKeyPair(t, filepath.Join(home, ".ssh", "id_ed25519"), testPubKey)
	p, r, v := newRemoteForEditorTest(t, lima.SSHConfig{Host: "mini.local"})
	// A regular file where ~/.ssh should be makes mkdir -p fail in the guest.
	if err := os.WriteFile(filepath.Join(r.guestHome, ".ssh"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := p.EditorSSH(context.Background(), v)
	if err == nil {
		t.Fatalf("EditorSSH = %+v despite a failed guest write", got)
	}
	if !strings.Contains(err.Error(), "mkdir") {
		t.Fatalf("error %q does not carry the guest's output", err)
	}
}

// TestRemoteLimaEditorSSHExpandsTildeIdentity proves a profile identity saved
// as "~/…" works: sandbar's other remote connections pass it to `ssh -i`,
// which expands the tilde itself, but the public half is read directly here.
func TestRemoteLimaEditorSSHExpandsTildeIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	key := filepath.Join(home, "keys", "mini")
	writeKeyPair(t, key, testPubKey)
	p, _, v := newRemoteForEditorTest(t, lima.SSHConfig{Host: "mini.local", IdentityPath: "~/keys/mini"})
	got, err := p.EditorSSH(context.Background(), v)
	if err != nil {
		t.Fatalf("EditorSSH: %v", err)
	}
	if got.IdentityFile != key || got.AuthorizedKey != key+".pub" {
		t.Fatalf("key = %q / %q, want %q", got.IdentityFile, got.AuthorizedKey, key)
	}
}

// TestRemoteLimaEditorSSHAppendsAfterUnterminatedLine proves the guest script
// does not fuse the new key onto an authorized_keys whose last line lacks a
// newline, which would break that existing key as well as the new one.
func TestRemoteLimaEditorSSHAppendsAfterUnterminatedLine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeKeyPair(t, filepath.Join(home, ".ssh", "id_ed25519"), testPubKey)
	p, r, v := newRemoteForEditorTest(t, lima.SSHConfig{Host: "mini.local"})
	const existing = "ssh-ed25519 AAAAexisting lima"
	ak := filepath.Join(r.guestHome, ".ssh", "authorized_keys")
	if err := os.MkdirAll(filepath.Dir(ak), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ak, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.EditorSSH(context.Background(), v); err != nil {
		t.Fatalf("EditorSSH: %v", err)
	}
	data, err := os.ReadFile(ak)
	if err != nil {
		t.Fatal(err)
	}
	if want := existing + "\n" + testPubKey + "\n"; string(data) != want {
		t.Fatalf("authorized_keys = %q, want %q", data, want)
	}
}

// TestRemoteLimaEditorSSHUnknownGuestUser proves an unreadable guest user is
// an error: an entry with no User would log in as the workstation's user,
// which the guest does not have.
func TestRemoteLimaEditorSSHUnknownGuestUser(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeKeyPair(t, filepath.Join(home, ".ssh", "id_ed25519"), testPubKey)
	p, r, v := newRemoteForEditorTest(t, lima.SSHConfig{Host: "mini.local"})
	v.Dir = t.TempDir() // no ssh.config
	if _, err := p.EditorSSH(context.Background(), v); err == nil {
		t.Fatal("EditorSSH succeeded without a guest user")
	}
	if len(r.calls) != 0 {
		t.Fatalf("guest was contacted before the entry could be built: %v", r.calls)
	}
}

// TestNewRemoteLimaKeepsProfileConnection pins that the constructor stores the
// fields EditorSSH builds its outer hop from.
func TestNewRemoteLimaKeepsProfileConnection(t *testing.T) {
	p, err := NewRemoteLima(TargetConfig{Provider: RemoteLimaProviderID, Host: "example.com", User: "dev", Port: 2200, IdentityPath: "/k/id", ProfileName: "mini"})
	if err != nil {
		t.Fatalf("NewRemoteLima: %v", err)
	}
	rp := p.(*remoteLimaProvider)
	if rp.profileName != "mini" {
		t.Fatalf("profileName = %q", rp.profileName)
	}
	if rp.conn.Host != "example.com" || rp.conn.User != "dev" || rp.conn.Port != 2200 || rp.conn.IdentityPath != "/k/id" {
		t.Fatalf("conn = %+v", rp.conn)
	}
}

// remapFiles serves reads of one "remote" instance directory from a local one,
// so a test can use a realistic remote path in the ProxyCommand while the
// guest user is still read from a file it controls.
type remapFiles struct {
	lima.HostFiles
	from, to string
}

func (f remapFiles) ReadFile(path string) ([]byte, error) {
	if rest, ok := strings.CutPrefix(path, f.from); ok {
		path = f.to + rest
	}
	return os.ReadFile(path)
}

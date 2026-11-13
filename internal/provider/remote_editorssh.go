package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/sshconfig"
	"github.com/lullabot/sandbar/internal/sshx"
	"github.com/lullabot/sandbar/internal/vm"
)

// authorizeKeyScript appends the public key it reads on stdin to the guest
// login user's authorized_keys unless an identical line is already there, so
// opening the same VM repeatedly never grows the file. It is a LITERAL: the
// key arrives on stdin and never becomes script text or an argv element, so
// nothing in a .pub file can be interpreted by the guest shell, and key
// material stays out of process listings and ssh debug logs. umask 077 gives
// a freshly created ~/.ssh 0700 and the file 0600; the chmod repairs a file
// that existed with looser permissions, which sshd's StrictModes would refuse.
// A file whose last line has no newline gets one first: appending straight
// onto it would fuse that key and this one into a single line sshd rejects,
// revoking the existing key as well as failing to add the new one.
//
// Its last line of output is the guest's $HOME, printed only once the key is
// in place: the folder VS Code opens is under it, and reporting it here saves
// the caller another round trip to the remote host to read it.
const authorizeKeyScript = `set -e; umask 077; mkdir -p ~/.ssh; key=$(cat); f=~/.ssh/authorized_keys; touch "$f"; grep -qxF -- "$key" "$f" || { [ ! -s "$f" ] || [ -z "$(tail -c1 "$f")" ] || echo >> "$f"; printf '%s\n' "$key" >> "$f"; }; chmod 600 "$f"; printf '%s\n' "$HOME"`

// defaultWorkstationKeys are tried, in order, when the profile names no
// identity: OpenSSH's own default key names, newest algorithm first.
var defaultWorkstationKeys = []string{"id_ed25519", "id_ecdsa", "id_rsa"}

// EditorSSH authorizes a workstation key in the guest and describes the guest
// as a host reached THROUGH the remote Lima host.
//
// The workstation cannot reach the guest directly: Lima forwards the guest's
// sshd only to the remote host's loopback, on a port it picks afresh at every
// start and records in <v.Dir>/ssh.config there. So the ProxyCommand logs in
// to the remote host and runs `ssh -F <v.Dir>/ssh.config -W 127.0.0.1:22
// lima-<name>`, letting that file supply the current port and the remote
// user's Lima key. The -W stream ends at the guest's own sshd, where the
// editor then authenticates as the guest user with a WORKSTATION key — which
// is why that key is first appended to the guest's authorized_keys. Lima's
// private key never leaves the remote host.
//
// The outer hop carries only the profile's own host, user, port and identity,
// read from the same sshx.Conn sandbar's own connections to that host are
// built from, and leaves everything else to the workstation's ssh_config as
// they do (see sshx.Config on why the remote-Lima hop does not force
// IdentitiesOnly).
func (p *remoteLimaProvider) EditorSSH(ctx context.Context, v vm.VM) (sshconfig.Entry, error) {
	if v.Dir == "" {
		return sshconfig.Entry{}, fmt.Errorf("remote lima: %q has no instance directory", v.Name)
	}
	hop := p.host.Conn().Config()
	for _, f := range []struct{ name, value string }{
		{"host", hop.Host}, {"user", hop.User},
		{"identity path", hop.IdentityPath}, {"instance directory", v.Dir},
	} {
		if strings.ContainsAny(f.value, `'"\`) {
			return sshconfig.Entry{}, fmt.Errorf("remote lima: the %s %q contains a quote or backslash, which cannot be written into an ssh ProxyCommand", f.name, f.value)
		}
	}
	priv, pub, err := p.workstationKey(hop.IdentityPath)
	if err != nil {
		return sshconfig.Entry{}, err
	}
	line, err := readPublicKeyLine(pub)
	if err != nil {
		return sshconfig.Entry{}, err
	}
	// Resolved before touching the guest: without it the entry would log in as
	// the workstation's user, an account the guest does not have.
	user := p.GuestUser(v)
	if user == "" {
		return sshconfig.Entry{}, fmt.Errorf("remote lima: cannot read %q's login user from %s/ssh.config on %s", v.Name, v.Dir, hop.Host)
	}

	var out bytes.Buffer
	if err := p.Shell(ctx, v.Name, strings.NewReader(line+"\n"), &out, "bash", "-c", authorizeKeyScript); err != nil {
		return sshconfig.Entry{}, fmt.Errorf("remote lima: authorizing %s in %q: %w: %s", pub, v.Name, err, strings.TrimSpace(out.String()))
	}

	return sshconfig.Entry{
		HostName:          "127.0.0.1",
		User:              user,
		IdentityFile:      priv,
		ProxyCommand:      p.proxyCommand(v),
		IdentitiesOnly:    true,
		EphemeralHostKeys: true,
		AuthorizedKey:     pub,
		GuestHome:         lastPathLine(out.String()),
	}, nil
}

// lastPathLine is the guest home authorizeKeyScript printed last, or "" when
// the output does not end in an absolute path — the caller then reads the home
// from the instance files instead, so a guest that printed something
// unexpected costs a round trip, not a wrong folder.
func lastPathLine(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\r\n"), "\n")
	home := strings.TrimRight(lines[len(lines)-1], "\r")
	if !strings.HasPrefix(home, "/") || strings.ContainsFunc(home, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return ""
	}
	return home
}

// proxyCommand builds the outer hop. Two shells parse it: the workstation's
// /bin/sh (ssh runs a ProxyCommand through it) and the remote login shell
// (ssh joins its command arguments with spaces and the far side re-parses
// them). So the remote command is quoted token by token for the remote shell,
// then quoted again as ONE argument for the local one, which hands it to ssh
// unchanged. Finally every '%' is doubled, because ssh expands %-tokens in a
// ProxyCommand before either shell sees it.
//
// A third parser constrains the inputs: ssh validates every config line's
// quoting with its own rules, where a backslash inside single quotes is an
// escape. One level of sshx.Quote's embedded-quote splice keeps its backslash
// outside any quotes and passes, but a
// quote in a value would nest a second level, put a backslash inside single
// quotes, and fail the whole file with "invalid quotes". EditorSSH therefore
// refuses quotes and backslashes in every value used here.
func (p *remoteLimaProvider) proxyCommand(v vm.VM) string {
	remote := []string{"ssh", "-F", filepath.Join(v.Dir, "ssh.config"), "-W", "127.0.0.1:22", sshconfig.LimaAlias(v.Name)}
	for i, tok := range remote {
		remote[i] = sshx.Quote(tok)
	}

	hop := p.host.Conn()
	cfg := hop.Config()
	argv := []string{"ssh"}
	if cfg.Port > 0 && cfg.Port != 22 {
		argv = append(argv, "-p", strconv.Itoa(cfg.Port))
	}
	if cfg.IdentityPath != "" {
		argv = append(argv, "-i", sshx.Quote(cfg.IdentityPath))
	}
	argv = append(argv, sshx.Quote(hop.Target()), "--", sshx.Quote(strings.Join(remote, " ")))
	return strings.ReplaceAll(strings.Join(argv, " "), "%", "%%")
}

// workstationKey picks the key pair the editor will log in with. The
// profile's identity (identityPath) is preferred, since the user chose it for this host;
// otherwise the first OpenSSH default with BOTH halves present, because the
// public half is what gets authorized and the private half what logs in.
func (p *remoteLimaProvider) workstationKey(identityPath string) (priv, pub string, err error) {
	if id := identityPath; id != "" {
		// sandbar's own connections hand this path to `ssh -i`, which expands a
		// leading ~ itself, so a profile saved as "~/.ssh/key" works everywhere
		// else. Here the .pub is read directly, with no ssh in between.
		id, err := profiles.ExpandHome(id)
		if err != nil {
			return "", "", fmt.Errorf("remote lima: identity path: %w", err)
		}
		if _, err := os.Stat(id + ".pub"); err != nil {
			return "", "", fmt.Errorf("remote lima: the profile's identity has no public key at %s: %w", id+".pub", err)
		}
		return id, id + ".pub", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("remote lima: locating ~/.ssh: %w", err)
	}
	for _, name := range defaultWorkstationKeys {
		k := filepath.Join(home, ".ssh", name)
		if fileExists(k) && fileExists(k+".pub") {
			return k, k + ".pub", nil
		}
	}
	return "", "", errors.New("remote lima: no ssh key pair found in ~/.ssh (id_ed25519, id_ecdsa or id_rsa with its .pub); set the profile's identity path to the key to use")
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

// readPublicKeyLine returns the single public-key line in path. Anything else
// is refused rather than appended: the guest script adds its input to
// authorized_keys verbatim, where an extra line would be a second, unreviewed
// grant (possibly with options such as command=), and a private key pasted
// into a .pub by mistake must not be copied into a VM.
func readPublicKeyLine(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("remote lima: public key %s does not exist", path)
		}
		return "", fmt.Errorf("remote lima: reading public key: %w", err)
	}
	line := strings.TrimRight(string(data), "\r\n")
	bad := line == "" || strings.ContainsAny(line, "\r\n") ||
		strings.ContainsFunc(line, func(r rune) bool { return r < 0x20 && r != '\t' || r == 0x7f })
	fields := strings.Fields(line)
	if !bad && len(fields) >= 2 {
		for _, prefix := range []string{"ssh-", "ecdsa-", "sk-"} {
			if strings.HasPrefix(fields[0], prefix) {
				return line, nil
			}
		}
	}
	return "", fmt.Errorf("remote lima: %s is not a single public key line", path)
}

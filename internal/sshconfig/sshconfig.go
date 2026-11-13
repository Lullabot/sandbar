// Package sshconfig owns sandbar's managed OpenSSH client config file and the
// one Include line that makes ssh read it.
//
// The point is that a VM becomes an ordinary named ssh host: VS Code's
// Remote-SSH, plain ssh, scp and anything else built on ssh all resolve the
// same alias, and nothing has to be pasted by hand. sandbar never writes host
// entries into ~/.ssh/config itself. It keeps them in one file of its own
// (ManagedPath), one delimited block per VM, and — only with the user's
// consent, which callers obtain — prepends a single Include of that file to
// ~/.ssh/config.
//
// Two OpenSSH scoping rules shape the file format, and both fail silently
// when violated, so they are worth stating:
//
//   - An Include written after a Host or Match line applies only to that
//     block. The Include in ~/.ssh/config is therefore PREPENDED, never
//     appended: most configs end inside some Host block.
//   - The same rule holds inside the managed file. Every block begins with
//     "Match all" to reset the scope, so a local-Lima Include entry that
//     follows a Host entry is not made conditional on that Host.
//
// The package has no dependency beyond the standard library and
// internal/statelock.
package sshconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/lullabot/sandbar/internal/statelock"
)

// Entry is a backend-neutral description of how a workstation ssh client
// reaches one VM. Either Include is set (local Lima, whose own per-instance
// ssh.config already holds everything, including the port Lima picks afresh
// on every start) or the Host options are.
type Entry struct {
	Alias string

	// Include, when set, is the only thing rendered: the file it names
	// defines the Host itself, and duplicating its options here would
	// override the values Lima keeps current.
	Include string

	HostName     string
	User         string
	IdentityFile string
	// ProxyCommand is written verbatim (after the control-character check):
	// it is a command line the provider builds with its own quoting, and
	// ssh's %h/%p tokens in it are intended.
	ProxyCommand string
	// Port 0 or 22 is omitted, 22 being ssh's own default.
	Port           int
	IdentitiesOnly bool
	// EphemeralHostKeys mirrors the throwaway-guest posture sandbar's own ssh
	// argv uses: a guest is rebuilt with a new host key on every reset, so
	// recording it would only produce a "host key changed" refusal later, and
	// remembering keys for disposable guests just accumulates junk.
	EphemeralHostKeys bool

	// AuthorizedKey is informational for callers (which public key a
	// provider authorized in the guest, so the user can be told). It is
	// never rendered.
	AuthorizedKey string
}

// aliasRe is deliberately narrower than what ssh accepts as a Host pattern:
// no wildcards, negation or whitespace, so an alias can only ever name itself.
var aliasRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Render returns the entry's delimited block, ending in a newline. The output
// is deterministic so an unchanged entry rewrites the file byte for byte.
func (e Entry) Render() (string, error) {
	if err := e.validate(); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s\nMatch all\n", beginMarker, e.Alias)
	if e.Include != "" {
		fmt.Fprintf(&b, "Include %s\n", quotePath(e.Include))
	} else {
		fmt.Fprintf(&b, "Host %s\n", e.Alias)
		if e.HostName != "" {
			fmt.Fprintf(&b, "  HostName %s\n", e.HostName)
		}
		if e.Port != 0 && e.Port != 22 {
			fmt.Fprintf(&b, "  Port %d\n", e.Port)
		}
		if e.User != "" {
			fmt.Fprintf(&b, "  User %s\n", e.User)
		}
		if e.IdentityFile != "" {
			fmt.Fprintf(&b, "  IdentityFile %s\n", quotePath(e.IdentityFile))
		}
		if e.IdentitiesOnly {
			b.WriteString("  IdentitiesOnly yes\n")
		}
		if e.ProxyCommand != "" {
			fmt.Fprintf(&b, "  ProxyCommand %s\n", e.ProxyCommand)
		}
		if e.EphemeralHostKeys {
			b.WriteString("  StrictHostKeyChecking no\n  UserKnownHostsFile /dev/null\n  LogLevel ERROR\n")
		}
	}
	fmt.Fprintf(&b, "%s%s\n", endMarker, e.Alias)
	return b.String(), nil
}

// validate is what stops a VM name, a profile field or a guest-reported value
// from writing directives of its own into the user's ssh configuration. A
// newline is the obvious vector; whitespace or a quote in a single-token
// field would split it into extra arguments.
func (e Entry) validate() error {
	if !aliasRe.MatchString(e.Alias) {
		return fmt.Errorf("ssh host alias %q: only letters, digits, '.', '_' and '-' are allowed", e.Alias)
	}
	if e.Port < 0 || e.Port > 65535 {
		return fmt.Errorf("ssh port %d is out of range", e.Port)
	}
	for _, f := range []struct{ name, v string }{{"HostName", e.HostName}, {"User", e.User}} {
		if err := checkPrintable(f.name, f.v); err != nil {
			return err
		}
		if strings.ContainsAny(f.v, " \"'\\#%$") {
			return fmt.Errorf("ssh %s %q contains a space, quote or special character", f.name, f.v)
		}
	}
	for _, f := range []struct{ name, v string }{{"Include", e.Include}, {"IdentityFile", e.IdentityFile}} {
		if err := checkPath(f.name, f.v); err != nil {
			return err
		}
	}
	return checkPrintable("ProxyCommand", e.ProxyCommand)
}

// checkPrintable rejects newlines and every other non-printing character
// (tabs and Unicode line separators included) rather than enumerating the
// dangerous ones: none has a legitimate place in these values.
func checkPrintable(name, v string) error {
	if !utf8.ValidString(v) {
		return fmt.Errorf("ssh %s is not valid UTF-8", name)
	}
	for _, r := range v {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("ssh %s %q contains a control character", name, v)
		}
	}
	return nil
}

// checkPath additionally refuses a double quote or backslash. Paths are
// written inside double quotes, and ssh's argument splitter treats both
// characters specially there; refusing them is simpler and safer than
// escaping for a parser whose rules have changed between releases.
func checkPath(name, v string) error {
	if err := checkPrintable(name, v); err != nil {
		return err
	}
	if strings.ContainsAny(v, "\"\\") {
		return fmt.Errorf("ssh %s %q contains a quote or backslash", name, v)
	}
	return nil
}

// quotePath double-quotes a path (so a space cannot split it) and doubles
// '%', because both Include and IdentityFile expand ssh's %-tokens and a
// literal percent in a directory name would otherwise be read as one.
func quotePath(p string) string {
	return `"` + strings.ReplaceAll(p, "%", "%%") + `"`
}

// LimaAlias is the alias local-Lima entries use. It is Lima's own Host name
// from its per-instance ssh.config, which the Include entry pulls in; any
// other alias would not match that file's Host line.
func LimaAlias(name string) string { return "lima-" + name }

// ProfileAlias is the alias for remote-Lima and Proxmox entries. The profile
// is part of it because two profiles may each hold a VM with the same name.
func ProfileAlias(profileName, vmName string) string {
	return "sandbar-" + slug(profileName) + "-" + vmName
}

// slug lowercases and replaces every run of characters other than [a-z0-9]
// with a single hyphen, so a profile name such as "Home Proxmox" becomes a
// valid alias component.
func slug(s string) string {
	var b strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			hyphen = false
			continue
		}
		if !hyphen {
			b.WriteByte('-')
			hyphen = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "profile"
	}
	return out
}

const (
	beginMarker = "# sandbar begin "
	endMarker   = "# sandbar end "

	managedHeader = "# Managed by sandbar. Each VM's block is rewritten whenever sandbar opens it\n" +
		"# in VS Code (sandbar code); edits inside a block will be lost.\n"
)

// homeDir falls back to "." rather than failing so a broken environment
// produces a visibly odd path in an error, matching internal/profiles.
func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "."
	}
	return home
}

// ManagedPath is ${XDG_CONFIG_HOME:-~/.config}/sandbar/ssh_config, made
// absolute: the Include in ~/.ssh/config must not depend on ssh's notion of a
// relative path (which is relative to ~/.ssh, not the working directory).
func ManagedPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(homeDir(), ".config")
	}
	p := filepath.Join(base, "sandbar", "ssh_config")
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// UserConfigPath is the user's own ssh client config, $HOME/.ssh/config.
func UserConfigPath() string {
	return filepath.Join(homeDir(), ".ssh", "config")
}

// IncludeLine is the exact line AddInclude writes, and the line a user is
// told to add by hand when sandbar must not edit the file.
func IncludeLine() string {
	return "Include " + quotePath(ManagedPath())
}

// Upsert writes the entry's block to the managed file, replacing an existing
// block for the same alias in place or appending a new one.
func Upsert(e Entry) error {
	block, err := e.Render()
	if err != nil {
		return err
	}
	return mutate(func(lines []string) []string {
		out, at := removeBlock(lines, e.Alias)
		blockLines := strings.SplitAfter(block, "\n")
		blockLines = blockLines[:len(blockLines)-1] // trailing "" after the final newline
		if at < 0 {
			if len(out) == 0 {
				out = strings.SplitAfter(managedHeader, "\n")
				out = out[:len(out)-1]
			}
			return append(out, blockLines...)
		}
		return append(out[:at], append(blockLines, out[at:]...)...)
	})
}

// Remove deletes the alias's block. A missing file or alias is not an error:
// the caller wants the alias gone, and it is. Removing matters most for
// Proxmox, whose entry names a DHCP address that may later be handed to an
// unrelated machine.
func Remove(alias string) error {
	if _, err := os.Stat(ManagedPath()); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return mutate(func(lines []string) []string {
		out, _ := removeBlock(lines, alias)
		return out
	})
}

// mutate is the single write path for the managed file: lock, re-read, apply,
// write atomically. Re-reading under the lock is what stops a long-running
// TUI from writing back an older copy over a block another sandbar process
// added; the lock alone would only order the two lost-update writes.
func mutate(apply func(lines []string) []string) error {
	path := ManagedPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	release := statelock.Acquire(path)
	defer release()

	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var lines []string
	if len(data) > 0 {
		s := string(data)
		if !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		lines = strings.SplitAfter(s, "\n")
		lines = lines[:len(lines)-1]
	}
	next := strings.Join(apply(lines), "")
	if next == string(data) {
		return nil
	}
	return writeAtomic(path, []byte(next), 0o600)
}

// removeBlock drops every block for alias and reports the line index where
// the first one began (-1 if none), so Upsert can put the replacement back
// where the old block was. A begin marker with no matching end — a damaged
// hand edit — is taken to run up to the next begin marker or the end of the
// file, so it can neither swallow the next VM's block nor linger as junk.
func removeBlock(lines []string, alias string) ([]string, int) {
	begin := beginMarker + alias
	end := endMarker + alias
	out := make([]string, 0, len(lines))
	at := -1
	for i := 0; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r\n") != begin {
			out = append(out, lines[i])
			continue
		}
		if at < 0 {
			at = len(out)
		}
		j := i + 1
		for ; j < len(lines); j++ {
			l := strings.TrimRight(lines[j], "\r\n")
			if l == end {
				break
			}
			if strings.HasPrefix(l, beginMarker) {
				j-- // keep the next block; resume from it
				break
			}
		}
		i = j
	}
	return out, at
}

// writeAtomic writes via a temp file in the same directory and a rename, so a
// crash or a full disk leaves the old file intact rather than a truncated one
// — for ~/.ssh/config that is the difference between a failed sandbar command
// and a broken ssh for everything.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// IncludeState describes ~/.ssh/config with respect to the managed file.
type IncludeState struct {
	// Present: some Include line already names the managed file.
	Present bool
	// Exists: the config file (or a symlink at its path) exists.
	Exists bool
	// Symlink: the path is a symlink, typically into a dotfiles repository.
	// sandbar never edits through one: rewriting it would replace the link
	// with a regular file and silently detach the user's dotfiles.
	Symlink bool
	// Writable: AddInclude could write the file (or create it).
	Writable bool
}

// IncludeStatus inspects ~/.ssh/config without changing it.
//
// Presence is judged loosely on purpose — any non-comment Include naming the
// managed file by absolute or ~/ path — because a false "missing" would offer
// to add a duplicate line the user already wrote by hand.
func IncludeStatus() (IncludeState, error) {
	path := UserConfigPath()
	var st IncludeState
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		st.Writable = canCreate(filepath.Dir(path))
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.Exists = true
	st.Symlink = fi.Mode()&os.ModeSymlink != 0

	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) { // a dangling symlink reads as not-exist
		return st, fmt.Errorf("read %s: %w", path, err)
	}
	st.Present = includesManaged(string(data))

	if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
		f.Close()
		// The atomic rewrite also needs to create a temp file beside it.
		st.Writable = dirWritable(filepath.Dir(path))
	}
	return st, nil
}

func includesManaged(config string) bool {
	abs := ManagedPath()
	targets := map[string]bool{abs: true}
	if rel, err := filepath.Rel(homeDir(), abs); err == nil && !strings.HasPrefix(rel, "..") {
		targets["~/"+filepath.ToSlash(rel)] = true
	}
	for _, line := range strings.Split(config, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(line) < len("include")+1 || !strings.EqualFold(line[:len("include")], "include") {
			continue
		}
		rest := line[len("include"):]
		if c := rest[0]; c != ' ' && c != '\t' && c != '=' {
			continue // e.g. a hypothetical "IncludeFoo" keyword
		}
		rest = strings.TrimLeft(rest, " \t")
		rest = strings.TrimPrefix(rest, "=")
		for _, arg := range splitArgs(rest) {
			if targets[strings.ReplaceAll(arg, "%%", "%")] {
				return true
			}
		}
	}
	return false
}

// splitArgs splits on whitespace, honouring double quotes, which is enough to
// read the Include lines a person or sandbar writes; it is not a full
// reimplementation of ssh's tokenizer.
func splitArgs(s string) []string {
	var args []string
	var cur strings.Builder
	inQuote, have := false, false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			have = true
		case (r == ' ' || r == '\t') && !inQuote:
			if have {
				args = append(args, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteRune(r)
			have = true
		}
	}
	if have {
		args = append(args, cur.String())
	}
	return args
}

func dirWritable(dir string) bool {
	return syscall.Access(dir, 0x2 /* W_OK */) == nil
}

// canCreate reports whether the config could be created: ~/.ssh exists and is
// writable, or does not exist and its parent is.
func canCreate(sshDir string) bool {
	if _, err := os.Stat(sshDir); err == nil {
		return dirWritable(sshDir)
	}
	return dirWritable(filepath.Dir(sshDir))
}

// ManualIncludeError means sandbar will not (or cannot) edit ~/.ssh/config
// and the user must add Line themselves. Callers show Line verbatim.
type ManualIncludeError struct {
	Path   string
	Line   string
	Reason string
}

func (e *ManualIncludeError) Error() string {
	return fmt.Sprintf("not editing %s (%s); add this line at the top of it yourself: %s", e.Path, e.Reason, e.Line)
}

const includeComment = "# Added by sandbar: VM host entries for VS Code Remote-SSH (sandbar code).\n"

// AddInclude prepends the Include of the managed file to ~/.ssh/config,
// creating ~/.ssh (0700) and the file (0600) if needed and preserving an
// existing file's mode. It is a no-op when the line is already present.
// Callers must have the user's consent first; this function does not ask.
//
// It takes no statelock: that would leave a "config.lock" file in ~/.ssh, a
// directory sandbar otherwise never touches, to guard a one-time edit the
// user just confirmed interactively. The file is re-read immediately before
// the atomic write instead.
func AddInclude() error {
	path := UserConfigPath()
	line := IncludeLine()
	manual := func(reason string) error {
		return &ManualIncludeError{Path: path, Line: line, Reason: reason}
	}
	if err := checkPath("Include", ManagedPath()); err != nil {
		return manual(err.Error())
	}
	st, err := IncludeStatus()
	if err != nil {
		return manual(err.Error())
	}
	switch {
	case st.Present:
		return nil
	case st.Symlink:
		return manual("it is a symlink")
	case !st.Writable:
		return manual("it is not writable")
	}

	mode := os.FileMode(0o600)
	var existing []byte
	if st.Exists {
		fi, err := os.Stat(path)
		if err != nil {
			return manual(err.Error())
		}
		mode = fi.Mode().Perm()
		if existing, err = os.ReadFile(path); err != nil {
			return manual(err.Error())
		}
	} else if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return manual(err.Error())
	}

	content := includeComment + line + "\n\n" + string(existing)
	if err := writeAtomic(path, []byte(content), mode); err != nil {
		return manual(err.Error())
	}
	return nil
}

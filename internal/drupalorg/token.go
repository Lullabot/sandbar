package drupalorg

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoToken reports that no drupal.org account PAT file exists at
// [TokenPath]. It means publication is unavailable, not that something went
// wrong: the CLI and the TUI both check for it with errors.Is before
// collecting a change set or resolving a fork, so they can say so up front
// rather than failing mid-publish.
var ErrNoToken = errors.New("no drupal.org token found")

// errTokenFile is the wrap prefix used for every I/O error LoadToken can
// return for the token file itself (as opposed to ErrNoToken, the "no file
// at all" sentinel, or the over-permissive-mode refusal, which both carry
// their own more specific messages).
const errTokenFile = "drupal.org token file: %w"

// TokenPath returns the conventional, fixed location of the workstation's
// drupal.org account personal access token:
// ${XDG_CONFIG_HOME:-~/.config}/sandbar/drupalorg.token.
//
// The path is a convention rather than a configured value, and deliberately
// so. internal/profiles/token.go's TokenFile is a field on a Proxmox
// connection profile, and profiles.yaml is a per-location store; this
// credential is workstation-global and has nothing to do with where VMs run,
// so it has no coherent home there. A new global config file was rejected
// too: it would introduce a persisted schema, its versioning, and a TUI
// editing surface for a single string. A convention has nothing to
// mis-record and nothing to migrate.
//
// This credential also must NOT live in internal/secrets. That package
// exists to deliver arbitrary KEY=VALUE secrets INTO guest VMs (see its
// package doc) — the one thing this design forbids for an account-level
// drupal.org token, which must never enter a guest. Storing it there would
// make the one thing the whole publication design forbids into a one-line
// change that looks correct. This package (drupalorg) is read only on the
// host, at the moment of an authenticated call, and is never handed to
// anything that provisions or configures a guest.
//
// TokenPath deliberately does not reuse internal/profiles.ExpandHome: that
// helper expands a leading "~/" out of an already-"~"-prefixed string, which
// is not the shape of the problem here (there is no configured path to
// expand, only an XDG fallback to compute), and importing internal/profiles
// — the connection-profile package — into internal/drupalorg would couple a
// package whose whole point is to be unrelated to profiles.yaml and
// connection profiles to that package for the sake of an 8-line helper.
// os.UserHomeDir is used directly instead.
func TokenPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "sandbar", "drupalorg.token"), nil
}

// LoadToken reads the workstation's drupal.org account PAT from [TokenPath].
// It is the single read site for this credential.
//
// A file readable by group or other is refused outright rather than warned
// about, mirroring internal/profiles.LoadToken's Proxmox-token refusal
// verbatim in intent: a leaked API token is not a recoverable mistake. An
// absent file returns [ErrNoToken] (checkable with errors.Is) rather than a
// generic I/O error, so a caller can distinguish "publication unavailable"
// from "something is broken". The token text itself never appears in any
// error this function returns.
func LoadToken() (string, error) {
	path, err := TokenPath()
	if err != nil {
		return "", err
	}

	// Opened once and checked via fstat on the open descriptor, rather than
	// os.Stat followed by a separate os.ReadFile, so the permission check and
	// the read observe the same inode: nothing on the filesystem can swap the
	// file between the two.
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%s: %w", path, ErrNoToken)
		}
		return "", fmt.Errorf(errTokenFile, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf(errTokenFile, err)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		return "", fmt.Errorf("drupal.org token file %s has mode %04o; it must not be readable by group or other (chmod 600)", path, mode)
	}

	b, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf(errTokenFile, err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("drupal.org token file %s is empty", path)
	}
	return tok, nil
}

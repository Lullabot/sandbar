package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lullabot/sandbar/internal/profiles"
)

// cloneTokenStdin is where `--clone-token-file -` reads. A variable so tests
// can supply a token without replacing the process's real stdin.
var cloneTokenStdin io.Reader = os.Stdin

// maxCloneTokenBytes bounds what a token file or stdin may hold. Real forge
// tokens are a few hundred bytes at most; the cap stops a mistaken path to an
// endless source from being read without end.
const maxCloneTokenBytes = 64 << 10

// resolveCloneToken merges --clone-token and --clone-token-file into the one
// token the rest of create/reset already handles. --clone-token puts the secret
// in this process's argv, which any local user can read from /proc or ps for
// the whole build; a file or stdin does not. The flags are exclusive, because
// silently preferring one would hide which credential a build used.
func resolveCloneToken(token, file string, tokenSet, fileSet bool) (string, error) {
	if tokenSet && fileSet {
		return "", errors.New("--clone-token and --clone-token-file cannot be combined")
	}
	if !fileSet {
		return token, nil
	}
	if file == "" {
		return "", errors.New("--clone-token-file needs a path (use - for stdin)")
	}
	return readCloneTokenFile(file, cloneTokenStdin)
}

// readCloneTokenFile reads a clone token from path, or from stdin when path is
// "-". A file readable by group or other is refused outright, as
// profiles.LoadToken does for Proxmox tokens, and so is anything that is not a
// regular file: a FIFO or device could block or stream unbounded data. The mode
// is checked on the open descriptor, so the file cannot be swapped between the
// check and the read. Stdin is not checked; the caller chose the pipe. Errors
// never include the contents.
func readCloneTokenFile(path string, stdin io.Reader) (string, error) {
	r := stdin
	if path != "-" {
		expanded, err := profiles.ExpandHome(path)
		if err != nil {
			return "", err
		}
		f, err := os.Open(expanded)
		if err != nil {
			return "", fmt.Errorf("clone token file: %w", err)
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return "", fmt.Errorf("clone token file: %w", err)
		}
		if !fi.Mode().IsRegular() {
			return "", fmt.Errorf("clone token file %s is not a regular file", expanded)
		}
		if mode := fi.Mode().Perm(); mode&0o077 != 0 {
			return "", fmt.Errorf("clone token file %s has mode %04o; it must not be readable by group or other (chmod 600)", expanded, mode)
		}
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, maxCloneTokenBytes+1))
	if err != nil {
		return "", fmt.Errorf("read clone token: %w", err)
	}
	if len(b) > maxCloneTokenBytes {
		return "", errors.New("clone token is implausibly large")
	}
	// Only a single trailing newline: `echo "$TOKEN" | ...` appends one, but
	// other whitespace could be part of the credential and is not ours to cut.
	tok := strings.TrimSuffix(string(b), "\n")
	tok = strings.TrimSuffix(tok, "\r")
	if tok == "" {
		return "", errors.New("clone token is empty")
	}
	return tok, nil
}

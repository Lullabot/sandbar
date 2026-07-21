package profiles

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadToken reads a Proxmox API token from path. The token is deliberately
// NOT a Profile field: profiles.yaml is secret-free by design (see Profile),
// so the profile records only where the credential lives and this function
// is the one place it is read. A file readable by group or other is refused
// outright rather than warned about — a leaked API token is not a
// recoverable mistake.
func LoadToken(path string) (string, error) {
	expanded, err := expandHome(path)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(expanded)
	if err != nil {
		return "", fmt.Errorf("proxmox token file: %w", err)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		return "", fmt.Errorf("proxmox token file %s has mode %04o; it must not be readable by group or other (chmod 600)", expanded, mode)
	}
	b, err := os.ReadFile(expanded)
	if err != nil {
		return "", fmt.Errorf("proxmox token file: %w", err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("proxmox token file %s is empty", expanded)
	}
	return tok, nil
}

// expandHome expands a leading "~/" (or a bare "~") in path against the
// current user's home directory, mirroring how IdentityPath is resolved
// elsewhere in the codebase (e.g. internal/lima/hostfiles.go). Any other
// path is returned unchanged.
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

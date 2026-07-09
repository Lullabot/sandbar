// Package secrets is the host-side, per-VM secrets store: the single source
// of truth for every secret sand injects into a Lima VM (global environment
// variables, GitHub tokens, and directory-scoped environment variables). It
// is persisted as a sibling of the managed-VM registry
// (internal/registry), under its own secrets/ subdirectory — one JSON file
// per VM — so the registry itself stays secret-free.
//
// Values are stored in plaintext on disk, protected only by filesystem
// permissions (0700 directory, 0600 file). This package never logs or
// prints a cleartext secret value; callers that need to display secrets must
// go through Redacted(), which only ever returns masked values.
package secrets

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Category identifies which of the three secret kinds a mutation targets.
type Category string

const (
	// CategoryGlobal is a VM-wide environment variable, keyed by name.
	CategoryGlobal Category = "global"
	// CategoryGitHub is a GitHub token bound to a home-relative directory
	// scope, keyed by scope. An empty scope (or ".") means the VM-wide
	// default token; a non-empty scope (e.g. "github.com/acme") is an
	// org/subtree override.
	CategoryGitHub Category = "github"
	// CategoryDirEnv is a generic environment variable scoped to a
	// home-relative directory, keyed by (scope, name).
	CategoryDirEnv Category = "dir_env"
)

// GlobalSecret is a VM-wide environment variable.
type GlobalSecret struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// GitHubSecret is a GitHub token bound to a home-relative directory scope.
type GitHubSecret struct {
	Scope string `json:"scope"`
	Token string `json:"token"`
}

// DirEnvSecret is a generic environment variable scoped to a home-relative
// directory.
type DirEnvSecret struct {
	Scope string `json:"scope"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Store is the on-disk JSON shape for one VM's secrets file. Field order and
// tags are a cross-task contract: keep them exactly as documented in
// .ai/strikethroo/plans/11--host-secrets-manager/tasks/01--host-secrets-store-package.md.
type Store struct {
	Version int            `json:"version"`
	Global  []GlobalSecret `json:"global"`
	GitHub  []GitHubSecret `json:"github"`
	DirEnv  []DirEnvSecret `json:"dir_env"`
}

// currentVersion is written to new/loaded stores that don't already carry a
// version (e.g. a freshly created Store or a pre-existing file from a
// future minor revision that omitted the field).
const currentVersion = 1

// dataDir mirrors registry.defaultPath's XDG resolution:
// ${XDG_DATA_HOME:-$HOME/.local/share}/sandbar.
func dataDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			home = "."
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "sandbar")
}

// Path returns the on-disk location of vm's secrets file:
// ${XDG_DATA_HOME:-$HOME/.local/share}/sandbar/secrets/<vm-name>.json.
func Path(vm string) string {
	return filepath.Join(dataDir(), "secrets", vm+".json")
}

// Load reads vm's secrets store. A missing file yields an empty, usable
// store — not an error — mirroring registry.LoadFrom's behavior so callers
// don't need special-case handling for a VM that has never had a secret
// set.
func Load(vm string) (*Store, error) {
	path := Path(vm)
	s := &Store{Version: currentVersion}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s, nil
		}
		return s, err
	}
	if len(data) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, s); err != nil {
		return &Store{Version: currentVersion}, err
	}
	if s.Version == 0 {
		s.Version = currentVersion
	}
	return s, nil
}

// Save atomically persists s as vm's secrets file: write to a unique temp
// file in the same directory, set its mode to exactly 0600, then rename
// over the destination. The parent directory is created with 0700 if
// missing. The temp-then-rename sequence ensures a crash mid-write can
// never leave a partially written or torn secrets file in place.
func (s *Store) Save(vm string) error {
	if s.Version == 0 {
		s.Version = currentVersion
	}
	path := Path(vm)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".secrets-*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if anything below fails before the rename.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// SetSecret adds or updates a secret in category, keyed by (scope, name):
//   - CategoryGlobal: keyed by name; scope is ignored, value is the
//     variable's value.
//   - CategoryGitHub: keyed by scope; name is ignored, value is the token.
//   - CategoryDirEnv: keyed by (scope, name); value is the variable's
//     value.
//
// An existing entry with the same key is updated in place; otherwise a new
// entry is appended.
func (s *Store) SetSecret(category Category, scope, name, value string) {
	switch category {
	case CategoryGlobal:
		for i := range s.Global {
			if s.Global[i].Name == name {
				s.Global[i].Value = value
				return
			}
		}
		s.Global = append(s.Global, GlobalSecret{Name: name, Value: value})
	case CategoryGitHub:
		for i := range s.GitHub {
			if s.GitHub[i].Scope == scope {
				s.GitHub[i].Token = value
				return
			}
		}
		s.GitHub = append(s.GitHub, GitHubSecret{Scope: scope, Token: value})
	case CategoryDirEnv:
		for i := range s.DirEnv {
			if s.DirEnv[i].Scope == scope && s.DirEnv[i].Name == name {
				s.DirEnv[i].Value = value
				return
			}
		}
		s.DirEnv = append(s.DirEnv, DirEnvSecret{Scope: scope, Name: name, Value: value})
	}
}

// RemoveSecret removes the secret in category keyed by (scope, name),
// following the same key semantics as SetSecret. It reports whether an
// entry was found and removed.
func (s *Store) RemoveSecret(category Category, scope, name string) bool {
	switch category {
	case CategoryGlobal:
		for i := range s.Global {
			if s.Global[i].Name == name {
				s.Global = append(s.Global[:i], s.Global[i+1:]...)
				return true
			}
		}
	case CategoryGitHub:
		for i := range s.GitHub {
			if s.GitHub[i].Scope == scope {
				s.GitHub = append(s.GitHub[:i], s.GitHub[i+1:]...)
				return true
			}
		}
	case CategoryDirEnv:
		for i := range s.DirEnv {
			if s.DirEnv[i].Scope == scope && s.DirEnv[i].Name == name {
				s.DirEnv = append(s.DirEnv[:i], s.DirEnv[i+1:]...)
				return true
			}
		}
	}
	return false
}

// Value returns the cleartext value of a stored secret keyed by
// (category, scope, name), and whether it exists. It is the inverse of the
// (category, scope, name) key SetSecret/RemoveSecret use; callers that only
// need to display the store must use Redacted() instead — Value hands back
// cleartext and is intended for an editor that pre-fills the current value.
func (s *Store) Value(category Category, scope, name string) (string, bool) {
	switch category {
	case CategoryGlobal:
		for i := range s.Global {
			if s.Global[i].Name == name {
				return s.Global[i].Value, true
			}
		}
	case CategoryGitHub:
		for i := range s.GitHub {
			if s.GitHub[i].Scope == scope {
				return s.GitHub[i].Token, true
			}
		}
	case CategoryDirEnv:
		for i := range s.DirEnv {
			if s.DirEnv[i].Scope == scope && s.DirEnv[i].Name == name {
				return s.DirEnv[i].Value, true
			}
		}
	}
	return "", false
}

// maskedValue is returned for every secret in Redacted(); it deliberately
// carries no information about the underlying value (not even its length)
// so it is safe to log or print.
const maskedValue = "****"

// RedactedEntry is a display-safe view of one stored secret. It never
// carries the cleartext value.
type RedactedEntry struct {
	Category Category
	Scope    string
	Name     string
	Masked   string
}

// Redacted returns a display list covering every secret in s with values
// replaced by a fixed mask. This is the only supported way for a caller to
// list/display store contents; callers must never print or log the raw
// Store fields.
func (s *Store) Redacted() []RedactedEntry {
	out := make([]RedactedEntry, 0, len(s.Global)+len(s.GitHub)+len(s.DirEnv))
	for _, g := range s.Global {
		out = append(out, RedactedEntry{Category: CategoryGlobal, Name: g.Name, Masked: maskedValue})
	}
	for _, g := range s.GitHub {
		out = append(out, RedactedEntry{Category: CategoryGitHub, Scope: g.Scope, Masked: maskedValue})
	}
	for _, d := range s.DirEnv {
		out = append(out, RedactedEntry{Category: CategoryDirEnv, Scope: d.Scope, Name: d.Name, Masked: maskedValue})
	}
	return out
}

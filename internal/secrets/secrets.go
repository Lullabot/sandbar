// Package secrets is a per-VM host store of arbitrary KEY=VALUE pairs that sand
// applies into a guest's shell environment. The pairs are persisted host-side in
// a 0600 JSON file and rendered into a file the guest SOURCEs, so the rendering
// is a security boundary: a value containing a single quote, a `$(…)`, or a
// backtick must reach the guest shell as literal text and never execute. Render
// wraps every value in POSIX single quotes (the one escaping that expands
// nothing) for exactly that reason.
//
// Pairs are additionally namespaced by a directory SCOPE: the empty scope ""
// is global, and any other scope is a safe home-relative directory path (see
// ValidScope) that a caller later turns into a filesystem location such as
// ~/<scope>/ on the guest. The scope is therefore a second injection surface
// beyond the KEY and is validated at this storage boundary.
//
// The on-disk shape mirrors the registry package:
// {"version":2,"vms":{"<name>":{"<scope>":{"KEY":"VALUE"}}}}. Load is tolerant
// of a missing or corrupt file, transparently migrates a v1 (or unversioned)
// flat file by promoting its pairs to the global scope, and refuses a file
// from a newer sand. It always returns a usable, non-nil store.
package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// schemaVersion is the on-disk format this build writes and understands. A file
// stamped with a higher version is refused rather than misparsed (a newer sand
// may have added fields this build would silently drop on the next save).
const schemaVersion = 2

// fileSchema is the on-disk JSON shape:
// {"version":2,"vms":{"<name>":{"<scope>":{"KEY":"VALUE"}}}}.
type fileSchema struct {
	Version int                                     `json:"version"`
	VMs     map[string]map[string]map[string]string `json:"vms"`
}

// versionProbe decodes only the "version" field so LoadFrom can pick the
// correct concrete type (v1File vs fileSchema) BEFORE attempting a full
// decode. v1 and v2 both nest a map under "vms", but at different depths (a
// string value vs a further map), so decoding v1 bytes directly into
// fileSchema would fail; the version must be known first.
type versionProbe struct {
	Version int `json:"version"`
}

// v1File is PR 27's original flat on-disk shape:
// {"version":1,"vms":{"<name>":{"KEY":"VALUE"}}} (or no "version" field at
// all, which is also treated as v1).
type v1File struct {
	Version int                          `json:"version"`
	VMs     map[string]map[string]string `json:"vms"`
}

// keyRE is the exact grammar for a shell-safe environment variable name. Keys are
// emitted UNQUOTED by Render, so this is a security gate, not mere validation.
var keyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidKey reports whether k is a legal environment variable name:
// [A-Za-z_][A-Za-z0-9_]*. Everything else — the empty string, a leading digit, a
// name with a dash, space, `=`, `$`, or any other metacharacter — is rejected,
// because such a name cannot be emitted as an unquoted `export` token without
// becoming a shell injection.
func ValidKey(k string) bool {
	return keyRE.MatchString(k)
}

// scopeSegmentRE is the grammar for one path segment of a scope: one or more
// of [A-Za-z0-9._-]. A segment that is exactly "." or ".." is rejected
// separately below (the character class alone would allow them).
var scopeSegmentRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidScope reports whether scope is a safe home-relative directory path.
// "" is the global scope. A non-empty scope must be one or more path segments
// of [A-Za-z0-9._-] (a single dot alone or ".." are rejected), slash-separated,
// with no leading/trailing slash and no empty segments. It becomes ~/<scope>/
// on the guest and a gitdir: pattern, so anything that could escape $HOME or
// inject into a shell/gitconfig is rejected.
func ValidScope(scope string) bool {
	if scope == "" {
		return true
	}
	if strings.HasPrefix(scope, "/") || strings.HasSuffix(scope, "/") {
		return false
	}
	for _, seg := range strings.Split(scope, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		if !scopeSegmentRE.MatchString(seg) {
			return false
		}
	}
	return true
}

// Store is an in-memory, per-VM secret store optionally backed by a JSON file.
// An empty path disables persistence (used in tests). It holds no mutex, so it
// is copy-safe to embed by value in the TUI model — callers hold a *Store and the
// TUI passes that pointer through its by-value Update. Data is always held in
// memory as v2 (name -> scope -> KEY -> VALUE), regardless of the on-disk
// version it was loaded from.
type Store struct {
	path string
	vms  map[string]map[string]map[string]string
}

// NewEmpty returns an in-memory store with no backing file. save() is a no-op for
// it, so it never touches disk.
func NewEmpty() *Store {
	return &Store{vms: map[string]map[string]map[string]string{}}
}

// defaultPath mirrors the registry's XDG derivation but for the secrets file:
// ${XDG_DATA_HOME:-$HOME/.local/share}/sandbar/secrets.json.
func defaultPath() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			home = "."
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "sandbar", "secrets.json")
}

// Load reads the store from the default path.
func Load() (*Store, error) {
	return LoadFrom(defaultPath())
}

// LoadFrom reads the store from an explicit path. A missing or empty file yields
// an empty store (not an error). A corrupt file is moved aside to
// "<path>.corrupt" — so a later save cannot silently clobber recoverable data —
// and the error is returned for the caller to surface. A file stamped with a
// version newer than this build understands is refused with an "upgrade sand"
// error. A v1 (or unversioned) file is transparently migrated: each VM's flat
// KEY=VALUE map is promoted to the global scope (""); the next save stamps the
// file as version 2. In every case the returned store is non-nil and usable.
func LoadFrom(path string) (*Store, error) {
	s := &Store{path: path, vms: map[string]map[string]map[string]string{}}
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

	var probe versionProbe
	if err := json.Unmarshal(data, &probe); err != nil {
		_ = os.Rename(path, path+".corrupt")
		return s, fmt.Errorf("secrets store at %s was unreadable (moved to %s.corrupt): %w", path, path, err)
	}

	if probe.Version > schemaVersion {
		return s, fmt.Errorf("secrets store at %s is version %d but this build understands only version %d — upgrade sand", path, probe.Version, schemaVersion)
	}

	if probe.Version <= 1 {
		// v1 (or unversioned) shape: vms[name] = map[string]string. Decode
		// into the concrete v1 type — NOT the v2 struct, since a string
		// value where v2 expects a nested map would fail to unmarshal.
		var v1 v1File
		if err := json.Unmarshal(data, &v1); err != nil {
			_ = os.Rename(path, path+".corrupt")
			return s, fmt.Errorf("secrets store at %s was unreadable (moved to %s.corrupt): %w", path, path, err)
		}
		for name, pairs := range v1.VMs {
			cp := make(map[string]string, len(pairs))
			for k, v := range pairs {
				cp[k] = v
			}
			s.vms[name] = map[string]map[string]string{"": cp}
		}
		return s, nil
	}

	// probe.Version == 2 (the only remaining case, since >2 and <=1 are
	// handled above): decode the full v2 shape.
	var parsed fileSchema
	if err := json.Unmarshal(data, &parsed); err != nil {
		_ = os.Rename(path, path+".corrupt")
		return s, fmt.Errorf("secrets store at %s was unreadable (moved to %s.corrupt): %w", path, path, err)
	}
	if parsed.VMs != nil {
		s.vms = parsed.VMs
	}
	return s, nil
}

// Get returns a defensive copy of the global-scope (scope "") KEY=VALUE pairs
// stored for vm, or an empty (non-nil) map if none. It is a convenience
// wrapper over GetAll for callers that only care about the global scope. The
// copy prevents a caller from mutating the store's backing map behind its
// back.
func (s *Store) Get(vm string) map[string]string {
	out := make(map[string]string, len(s.vms[vm][""]))
	for k, v := range s.vms[vm][""] {
		out[k] = v
	}
	return out
}

// Set replaces vm's global-scope (scope "") pairs with a copy of pairs and
// persists the change, leaving any other scope for vm untouched. It is a
// convenience wrapper over SetAll. Every key is validated first (ValidKey); a
// single invalid key rejects the whole call without mutating the store or
// touching disk, so an injectable key can never be persisted. An empty pairs
// map drops the global scope (and, if no other scope remains, vm's entry)
// rather than persisting an empty object.
func (s *Store) Set(vm string, pairs map[string]string) error {
	scopes := s.GetAll(vm)
	if len(pairs) == 0 {
		delete(scopes, "")
	} else {
		scopes[""] = pairs
	}
	return s.SetAll(vm, scopes)
}

// GetAll returns a defensive deep copy of vm's scope -> KEY -> VALUE map, or
// an empty (non-nil) map if vm has no entry. Mutating the returned map (at
// any depth) does not affect the store.
func (s *Store) GetAll(vm string) map[string]map[string]string {
	src := s.vms[vm]
	out := make(map[string]map[string]string, len(src))
	for scope, pairs := range src {
		cp := make(map[string]string, len(pairs))
		for k, v := range pairs {
			cp[k] = v
		}
		out[scope] = cp
	}
	return out
}

// SetAll replaces vm's scopes with a deep copy of scopes and persists the
// change. Every scope is validated (ValidScope) and every key within every
// scope is validated (ValidKey) BEFORE any mutation, so the whole call is
// rejected on the first invalid scope or key without touching the in-memory
// store or disk (all-or-nothing, mirroring PR 27's Set). An empty scopes map,
// or one whose scopes are all empty, drops vm's entry entirely rather than
// persisting an empty object tree.
func (s *Store) SetAll(vm string, scopes map[string]map[string]string) error {
	for scope, pairs := range scopes {
		if !ValidScope(scope) {
			return fmt.Errorf("invalid secret scope %q: must be a safe home-relative directory path", scope)
		}
		for k := range pairs {
			if !ValidKey(k) {
				return fmt.Errorf("invalid secret key %q: keys must match [A-Za-z_][A-Za-z0-9_]*", k)
			}
		}
	}

	cp := make(map[string]map[string]string, len(scopes))
	for scope, pairs := range scopes {
		if len(pairs) == 0 {
			continue
		}
		inner := make(map[string]string, len(pairs))
		for k, v := range pairs {
			inner[k] = v
		}
		cp[scope] = inner
	}

	if len(cp) == 0 {
		if _, ok := s.vms[vm]; !ok {
			return s.save()
		}
		delete(s.vms, vm)
		return s.save()
	}
	s.vms[vm] = cp
	return s.save()
}

// Remove drops vm's entry (all scopes) and persists the change.
func (s *Store) Remove(vm string) error {
	delete(s.vms, vm)
	return s.save()
}

// shellSingleQuote wraps s in single quotes for safe inclusion in a POSIX shell
// file. Inside single quotes no expansion occurs, so the only character needing
// special handling is the quote itself. Each single quote is replaced by the
// four-byte sequence quote, backslash, quote, quote -- which closes the quoted
// span, emits one escaped literal quote, then reopens the span. That keeps
// command substitutions, backticks, dollar-variables, and backslashes all
// literal. See the ReplaceAll call below for the exact sequence.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Render emits the guest env file: one `export KEY='VALUE'` line per pair, keys
// sorted ascending so the output is byte-stable for equal input, each value
// single-quote-escaped, with a trailing newline. Keys that are not ValidKey are
// skipped — they are emitted unquoted and so cannot be represented safely; Set
// already rejects them, and this is the second line of defense. Render takes a
// single map[string]string for ONE scope: rendering stays per-scope even though
// storage is now scope-aware.
func Render(pairs map[string]string) string {
	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		if !ValidKey(k) {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString("export " + k + "=" + shellSingleQuote(pairs[k]) + "\n")
	}
	return b.String()
}

// save writes the store atomically: a unique temp file in the same directory as
// the target, created at 0600 BEFORE any secret bytes are written, is renamed
// over the target. The parent directory is forced to 0700. There is therefore no
// instant at which a world-readable file holds a secret. An empty path is a
// no-op, so an in-memory store never touches disk. The unique temp name keeps two
// TUI processes sharing a data dir from racing on a fixed name.
func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Force 0700 even if the directory pre-existed (e.g. the registry created the
	// shared sandbar dir at 0755): the secrets dir must not be world-listable.
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(fileSchema{Version: schemaVersion, VMs: s.vms}, "", "  ")
	if err != nil {
		return err
	}

	// os.CreateTemp opens the file at mode 0600, so the file is never
	// world-readable, not even for the instant before we write secret bytes.
	tmp, err := os.CreateTemp(dir, ".secrets-*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

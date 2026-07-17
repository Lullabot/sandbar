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
// Orthogonal to that directory scope, every VM is ALSO keyed by a CONNECTION
// scope (registry.Scope{Provider, RemoteTarget}) identifying which host the VM
// lives on. The connection scope wraps the whole per-VM entry (directory
// scopes and all) so a `web` on the local Lima provider and a same-named `web`
// on a remote host never share, or clobber, each other's secrets. Do not
// confuse the two: ValidScope/the directory scope namespaces PATHS within one
// VM; the connection scope namespaces WHICH VM (by host).
//
// The on-disk shape (version 3) mirrors the registry package's own
// Provider/RemoteTarget fields for readability:
// {"version":3,"scopes":[{"provider":"lima","vms":{"<name>":{"<dirscope>":{"KEY":"VALUE"}}}}]}.
// Load is tolerant of a missing or corrupt file, transparently migrates an
// older file — v1 (or unversioned) flat pairs, or v2's bare name->dirscope
// keying — by lifting every VM under registry.LocalScope (no connection scope
// existed before this format, so anything they recorded could only ever have
// been local), and refuses a file from a newer sand. It always returns a
// usable, non-nil store.
//
// Every mutation (Set, SetAll, Remove) is a lock-protected read-modify-write:
// it takes a cross-process advisory lock on "<path>.lock" (internal/filelock),
// re-reads the CURRENT on-disk store fresh, applies only that one call's
// per-(connScope, vm) delta to it, and persists the merged result — never a
// blind overwrite of a possibly-stale in-memory snapshot. This is what lets
// two sand processes sharing a data dir mutate different VMs (or different
// connection scopes) concurrently without one silently discarding the
// other's committed secrets. See mutateLocked.
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

	"github.com/lullabot/sandbar/internal/filelock"
	"github.com/lullabot/sandbar/internal/registry"
)

// schemaVersion is the on-disk format this build writes and understands. A file
// stamped with a higher version is refused rather than misparsed (a newer sand
// may have added fields this build would silently drop on the next save).
//
// Version 3 added the connection-scope dimension (see the package doc):
// every VM's entry is now nested inside a scope group keyed by
// Provider/RemoteTarget instead of living directly under "vms".
const schemaVersion = 3

// fileSchema is the on-disk JSON shape for version 3:
// {"version":3,"scopes":[{"provider":"...","remote_target":"...","vms":{"<name>":{"<dirscope>":{"KEY":"VALUE"}}}}]}.
// A list (rather than a map keyed by some serialized scope string) keeps the
// connection scope's fields — Provider and RemoteTarget — individually
// readable, mirroring how the registry package records them on entry.
type fileSchema struct {
	Version int          `json:"version"`
	Scopes  []scopeGroup `json:"scopes"`
}

// scopeGroup is one connection scope's VMs in the v3 on-disk shape. Provider
// and RemoteTarget together are exactly registry.Scope; RemoteTarget is
// omitted when empty (the local provider), matching the registry package's own
// per-entry fields.
type scopeGroup struct {
	Provider     string                                  `json:"provider"`
	RemoteTarget string                                  `json:"remote_target,omitempty"`
	VMs          map[string]map[string]map[string]string `json:"vms"`
}

// versionProbe decodes only the "version" field so LoadFrom can pick the
// correct concrete type (v1File vs fileSchemaV2 vs fileSchema) BEFORE
// attempting a full decode. Each version nests its "vms" map at a different
// shape, so decoding older bytes directly into the current fileSchema would
// fail; the version must be known first.
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

// fileSchemaV2 is the pre-connection-scope on-disk shape:
// {"version":2,"vms":{"<name>":{"<dirscope>":{"KEY":"VALUE"}}}}. Every VM it
// records predates connection scopes and is lifted under registry.LocalScope
// on load — see LoadFrom.
type fileSchemaV2 struct {
	Version int                                     `json:"version"`
	VMs     map[string]map[string]map[string]string `json:"vms"`
}

// toScopeGroups converts the in-memory connection-scope-keyed map into the
// v3 on-disk slice shape, sorted by (Provider, RemoteTarget) so two saves of
// identical content produce byte-identical files. A connection scope with no
// VMs left (SetAll/Remove already prune these from the in-memory map, but this
// guards the invariant at the boundary too) is omitted rather than persisted
// as an empty group.
func toScopeGroups(vms map[registry.Scope]map[string]map[string]map[string]string) []scopeGroup {
	groups := make([]scopeGroup, 0, len(vms))
	for connScope, byName := range vms {
		if len(byName) == 0 {
			continue
		}
		groups = append(groups, scopeGroup{
			Provider:     connScope.Provider,
			RemoteTarget: connScope.RemoteTarget,
			VMs:          byName,
		})
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Provider != groups[j].Provider {
			return groups[i].Provider < groups[j].Provider
		}
		return groups[i].RemoteTarget < groups[j].RemoteTarget
	})
	return groups
}

// fromScopeGroups is the inverse of toScopeGroups, used when decoding a v3
// file: each group's Provider/RemoteTarget becomes the registry.Scope key.
func fromScopeGroups(groups []scopeGroup) map[registry.Scope]map[string]map[string]map[string]string {
	out := make(map[registry.Scope]map[string]map[string]map[string]string, len(groups))
	for _, g := range groups {
		if len(g.VMs) == 0 {
			continue
		}
		out[registry.Scope{Provider: g.Provider, RemoteTarget: g.RemoteTarget}] = g.VMs
	}
	return out
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
// An empty path disables persistence (used in tests). It holds no in-process
// mutex, so it is copy-safe to embed by value in the TUI model — callers hold
// a *Store and the TUI passes that pointer through its by-value Update. Data
// is always held in memory as v3 (connScope -> name -> dirScope -> KEY ->
// VALUE), regardless of the on-disk version it was loaded from: the outer
// registry.Scope key is the CONNECTION scope (which host the VM lives on);
// the innermost string key remains the pre-existing directory scope
// (ValidScope) — the two are orthogonal, see the package doc.
//
// Writes are lock-protected read-modify-writes (see mutateLocked): the
// cross-process serialization is a file lock on disk, not an in-process
// mutex, so this type remains safe to copy and there is nothing to guard
// concurrent in-process callers beyond what already held (the TUI is
// single-threaded).
type Store struct {
	path string
	vms  map[registry.Scope]map[string]map[string]map[string]string
}

// NewEmpty returns an in-memory store with no backing file. mutate/saveTree
// are no-ops (or bypass disk entirely) for it, so it never touches disk.
func NewEmpty() *Store {
	return &Store{vms: map[registry.Scope]map[string]map[string]map[string]string{}}
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

// LoadFrom reads the store from an explicit path at process start. A missing
// or empty file yields an empty store (not an error). A corrupt file is moved
// aside to "<path>.corrupt" — so a later save cannot silently clobber
// recoverable data — and the error is returned for the caller to surface. A
// file stamped with a version newer than this build understands is refused
// with an "upgrade sand" error (the store returned still carries its path, so
// -- unlike the registry's equivalent -- a caller that Sets afterward can
// still record new secrets; only this ONE unreadable version's data is
// unavailable). A v1 (or unversioned) file, or a v2 file, is transparently
// migrated: no connection scope existed before version 3, so every VM either
// format recorded is lifted under registry.LocalScope with its secrets (and,
// for v2, its directory scopes) intact; the next save stamps the file as
// version 3. In every case the returned store is non-nil and usable.
//
// The actual decode + in-memory migration is done by the pure, side-effect-
// free parseTree, which this process-start path and the locked reload
// (reloadUnlocked) both share so schema/version handling lives in exactly one
// place and the two can never drift. LoadFrom layers the two Load()-only side
// effects around it -- quarantining a corrupt file and reporting a
// too-new-to-understand one -- that the locked reload deliberately does
// neither of; see reloadUnlocked.
func LoadFrom(path string) (*Store, error) {
	s := &Store{path: path, vms: map[registry.Scope]map[string]map[string]map[string]string{}}
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

	vms, perr := parseTree(data)
	if perr != nil {
		var unsupported unsupportedVersionError
		if errors.As(perr, &unsupported) {
			// A file from a newer sand: valid, just unsupported. Do NOT
			// quarantine or clobber it, but (unlike the registry) do not strip
			// the store's path either -- a subsequent Set would reload this
			// same too-new file and hit the identical refusal before ever
			// reaching a write, so there is nothing to protect by detaching it.
			return s, fmt.Errorf("secrets store at %s %w", path, perr)
		}
		// A genuinely unparseable file: move it aside so a later save cannot
		// silently clobber recoverable data, and surface the error.
		_ = os.Rename(path, path+".corrupt")
		return s, fmt.Errorf("secrets store at %s was unreadable (moved to %s.corrupt): %w", path, path, perr)
	}
	s.vms = vms
	return s, nil
}

// unsupportedVersionError is returned by parseTree when the on-disk file's
// schema version is NEWER than this binary understands. It is distinguished
// from an ordinary decode failure (via errors.As) because LoadFrom's two
// failure paths demand opposite handling: a corrupt file is quarantined to
// .corrupt, but a newer-but-valid file must be left exactly as found for the
// newer sand. Mirrors the registry package's identically-purposed type.
type unsupportedVersionError struct {
	have, understand int
}

func (e unsupportedVersionError) Error() string {
	return fmt.Sprintf("is version %d but this build understands only version %d — upgrade sand", e.have, e.understand)
}

// parseTree decodes the on-disk store bytes into the in-memory connScope ->
// vm -> dirScope -> key -> value tree, migrating a legacy (v1/v2) shape into
// the current (v3) keying IN MEMORY only. It is the single place schema/
// version handling lives, shared by both the process-start LoadFrom path and
// the locked reload (reloadUnlocked), so the two can never drift on how a
// file is interpreted.
//
// It is PURE: no file I/O, no save, no lock, no .corrupt rename, no seeding.
// Those two side effects the old LoadFrom performed inline -- quarantining a
// corrupt file and refusing (without stripping) a too-new one -- are the
// CALLER's responsibility now, and only the process-start path (LoadFrom)
// performs them; the locked reload does neither, which is what keeps a locked
// read-modify-write from double-writing or re-acquiring the lock while merely
// reloading.
//
// Empty input yields an empty tree and is not an error (callers with a
// missing/empty file short-circuit before calling this, but it is safe to
// call directly too). A file whose version exceeds schemaVersion is refused
// with an unsupportedVersionError and a nil tree. Any other decode failure
// (at the version probe, or at any of the three concrete per-version shapes)
// returns the wrapped error and a nil tree so the caller can decide whether to
// quarantine the bytes.
func parseTree(data []byte) (map[registry.Scope]map[string]map[string]map[string]string, error) {
	out := map[registry.Scope]map[string]map[string]map[string]string{}
	if len(data) == 0 {
		return out, nil
	}

	var probe versionProbe
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("decode secrets store: %w", err)
	}

	if probe.Version > schemaVersion {
		return nil, unsupportedVersionError{have: probe.Version, understand: schemaVersion}
	}

	if probe.Version <= 1 {
		// v1 (or unversioned) shape: vms[name] = map[string]string. Decode
		// into the concrete v1 type — NOT the v2/v3 structs, since a string
		// value where those expect a nested map would fail to unmarshal.
		var v1 v1File
		if err := json.Unmarshal(data, &v1); err != nil {
			return nil, fmt.Errorf("decode legacy (v1) secrets store: %w", err)
		}
		byName := make(map[string]map[string]map[string]string, len(v1.VMs))
		for name, pairs := range v1.VMs {
			byName[name] = map[string]map[string]string{"": copyPairs(pairs)}
		}
		if len(byName) > 0 {
			out[registry.LocalScope] = byName
		}
		return out, nil
	}

	if probe.Version == 2 {
		// Pre-connection-scope shape: vms[name][dirscope] = KEY->VALUE, with
		// no notion of which host a VM lives on. Every VM it records predates
		// connection scopes and so could only ever have been local — lift the
		// whole map under registry.LocalScope, secrets and directory scopes
		// intact.
		var parsed fileSchemaV2
		if err := json.Unmarshal(data, &parsed); err != nil {
			return nil, fmt.Errorf("decode legacy (v2) secrets store: %w", err)
		}
		if len(parsed.VMs) > 0 {
			out[registry.LocalScope] = parsed.VMs
		}
		return out, nil
	}

	// probe.Version == 3 (the only remaining case, since >3, <=1, and ==2 are
	// handled above): decode the full v3 shape directly.
	var parsed fileSchema
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("decode secrets store: %w", err)
	}
	if parsed.Scopes != nil {
		out = fromScopeGroups(parsed.Scopes)
	}
	return out, nil
}

// Get returns a defensive copy of the global-scope (directory scope "")
// KEY=VALUE pairs stored for vm under connScope — the registry.Scope
// identifying which host vm lives on (registry.LocalScope for the local Lima
// provider) — or an empty (non-nil) map if none. It is a convenience wrapper
// over GetAll for callers that only care about the global scope. The copy
// prevents a caller from mutating the store's backing map behind its back.
func (s *Store) Get(vm string, connScope registry.Scope) map[string]string {
	out := make(map[string]string, len(s.vms[connScope][vm][""]))
	for k, v := range s.vms[connScope][vm][""] {
		out[k] = v
	}
	return out
}

// Set replaces vm's global-scope (directory scope "") pairs, under connScope,
// with a copy of pairs and persists the change, leaving any other directory
// scope for vm (and any other connection scope entirely) untouched. Every key
// is validated first (ValidKey), BEFORE the lock is taken or anything is
// reloaded/mutated; a single invalid key rejects the whole call without ever
// touching disk, so an injectable key can never be persisted. An empty pairs
// map drops the global scope (and, if no other directory scope remains, vm's
// entry under connScope) rather than persisting an empty object.
//
// The "other directory scope for vm" being preserved is read from the
// FRESHLY RELOADED on-disk tree (inside the locked mutation below), not from
// this Store's possibly-stale in-memory snapshot — otherwise a concurrent
// process's directory-scoped write for the same vm could be silently
// clobbered by this call's blind reconstruction of "everything else".
func (s *Store) Set(vm string, connScope registry.Scope, pairs map[string]string) error {
	for k := range pairs {
		if !ValidKey(k) {
			return fmt.Errorf("invalid secret key %q: keys must match [A-Za-z_][A-Za-z0-9_]*", k)
		}
	}
	cpPairs := copyPairs(pairs)
	return s.mutate(func(cur map[registry.Scope]map[string]map[string]map[string]string) {
		scopes := copyScopeMap(cur[connScope][vm])
		if len(cpPairs) == 0 {
			delete(scopes, "")
		} else {
			scopes[""] = cpPairs
		}
		applyEntry(cur, connScope, vm, pruneEmptyScopes(scopes))
	})
}

// GetAll returns a defensive deep copy of vm's directory-scope -> KEY -> VALUE
// map under connScope, or an empty (non-nil) map if vm has no entry there.
// Mutating the returned map (at any depth) does not affect the store. A
// same-named vm under a DIFFERENT connScope is never visible here — that
// isolation is the whole point of the connection scope.
func (s *Store) GetAll(vm string, connScope registry.Scope) map[string]map[string]string {
	return copyScopeMap(s.vms[connScope][vm])
}

// SetAll replaces vm's directory scopes, under connScope, with a deep copy of
// scopes and persists the change — a full replacement of that one
// (connScope, vm) subtree against the CURRENT on-disk tree, not a whole-store
// overwrite (see mutateLocked). Every directory scope is validated
// (ValidScope) and every key within every scope is validated (ValidKey)
// BEFORE the lock is taken or anything is reloaded/mutated, so the whole call
// is rejected on the first invalid scope or key without ever touching disk
// (all-or-nothing, mirroring PR 27's Set). An empty scopes map, or one whose
// scopes are all empty, drops vm's entry under connScope entirely (pruning
// connScope itself once it holds no VMs) rather than persisting an empty
// object tree.
func (s *Store) SetAll(vm string, connScope registry.Scope, scopes map[string]map[string]string) error {
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

	cp := pruneEmptyScopes(copyScopeMap(scopes))
	return s.mutate(func(cur map[registry.Scope]map[string]map[string]map[string]string) {
		applyEntry(cur, connScope, vm, cp)
	})
}

// Remove drops vm's entry (all directory scopes) under connScope and persists
// the change, leaving any same-named vm under a DIFFERENT connScope untouched.
// The connScope entry itself is pruned once it holds no more VMs, so an empty
// connection scope never lingers on disk. The delete is applied to the
// FRESHLY RELOADED on-disk tree, so a same-named vm entry a concurrent process
// added under connScope after this Store last observed the file is still
// found and removed (and, symmetrically, any OTHER vm a concurrent process
// added is left untouched).
func (s *Store) Remove(vm string, connScope registry.Scope) error {
	return s.mutate(func(cur map[registry.Scope]map[string]map[string]map[string]string) {
		byName := cur[connScope]
		if byName != nil {
			delete(byName, vm)
			if len(byName) == 0 {
				delete(cur, connScope)
			}
		}
	})
}

// copyPairs returns a shallow copy of a KEY -> VALUE map.
func copyPairs(pairs map[string]string) map[string]string {
	out := make(map[string]string, len(pairs))
	for k, v := range pairs {
		out[k] = v
	}
	return out
}

// copyScopeMap returns a deep copy of a directory-scope -> KEY -> VALUE map.
func copyScopeMap(src map[string]map[string]string) map[string]map[string]string {
	out := make(map[string]map[string]string, len(src))
	for scope, pairs := range src {
		out[scope] = copyPairs(pairs)
	}
	return out
}

// pruneEmptyScopes drops any directory scope whose pairs map is empty, so
// nothing ever persists an empty object for a scope that holds no secrets.
func pruneEmptyScopes(scopes map[string]map[string]string) map[string]map[string]string {
	out := make(map[string]map[string]string, len(scopes))
	for scope, pairs := range scopes {
		if len(pairs) > 0 {
			out[scope] = pairs
		}
	}
	return out
}

// applyEntry replaces vm's entire directory-scope subtree under connScope in
// cur with cp (a full replacement, already pruned of empty scopes), pruning
// vm's entry entirely when cp is empty and, in turn, connScope's own group
// once it holds no more VMs. This is the one shared merge step Set (via its
// own reconstructed delta) and SetAll (via the caller-supplied scopes) both
// funnel through, always against the FRESHLY RELOADED tree handed to them by
// mutateLocked/mutate — never against a stale in-memory snapshot — so neither
// can discard an unrelated VM, or an unrelated connection scope, that a
// concurrent process just committed.
func applyEntry(cur map[registry.Scope]map[string]map[string]map[string]string, connScope registry.Scope, vm string, cp map[string]map[string]string) {
	byName := cur[connScope]
	if len(cp) == 0 {
		if byName == nil {
			return
		}
		if _, ok := byName[vm]; !ok {
			return
		}
		delete(byName, vm)
		if len(byName) == 0 {
			delete(cur, connScope)
		}
		return
	}
	if byName == nil {
		byName = map[string]map[string]map[string]string{}
		cur[connScope] = byName
	}
	byName[vm] = cp
}

// mutate is the always-write convenience wrapper over mutateLocked used by
// Set/SetAll/Remove, which — matching their pre-lock behavior — persist
// unconditionally rather than skipping a no-op write. For an in-memory store
// (empty path) it applies apply directly to the working copy: there is
// nothing on disk to lock, reload, or write, so going anywhere near
// mutateLocked/filelock would be pointless (and, for a "" lock path, wrong).
func (s *Store) mutate(apply func(cur map[registry.Scope]map[string]map[string]map[string]string)) error {
	if s.path == "" {
		apply(s.vms)
		return nil
	}
	return s.mutateLocked(func(cur map[registry.Scope]map[string]map[string]map[string]string) (bool, error) {
		apply(cur)
		return true, nil
	})
}

// mutateLocked performs one lock-protected read-modify-write against the
// on-disk store. It takes the cross-process lock (best-effort: a lock it
// cannot take only WARNS and proceeds unserialized — a wedged or unwritable
// lock file must never fail an otherwise-valid write), reloads the CURRENT
// on-disk tree via reloadUnlocked, hands that fresh tree to apply so the
// caller can layer ONLY this operation's delta onto it, and — when apply asks
// — persists the merged result via the existing atomic temp+rename body
// (saveTree). On success it refreshes the in-memory working copy from the
// merged tree, so later reads (Get/GetAll) see exactly what hit disk,
// including entries a concurrent process added.
//
// The lock is taken EXACTLY ONCE, here; neither reloadUnlocked nor saveTree
// re-acquires it, so there is no re-entrant self-deadlock (a fresh flock fd on
// the same path is a distinct open file description and would otherwise
// block). Only called with s.path != "" — mutate handles the in-memory case
// directly, since there is nothing on disk to lock or reload there.
func (s *Store) mutateLocked(apply func(cur map[registry.Scope]map[string]map[string]map[string]string) (write bool, err error)) error {
	// Ensure the data dir exists before taking the lock: the lock file lives
	// beside the store, so on a first-ever write the dir may not exist yet, and
	// a missing dir would needlessly degrade that first lock to the
	// unserialized path. saveTree re-creates (and force-chmods) it too; both
	// are best-effort here.
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.warnf("could not create data dir for %s (%v); proceeding", s.path, err)
	}
	release, err := filelock.Acquire(s.path + ".lock")
	if err != nil {
		s.warnf("could not lock %s (%v); writing without cross-process serialization", s.path, err)
	}
	defer release()

	cur, err := s.reloadUnlocked()
	if err != nil {
		return err
	}
	write, err := apply(cur)
	if err != nil {
		return err
	}
	if write {
		if err := s.saveTree(cur); err != nil {
			return err
		}
	}
	s.vms = cur // refresh the working copy from the merged, on-disk-current state
	return nil
}

// reloadUnlocked re-reads the on-disk store fresh and decodes it in memory via
// the pure parseTree, returning the CURRENT connScope -> vm -> dirScope -> key
// -> value tree. It is the read half of every locked read-modify-write. It
// does NOT take the lock or quarantine a corrupt file — the caller already
// holds the lock (taken once at the mutation boundary), so re-acquiring here
// would self-deadlock on a fresh flock fd, and quarantining here would move a
// file out from under a live mutation. A missing or empty file yields an
// empty tree. A v1/v2 file is migrated in memory but deliberately NOT written
// back — the merged result is persisted exactly once, by the caller's single
// saveTree call, already stamped at the current schema version. A decode
// failure (or a newer-sand file) is surfaced so the caller ABORTS the
// mutation rather than clobbering an unreadable or newer on-disk file.
func (s *Store) reloadUnlocked() (map[registry.Scope]map[string]map[string]map[string]string, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[registry.Scope]map[string]map[string]map[string]string{}, nil
		}
		return nil, err
	}
	return parseTree(data)
}

// warnf emits a best-effort operational note about a DEGRADED write — today
// only a failure to take the cross-process lock, after which the mutation
// proceeds unserialized. It never affects control flow and never fails a
// mutation. The note goes to stderr, matching the registry package's own
// warning channel (internal/registry.warnf) and the headless `sand create`
// path's warnings; it is intentionally visible so a user can tell a write was
// not serialized against concurrent sand processes.
func (s *Store) warnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "warning: secrets store: "+format+"\n", args...)
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

// saveTree atomically writes vms to the backing file: a unique temp file in
// the same directory as the target, created at 0600 BEFORE any secret bytes
// are written, is renamed over the target. The parent directory is forced to
// 0700. There is therefore no instant at which a world-readable file holds a
// secret. An empty path is a no-op, so an in-memory store never touches disk.
// The unique temp name keeps two TUI processes sharing a data dir from racing
// on a fixed name.
//
// Writes are now lock-protected read-modify-writes: every mutation reloads
// the current on-disk tree under the cross-process lock (see mutateLocked)
// and calls this with the MERGED tree, so two sand processes sharing a data
// dir can no longer silently discard each other's committed secrets. The
// atomic temp+rename still guarantees a reader never observes a half-written
// file, and the unique temp name keeps two writers from colliding on a shared
// temp path. This function's own bytes (mode 0600 temp, forced 0700 dir,
// fsync before rename) are UNCHANGED from the pre-lock save() — only the
// caller now wraps it in a lock+reload+merge.
func (s *Store) saveTree(vms map[registry.Scope]map[string]map[string]map[string]string) error {
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

	data, err := json.MarshalIndent(fileSchema{Version: schemaVersion, Scopes: toScopeGroups(vms)}, "", "  ")
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

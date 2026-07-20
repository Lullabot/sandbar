// Package registry is now a cache, known-targets list, and one-release legacy fallback
// for ownership decisions: the source of truth is the provider-side provenance marker
// (sandbar.json in the instance directory, read via internal/provider/Provenancer).
// This registry still tracks which Lima instances were created by sand so the TUI can
// mark them and gate destructive operations, but it no longer decides ownership.
// Legacy entries (from before provenance markers existed) are adopted once per process
// per scope (see Adopt) to stamp markers onto unmarked instances during upgrade; after
// one release in the wild, the fallback path can be removed (see "legacy, remove after
// one release" comments in manage.RecreateBase and internal/ui/board.go).
// The registry still serves as a known-targets list, keeping entries keyed by scope
// (profile identity) for quick lookups that don't require a provider round trip.
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/lullabot/sandbar/internal/vm"
)

// entry is the per-VM record. Config is the (secret-free) create configuration
// so a later recreate reproduces the VM's sizing and identity instead of
// silently resetting them to defaults. Base mirrors Config.BaseName and is kept
// as a small, stable field for callers that only need the clone source.
//
// Provider and RemoteTarget record which backend owns this VM (schema version
// 2; see currentVersion and the migration in LoadFrom). Provider is a backend
// identifier such as LocalProviderID ("lima"); RemoteTarget is a stable,
// secret-free identity for a remote provider's target (e.g. "user@host:22") and
// is empty for the local provider. Together they are what stops a remote
// host's VM from being reconciled against, or colliding with, a local one that
// happens to share a name — see Scope.
type entry struct {
	Base         string          `json:"base"`
	Config       vm.CreateConfig `json:"config"`
	Provider     string          `json:"provider"`
	RemoteTarget string          `json:"remote_target,omitempty"`
}

// LocalProviderID is the Provider tag every local-Lima-owned entry carries: the
// default for every entry Add adds, and what the version-2 migration stamps
// onto every pre-migration entry (which could only ever have been local, since
// no remote provider existed when they were written).
const LocalProviderID = "lima"

// Scope identifies which provider — and, for a remote provider, which remote
// target — owns a set of registry entries. Operations that must not cross
// providers (Reconcile, and provider-scoped lookups like BaseInScope) take a
// Scope so a `List` from one provider's live instances can never prune or
// match another provider's entries. RemoteTarget is empty for the local
// provider; a remote provider's Scope carries a stable, secret-free identity
// for its remote host (e.g. "user@host:22") — never a private key or password.
type Scope struct {
	Provider     string
	RemoteTarget string
}

// LocalScope is the Scope every sand entrypoint uses when unconfigured (an
// unconfigured `sand` only ever talks to local Lima). Provider selection
// (provider.Resolve) constructs a remote Scope from its resolved target
// configuration instead.
//
// Scope is comparable (a plain Provider+RemoteTarget struct), so since this
// task it doubles as half of the in-memory index key (see scopedKey): the
// registry no longer needs a separate "does this entry belong to that scope"
// predicate — a (scope, name) map lookup either finds the entry or it
// doesn't, and two entries sharing a name under different scopes are simply
// two different keys.
var LocalScope = Scope{Provider: LocalProviderID}

// currentVersion is the schema version this binary writes. A file with no
// version predates versioning and is read as version 1.
//
// Version 2 did two things at once: it renamed the default base image from
// claude-base to sandbar-base (the project outgrew the agent that used to ship
// inside its base), and it added the per-entry Provider/RemoteTarget tag (see
// entry). A file written by an older sand records the old base name in every
// entry and carries no provider tag, so LoadFrom rewrites both on read and
// stamps the file version 2 so the rewrite runs at most once. See renameBase.
//
// Version 3 re-keys the index by (scope, name) instead of bare name, so a VM
// named "web" on one connection profile and a "web" on another can coexist
// (see scopedKey and Scope). A flat {"name": entry} JSON object cannot hold
// two same-named entries, so the on-disk shape changes from that object to a
// JSON ARRAY of entries (each self-describing: name+provider+remote_target+
// base+config). LoadFrom lifts every v2 entry into the new keying using its
// OWN recorded Provider/RemoteTarget (defaulting to the local provider, which
// is what every v1-migrated entry already carries), so a v2 file that already
// recorded remote-scoped entries (AddScoped predates this task) keeps their
// scope rather than collapsing everything to local.
const currentVersion = 3

// legacyBaseName is the base image's pre-v2 name. Entries recorded under it are
// rewritten to the current default base (vm.DefaultCreateConfig().BaseName) on
// load — the same rename the provisioner applies to the Lima instance itself.
const legacyBaseName = "claude-base"

// scopedKey is the in-memory index key: entries are unique per (scope, name),
// not per bare name, so two providers (or two remote targets) may legitimately
// record a VM with the same name. Scope is a comparable struct (Provider +
// RemoteTarget), so this is a valid map key.
type scopedKey struct {
	scope Scope
	name  string
}

// diskEntry is the v3 on-disk shape for one registry entry: a JSON array
// element self-describing its own name and scope, since the array (unlike the
// old flat map) does not use the name as a JSON key. See currentVersion.
type diskEntry struct {
	Name         string          `json:"name"`
	Provider     string          `json:"provider"`
	RemoteTarget string          `json:"remote_target,omitempty"`
	Base         string          `json:"base"`
	Config       vm.CreateConfig `json:"config"`
}

// fileSchema is the on-disk JSON shape for the current version:
// {"version": 3, "vms": [{"name": "...", ...}, ...]}.
type fileSchema struct {
	Version int         `json:"version"`
	VMs     []diskEntry `json:"vms"`
}

// legacyFileSchema is the pre-v3 on-disk shape (versions 1 and 2): a flat
// {"version": N, "vms": {"<name>": {...}}} object, keyed by bare name. Parsed
// only during migration in LoadFrom.
type legacyFileSchema struct {
	Version int              `json:"version"`
	VMs     map[string]entry `json:"vms"`
}

// versionProbe reads just the version field so LoadFrom can decide whether
// "vms" is the legacy object shape or the current array shape before
// unmarshaling it — the two are not JSON-compatible with a single struct.
type versionProbe struct {
	Version int `json:"version"`
}

// Registry is an in-memory index of sand-managed instances, optionally
// backed by a JSON file. An empty path disables persistence (used in tests).
type Registry struct {
	path string
	vms  map[scopedKey]entry
}

// NewEmpty returns an in-memory registry with no backing file.
func NewEmpty() *Registry {
	return &Registry{vms: map[scopedKey]entry{}}
}

// defaultPath mirrors the original bash provisioner's data dir:
// ${XDG_DATA_HOME:-$HOME/.local/share}/sandbar/managed-vms.json.
func defaultPath() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			home = "."
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "sandbar", "managed-vms.json")
}

// migrateLegacyIndex copies a pre-rename managed index from the old
// claude-code-ansible data dir into the new sandbar dir exactly once,
// copy-before-remove so a crash cannot lose it.
func migrateLegacyIndex(newPath string) {
	if _, err := os.Stat(newPath); err == nil {
		return // new index already present; nothing to do
	}
	base := filepath.Dir(filepath.Dir(newPath)) // .../.local/share
	oldPath := filepath.Join(base, "claude-code-ansible", "managed-vms.json")
	data, err := os.ReadFile(oldPath)
	if err != nil {
		return // no legacy index
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(newPath, data, 0o600); err != nil {
		return
	}
	// verify the new file reads back before removing the old one
	if back, err := os.ReadFile(newPath); err != nil || len(back) != len(data) {
		return
	}
	_ = os.Remove(oldPath)
	_ = os.Remove(filepath.Join(base, "claude-code-ansible")) // rmdir if empty
}

// Load reads the registry from the default path.
func Load() (*Registry, error) {
	p := defaultPath()
	migrateLegacyIndex(p)
	return LoadFrom(p)
}

// LoadFrom reads the registry from an explicit path. A missing or empty file
// yields an empty registry (not an error). A corrupt file is moved aside to
// "<path>.corrupt" — so a later save() cannot silently clobber recoverable
// data — and the error is returned for the caller to surface; the returned
// registry is always non-nil and usable.
//
// Three on-disk shapes must be understood here: an unversioned (v1) or v2
// file is the legacy flat {"vms": {"<name>": {...}}} object (legacyFileSchema)
// keyed by bare name; a v3 file is the current {"vms": [{...}, ...]} array
// (fileSchema), already self-describing each entry's (scope, name). A
// versionProbe reads just the version field first because the two shapes are
// not both unmarshalable into one struct.
func LoadFrom(path string) (*Registry, error) {
	r := &Registry{path: path, vms: map[scopedKey]entry{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return r, nil
		}
		return r, err
	}
	if len(data) == 0 {
		return r, nil
	}
	var probe versionProbe
	if err := json.Unmarshal(data, &probe); err != nil {
		_ = os.Rename(path, path+".corrupt")
		return r, fmt.Errorf("managed-VM index at %s was unreadable (moved to %s.corrupt): %w", path, path, err)
	}
	version := probe.Version
	if version == 0 {
		version = 1 // unversioned file predates the version field
	}
	if version > currentVersion {
		return NewEmpty(), fmt.Errorf(
			"managed index %s has schema version %d, but this sand only understands %d; upgrade sand",
			path, version, currentVersion)
	}

	needsSave := false
	if version < currentVersion {
		// v1 or v2: the legacy flat object, keyed by bare name.
		var legacy legacyFileSchema
		if err := json.Unmarshal(data, &legacy); err != nil {
			_ = os.Rename(path, path+".corrupt")
			return r, fmt.Errorf("managed-VM index at %s was unreadable (moved to %s.corrupt): %w", path, path, err)
		}
		vms := legacy.VMs
		if vms == nil {
			vms = map[string]entry{}
		}
		// A pre-v2 file records the old base name in every entry AND carries no
		// Provider tag (no non-local provider existed when it was written).
		// Rewrite BOTH here, in memory, before lifting into the new (scope,
		// name) keying: rename the legacy base to the current default so the
		// TUI groups clones under the base the provisioner will rename their
		// source to, and stamp every entry local.
		if version < 2 {
			renameLegacyBase(vms, legacyBaseName, vm.DefaultCreateConfig().BaseName)
			for name, e := range vms {
				if e.Provider == "" {
					e.Provider = LocalProviderID
					vms[name] = e
				}
			}
		}
		// Lift each entry into (scope, name) keying using ITS OWN recorded
		// Provider/RemoteTarget — every v1-migrated entry above is now tagged
		// LocalProviderID, but a v2 file may already carry remote-scoped
		// entries (AddScoped predates this task), and those must keep their
		// own scope rather than collapse to local.
		for name, e := range vms {
			scope := Scope{Provider: e.Provider, RemoteTarget: e.RemoteTarget}
			if scope.Provider == "" {
				scope = LocalScope
			}
			r.vms[scopedKey{scope: scope, name: name}] = e
		}
		needsSave = true
	} else {
		// v3: already (scope, name)-shaped, one array element per entry.
		var parsed fileSchema
		if err := json.Unmarshal(data, &parsed); err != nil {
			_ = os.Rename(path, path+".corrupt")
			return r, fmt.Errorf("managed-VM index at %s was unreadable (moved to %s.corrupt): %w", path, path, err)
		}
		for _, de := range parsed.VMs {
			scope := Scope{Provider: de.Provider, RemoteTarget: de.RemoteTarget}
			if scope.Provider == "" {
				scope = LocalScope
			}
			r.vms[scopedKey{scope: scope, name: de.Name}] = entry{
				Base: de.Base, Config: de.Config, Provider: de.Provider, RemoteTarget: de.RemoteTarget,
			}
		}
	}

	if needsSave {
		// Best-effort persist. The in-memory registry is already correctly
		// migrated, so this write is only about durability — and it must NOT be
		// fatal to a load. A read-only or full data dir would otherwise make
		// EVERY `sand`/`sand create` invocation surface a migration error, where
		// the old (pure-read) LoadFrom loaded the same file silently; the next
		// successful mutating save() persists the version bump instead.
		_ = r.save()
	}
	return r, nil
}

// renameLegacyBase rewrites every entry in vms whose base is from to to, in
// both the small Base field and the embedded Config.BaseName (the two are
// kept in step — Add writes both from one cfg). It operates on the legacy
// bare-name-keyed map during migration, before entries are lifted into the
// (scope, name) keying — this is the registry half of the base-image rename;
// the provisioner renames the Lima instance itself under the base lock.
func renameLegacyBase(vms map[string]entry, from, to string) {
	if from == to {
		return
	}
	for name, e := range vms {
		changed := false
		if e.Base == from {
			e.Base = to
			changed = true
		}
		if e.Config.BaseName == from {
			e.Config.BaseName = to
			changed = true
		}
		if changed {
			vms[name] = e
		}
	}
}

// IsManaged reports whether name was created by sand under the local Lima
// provider. Equivalent to IsManagedInScope(name, LocalScope) — kept as the
// unscoped convenience every existing (local-only) caller uses.
func (r *Registry) IsManaged(name string) bool {
	return r.IsManagedInScope(name, LocalScope)
}

// IsManagedInScope reports whether name is a managed VM owned by scope. Unlike
// IsManaged, it does not match an entry that belongs to a different provider —
// so a remote provider never treats a same-named local entry as its own, which
// is the whole point of Scope (a same-named VM must not cross providers). The
// index is keyed by (scope, name), so this is a direct lookup: two entries
// sharing name under different scopes cannot shadow one another.
func (r *Registry) IsManagedInScope(name string, scope Scope) bool {
	_, ok := r.vms[scopedKey{scope: scope, name: name}]
	return ok
}

// Base returns the base image a managed VM was cloned from under the local
// Lima provider, or "" if unknown. Equivalent to the Base half of
// BaseInScope(name, LocalScope) — kept as the unscoped convenience every
// existing (local-only) caller uses.
func (r *Registry) Base(name string) string {
	base, _ := r.BaseInScope(name, LocalScope)
	return base
}

// IsBase reports whether name is a base image that at least one managed VM —
// under ANY scope — was cloned from. (The default base name is also treated
// as a base by the UI even before any clone records it.) This intentionally
// scans every scope: a base image is shared infrastructure, not something a
// single connection profile owns.
func (r *Registry) IsBase(name string) bool {
	if name == "" {
		return false
	}
	for _, e := range r.vms {
		if e.Base == name {
			return true
		}
	}
	return false
}

// Config returns the stored create configuration for a managed VM under the
// local Lima provider (with its clone token stripped) and whether the VM is
// managed. Equivalent to ConfigInScope(name, LocalScope) — kept as the
// unscoped convenience every existing (local-only) caller uses.
func (r *Registry) Config(name string) (vm.CreateConfig, bool) {
	return r.ConfigInScope(name, LocalScope)
}

// ConfigInScope returns the stored create configuration for a managed VM owned
// by scope (clone token stripped) and whether such an entry exists. It is the
// scoped counterpart to Config: a remote provider must not read a same-named
// local entry's recorded user/sizing (e.g. resolving the guest user secrets are
// applied as), which would otherwise silently target the wrong account.
func (r *Registry) ConfigInScope(name string, scope Scope) (vm.CreateConfig, bool) {
	e, ok := r.vms[scopedKey{scope: scope, name: name}]
	if !ok {
		return vm.CreateConfig{}, false
	}
	return e.Config, true
}

// Add records cfg as a managed VM keyed by cfg.Name and persists the change,
// tagged as owned by the local Lima provider (LocalScope). The clone token is
// stripped first: secrets never touch the on-disk index. Equivalent to
// AddScoped(cfg, LocalScope) — kept as the unscoped convenience every existing
// caller uses, since sand has only ever had one provider until now.
func (r *Registry) Add(cfg vm.CreateConfig) error {
	return r.AddScoped(cfg, LocalScope)
}

// AddScoped records cfg as a managed VM keyed by (scope, cfg.Name) and
// persists the change. The clone token is stripped first: secrets never touch
// the on-disk index (nor does scope carry one — see Scope). Because the key
// includes scope, calling this with the same name under two different scopes
// records two independent entries — neither overwrites the other, which is
// the whole point of this task's re-keying (a VM named "web" on one
// connection profile and a "web" on another must coexist).
func (r *Registry) AddScoped(cfg vm.CreateConfig, scope Scope) error {
	cfg.CloneToken = ""
	r.vms[scopedKey{scope: scope, name: cfg.Name}] = entry{
		Base: cfg.BaseName, Config: cfg, Provider: scope.Provider, RemoteTarget: scope.RemoteTarget,
	}
	return r.save()
}

// Remove drops name from the index under the local Lima provider and persists
// the change. Equivalent to RemoveScoped(LocalScope, name) — kept as the
// unscoped convenience every existing (local-only) caller uses. A caller
// acting on a VM that could be remote (e.g. the TUI's delete path) must use
// RemoveScoped with that VM's own scope instead, or it would target
// LocalScope and leave the real (remote-scoped) entry dangling.
func (r *Registry) Remove(name string) error {
	return r.RemoveScoped(LocalScope, name)
}

// RemoveScoped drops the (scope, name) entry from the index and persists the
// change. It never touches a same-named entry recorded under a different
// scope — the whole point of the (scope, name) keying this task introduces.
func (r *Registry) RemoveScoped(scope Scope, name string) error {
	delete(r.vms, scopedKey{scope: scope, name: name})
	return r.save()
}

// Reconcile drops managed entries whose VM no longer exists; present is the set
// of live instance names. It returns the names that were dropped (nil if none
// were), so a caller with its own per-VM state keyed by that name (the TUI's
// secrets store) can prune it in step — this is the single shared place the
// TUI and headless `sand create` path agree on reconciliation, so it must
// carry enough information for both to stay in sync, not just the TUI's
// original bool. This keeps a stale entry from lingering after a VM is
// deleted outside the TUI. It cannot detect a name being *reused* by an
// unrelated VM — provenance is not recoverable from limactl — which is why
// recreate still requires an explicit confirmation.
//
// Equivalent to ReconcileScoped(LocalScope, present) — kept as the unscoped
// convenience every existing (local-only) caller uses.
func (r *Registry) Reconcile(present map[string]bool) ([]string, error) {
	return r.ReconcileScoped(LocalScope, present)
}

// ReconcileScoped is Reconcile scoped to a single provider: only entries
// keyed under scope are considered for pruning, and present is that SAME
// provider's live instance list. An entry owned by a different scope (a
// remote host's VM, or vice versa) is left untouched no matter what present
// contains — a listing from one provider must never prune, or be mistaken
// for, another provider's entries, since two providers (or a same-named VM
// under two scopes) can legitimately reuse the same VM name.
func (r *Registry) ReconcileScoped(scope Scope, present map[string]bool) ([]string, error) {
	var dropped []string
	for key := range r.vms {
		if key.scope != scope {
			continue
		}
		if !present[key.name] {
			delete(r.vms, key)
			dropped = append(dropped, key.name)
		}
	}
	if len(dropped) == 0 {
		return nil, nil
	}
	return dropped, r.save()
}

// BaseInScope returns the base image recorded for name, and whether name is
// managed AND owned by scope — the provider-scoped counterpart to Base+
// IsManaged that RecreateBase (internal/manage) uses so a VM owned by one
// provider can never be recreated (nor even reported managed) from another
// provider's scope.
func (r *Registry) BaseInScope(name string, scope Scope) (base string, managed bool) {
	e, ok := r.vms[scopedKey{scope: scope, name: name}]
	if !ok {
		return "", false
	}
	return e.Base, true
}

// save writes the index atomically (unique temp file + rename). With an empty
// path it is a no-op, so an in-memory registry never touches disk. The temp file
// is unique per write so two TUI processes sharing a data dir don't race on a
// shared name. Entries are written in a stable (scope, name) sort order so two
// saves of the same logical state produce byte-identical output — otherwise Go
// map iteration order would make the file's array order (and therefore its
// diff) flap on every unrelated save.
func (r *Registry) save() error {
	if r.path == "" {
		return nil
	}
	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	keys := make([]scopedKey, 0, len(r.vms))
	for k := range r.vms {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.scope.Provider != b.scope.Provider {
			return a.scope.Provider < b.scope.Provider
		}
		if a.scope.RemoteTarget != b.scope.RemoteTarget {
			return a.scope.RemoteTarget < b.scope.RemoteTarget
		}
		return a.name < b.name
	})
	vms := make([]diskEntry, 0, len(keys))
	for _, k := range keys {
		e := r.vms[k]
		// Provider/RemoteTarget come from the KEY's scope, not the entry's own
		// (redundant) copy of those fields — the key is authoritative for every
		// lookup, so the on-disk self-description must always agree with it even
		// if an in-memory entry's own Provider/RemoteTarget were ever left unset.
		vms = append(vms, diskEntry{
			Name: k.name, Provider: k.scope.Provider, RemoteTarget: k.scope.RemoteTarget, Base: e.Base, Config: e.Config,
		})
	}
	data, err := json.MarshalIndent(fileSchema{Version: currentVersion, VMs: vms}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".managed-vms-*.json.tmp") // 0600 by default
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, r.path)
}

// Package registry tracks which Lima instances were created by sand so the
// TUI can mark them and gate destructive operations. This matters because
// recreate clones from a Claude base image and would replace ANY instance it is
// pointed at; Lima does not record a clone's source, so we keep our own small
// JSON index under the XDG data dir (the same location the original bash
// provisioner used for its cache).
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

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
// (plan 15 task 5) constructs a remote Scope from its resolved target
// configuration instead.
var LocalScope = Scope{Provider: LocalProviderID}

// matches reports whether e is owned by s. A pre-migration entry (Provider
// unset) is treated as local — LoadFrom normalizes this on load, so this
// fallback only matters for an entry constructed in memory before a save.
func (s Scope) matches(e entry) bool {
	p := e.Provider
	if p == "" {
		p = LocalProviderID
	}
	return p == s.Provider && e.RemoteTarget == s.RemoteTarget
}

// currentVersion is the schema version this binary writes. A file with no
// version predates versioning and is read as version 1. Version 2 added the
// per-entry Provider/RemoteTarget tag (see entry and LoadFrom's migration).
const currentVersion = 2

// fileSchema is the on-disk JSON shape: {"version": N, "vms": {"<name>": {...}}}.
type fileSchema struct {
	Version int              `json:"version"`
	VMs     map[string]entry `json:"vms"`
}

// Registry is an in-memory index of sand-managed instances, optionally
// backed by a JSON file. An empty path disables persistence (used in tests).
type Registry struct {
	path string
	vms  map[string]entry
}

// NewEmpty returns an in-memory registry with no backing file.
func NewEmpty() *Registry {
	return &Registry{vms: map[string]entry{}}
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
func LoadFrom(path string) (*Registry, error) {
	r := &Registry{path: path, vms: map[string]entry{}}
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
	var parsed fileSchema
	if err := json.Unmarshal(data, &parsed); err != nil {
		_ = os.Rename(path, path+".corrupt")
		return r, fmt.Errorf("managed-VM index at %s was unreadable (moved to %s.corrupt): %w", path, path, err)
	}
	if parsed.Version == 0 {
		parsed.Version = 1 // unversioned file predates the version field
	}
	if parsed.Version > currentVersion {
		return NewEmpty(), fmt.Errorf(
			"managed index %s has schema version %d, but this sand only understands %d; upgrade sand",
			path, parsed.Version, currentVersion)
	}
	if parsed.VMs != nil {
		r.vms = parsed.VMs
	}
	if parsed.Version < currentVersion {
		// Pre-version-2 file: no entry has ever had a Provider tag, because no
		// non-local provider existed yet when it was written. Stamp every entry
		// local and re-save — save() is the existing atomic temp-file+rename
		// writer, so the old file is never truncated before the new one is
		// durably in place, and it always stamps currentVersion, which is what
		// bumps the on-disk version here.
		for name, e := range r.vms {
			if e.Provider == "" {
				e.Provider = LocalProviderID
				r.vms[name] = e
			}
		}
		if err := r.save(); err != nil {
			return r, fmt.Errorf("managed-VM index at %s: migrating to schema version %d: %w", path, currentVersion, err)
		}
	}
	return r, nil
}

// IsManaged reports whether name was created by sand.
func (r *Registry) IsManaged(name string) bool {
	_, ok := r.vms[name]
	return ok
}

// Base returns the base image a managed VM was cloned from, or "" if unknown.
func (r *Registry) Base(name string) string {
	return r.vms[name].Base
}

// IsBase reports whether name is a base image that at least one managed VM was
// cloned from. (The default base name is also treated as a base by the UI even
// before any clone records it.)
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

// Config returns the stored create configuration for a managed VM (with its
// clone token stripped) and whether the VM is managed.
func (r *Registry) Config(name string) (vm.CreateConfig, bool) {
	e, ok := r.vms[name]
	return e.Config, ok
}

// Add records cfg as a managed VM keyed by cfg.Name and persists the change,
// tagged as owned by the local Lima provider (LocalScope). The clone token is
// stripped first: secrets never touch the on-disk index. Equivalent to
// AddScoped(cfg, LocalScope) — kept as the unscoped convenience every existing
// caller uses, since sand has only ever had one provider until now.
func (r *Registry) Add(cfg vm.CreateConfig) error {
	return r.AddScoped(cfg, LocalScope)
}

// AddScoped records cfg as a managed VM keyed by cfg.Name, tagged as owned by
// scope, and persists the change. The clone token is stripped first: secrets
// never touch the on-disk index (nor does scope carry one — see Scope).
func (r *Registry) AddScoped(cfg vm.CreateConfig, scope Scope) error {
	cfg.CloneToken = ""
	r.vms[cfg.Name] = entry{Base: cfg.BaseName, Config: cfg, Provider: scope.Provider, RemoteTarget: scope.RemoteTarget}
	return r.save()
}

// Remove drops name from the index and persists the change.
func (r *Registry) Remove(name string) error {
	delete(r.vms, name)
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
// matching scope are considered for pruning, and present is that SAME
// provider's live instance list. An entry owned by a different provider (a
// remote host's VM, or vice versa) is left untouched no matter what present
// contains — a listing from one provider must never prune, or be mistaken
// for, another provider's entries, since two providers can legitimately reuse
// the same VM name.
func (r *Registry) ReconcileScoped(scope Scope, present map[string]bool) ([]string, error) {
	var dropped []string
	for name, e := range r.vms {
		if !scope.matches(e) {
			continue
		}
		if !present[name] {
			delete(r.vms, name)
			dropped = append(dropped, name)
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
	e, ok := r.vms[name]
	if !ok || !scope.matches(e) {
		return "", false
	}
	return e.Base, true
}

// save writes the index atomically (unique temp file + rename). With an empty
// path it is a no-op, so an in-memory registry never touches disk. The temp file
// is unique per write so two TUI processes sharing a data dir don't race on a
// shared name.
func (r *Registry) save() error {
	if r.path == "" {
		return nil
	}
	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(fileSchema{Version: currentVersion, VMs: r.vms}, "", "  ")
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

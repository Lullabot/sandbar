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
type entry struct {
	Base   string          `json:"base"`
	Config vm.CreateConfig `json:"config"`
}

// currentVersion is the schema version this binary writes. A file with no
// version predates versioning and is read as version 1.
const currentVersion = 1

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

// Add records cfg as a managed VM keyed by cfg.Name and persists the change. The
// clone token is stripped first: secrets never touch the on-disk index.
func (r *Registry) Add(cfg vm.CreateConfig) error {
	cfg.CloneToken = ""
	r.vms[cfg.Name] = entry{Base: cfg.BaseName, Config: cfg}
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
func (r *Registry) Reconcile(present map[string]bool) ([]string, error) {
	var dropped []string
	for name := range r.vms {
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

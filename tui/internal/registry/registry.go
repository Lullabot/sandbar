// Package registry tracks which Lima instances were created by claude-vm so the
// TUI can mark them and gate destructive operations. This matters because
// recreate clones from a Claude base image and would replace ANY instance it is
// pointed at; Lima does not record a clone's source, so we keep our own small
// JSON index under the XDG data dir (the same location new-vm.sh uses for its
// cache).
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/deviantintegral/claude-code-ansible/tui/internal/vm"
)

// entry is the per-VM record. Config is the (secret-free) create configuration
// so a later recreate reproduces the VM's sizing and identity instead of
// silently resetting them to defaults. Base mirrors Config.BaseName and is kept
// as a small, stable field for callers that only need the clone source.
type entry struct {
	Base   string          `json:"base"`
	Config vm.CreateConfig `json:"config"`
}

// fileSchema is the on-disk JSON shape: {"vms": {"<name>": {...}}}.
type fileSchema struct {
	VMs map[string]entry `json:"vms"`
}

// Registry is an in-memory index of claude-vm-managed instances, optionally
// backed by a JSON file. An empty path disables persistence (used in tests).
type Registry struct {
	path string
	vms  map[string]entry
}

// NewEmpty returns an in-memory registry with no backing file.
func NewEmpty() *Registry {
	return &Registry{vms: map[string]entry{}}
}

// defaultPath mirrors new-vm.sh's data dir:
// ${XDG_DATA_HOME:-$HOME/.local/share}/claude-code-ansible/managed-vms.json.
func defaultPath() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			home = "."
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "claude-code-ansible", "managed-vms.json")
}

// Load reads the registry from the default path.
func Load() (*Registry, error) {
	return LoadFrom(defaultPath())
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
	if parsed.VMs != nil {
		r.vms = parsed.VMs
	}
	return r, nil
}

// IsManaged reports whether name was created by claude-vm.
func (r *Registry) IsManaged(name string) bool {
	_, ok := r.vms[name]
	return ok
}

// Base returns the base image a managed VM was cloned from, or "" if unknown.
func (r *Registry) Base(name string) string {
	return r.vms[name].Base
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
// of live instance names. It returns whether anything was pruned. This keeps a
// stale entry from lingering after a VM is deleted outside the TUI. It cannot
// detect a name being *reused* by an unrelated VM — provenance is not
// recoverable from limactl — which is why recreate still requires an explicit
// confirmation.
func (r *Registry) Reconcile(present map[string]bool) (bool, error) {
	changed := false
	for name := range r.vms {
		if !present[name] {
			delete(r.vms, name)
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	return true, r.save()
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
	data, err := json.MarshalIndent(fileSchema{VMs: r.vms}, "", "  ")
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

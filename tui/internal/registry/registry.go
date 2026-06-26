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
	"io/fs"
	"os"
	"path/filepath"
)

// entry is the per-VM record. Base is the base image the VM was cloned from, so
// a later recreate can clone from the same base.
type entry struct {
	Base string `json:"base"`
}

// fileSchema is the on-disk JSON shape: {"vms": {"<name>": {"base": "..."}}}.
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
// yields an empty registry (not an error); a corrupt file returns an error
// alongside a usable empty registry so callers can degrade gracefully.
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
		return r, err
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

// Add records name (cloned from base) as managed and persists the change.
func (r *Registry) Add(name, base string) error {
	r.vms[name] = entry{Base: base}
	return r.save()
}

// Remove drops name from the index and persists the change.
func (r *Registry) Remove(name string) error {
	delete(r.vms, name)
	return r.save()
}

// save writes the index atomically (temp file + rename). With an empty path it
// is a no-op, so an in-memory registry never touches disk.
func (r *Registry) save() error {
	if r.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(fileSchema{VMs: r.vms}, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

package profiles

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// currentVersion is the schema version this binary writes.
const currentVersion = 1

// fileSchema is the on-disk YAML shape.
type fileSchema struct {
	Version  int       `yaml:"version"`
	LastUsed string    `yaml:"last_used,omitempty"`
	Profiles []Profile `yaml:"profiles"`
}

// Store is an in-memory set of profiles, optionally backed by a YAML file at
// path. An empty path disables persistence (used in tests that don't care
// about disk).
type Store struct {
	path     string
	profiles map[string]Profile
	order    []string // insertion order, for stable List() output
	lastUsed string
}

// defaultPath returns ${XDG_CONFIG_HOME:-~/.config}/sandbar/profiles.yaml.
func defaultPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			home = "."
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "sandbar", "profiles.yaml")
}

// Load reads the store from the default path, seeding a single enabled Local
// profile if no file exists yet.
func Load() (*Store, error) {
	return LoadFrom(defaultPath())
}

// LoadFrom reads the store from an explicit path. A missing or empty file
// seeds a single enabled Local profile and persists it immediately, so an
// unconfigured sand behaves as today. A corrupt file is moved aside to
// "<path>.corrupt" (so a later save cannot silently clobber recoverable
// data), the error is returned, and the returned store is still seeded and
// usable — a mangled file never bricks startup.
func LoadFrom(path string) (*Store, error) {
	s := &Store{path: path, profiles: map[string]Profile{}}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s, s.seedLocal()
		}
		return s, err
	}
	if len(data) == 0 {
		return s, s.seedLocal()
	}

	var parsed fileSchema
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		_ = os.Rename(path, path+".corrupt")
		seedErr := s.seedLocal()
		wrapped := fmt.Errorf("profiles file at %s was unreadable (moved to %s.corrupt): %w", path, path, err)
		if seedErr != nil {
			return s, seedErr
		}
		return s, wrapped
	}

	for _, p := range parsed.Profiles {
		s.profiles[p.ID] = p
		s.order = append(s.order, p.ID)
	}
	s.lastUsed = parsed.LastUsed

	if len(s.profiles) == 0 {
		return s, s.seedLocal()
	}
	return s, nil
}

// seedLocal populates the store with a single enabled Local profile and
// persists it.
func (s *Store) seedLocal() error {
	p := Profile{
		ID:      LocalProfileID,
		Name:    DefaultLocalName,
		Type:    TypeLocal,
		Enabled: true,
	}
	s.profiles = map[string]Profile{p.ID: p}
	s.order = []string{p.ID}
	return s.save()
}

// List returns all profiles in stable (insertion) order.
func (s *Store) List() []Profile {
	list := make([]Profile, 0, len(s.order))
	for _, id := range s.order {
		list = append(list, s.profiles[id])
	}
	return list
}

// Get returns the profile with the given ID, and whether it exists.
func (s *Store) Get(id string) (Profile, bool) {
	p, ok := s.profiles[id]
	return p, ok
}

// GetByName returns the first profile (in stable insertion order) with the
// given display Name, and whether one was found. Used by the CLI's
// `--profile <name>` flags, which address profiles by their (renameable)
// display name rather than by their immutable ID — names are not enforced
// unique, so a collision (only possible via a hand-edited profiles.yaml)
// resolves to the earliest-created match.
func (s *Store) GetByName(name string) (Profile, bool) {
	for _, id := range s.order {
		if p := s.profiles[id]; p.Name == name {
			return p, true
		}
	}
	return Profile{}, false
}

// generateID returns a short, random, stable-unique token for a new profile.
// It is never derived from the profile's Name or connection target, both of
// which are editable after creation.
func generateID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Add creates a new profile, assigning it an immutable ID (LocalProfileID
// for a Local profile — there can only ever be one — or a fresh random token
// for a RemoteSSH profile), validates the resulting set, persists it, and
// returns the stored profile (with its assigned ID).
func (s *Store) Add(p Profile) (Profile, error) {
	if p.Type == TypeLocal {
		if _, exists := s.profiles[LocalProfileID]; exists {
			return Profile{}, errors.New("only one Local profile may exist")
		}
		p.ID = LocalProfileID
	} else {
		id, err := generateID()
		if err != nil {
			return Profile{}, err
		}
		p.ID = id
	}

	trial := s.cloneProfiles()
	trial[p.ID] = p
	if err := validate(trial); err != nil {
		return Profile{}, err
	}

	s.profiles[p.ID] = p
	s.order = append(s.order, p.ID)
	if err := s.save(); err != nil {
		return Profile{}, err
	}
	return p, nil
}

// Update replaces the profile with the same ID (ID and Type must not
// change), validates the resulting set, and persists it. Name may change
// freely; last-used tracking is by ID, so a rename does not lose it.
func (s *Store) Update(p Profile) (Profile, error) {
	existing, ok := s.profiles[p.ID]
	if !ok {
		return Profile{}, fmt.Errorf("no profile with id %q", p.ID)
	}
	if p.Type != existing.Type {
		return Profile{}, fmt.Errorf("profile %q: type is immutable", p.ID)
	}

	trial := s.cloneProfiles()
	trial[p.ID] = p
	if err := validate(trial); err != nil {
		return Profile{}, err
	}

	s.profiles[p.ID] = p
	if err := s.save(); err != nil {
		return Profile{}, err
	}
	return p, nil
}

// Remove deletes the profile with the given ID and persists the change. It
// refuses to remove the permanent Local profile.
func (s *Store) Remove(id string) error {
	if id == LocalProfileID {
		return errors.New("the local profile is permanent and cannot be removed")
	}
	if _, ok := s.profiles[id]; !ok {
		return fmt.Errorf("no profile with id %q", id)
	}
	delete(s.profiles, id)
	for i, oid := range s.order {
		if oid == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return s.save()
}

// Enable sets Enabled=true on the profile with the given ID and persists it.
func (s *Store) Enable(id string) error {
	return s.setEnabled(id, true)
}

// Disable sets Enabled=false on the profile with the given ID and persists
// it, without losing any of its other configuration.
func (s *Store) Disable(id string) error {
	return s.setEnabled(id, false)
}

func (s *Store) setEnabled(id string, enabled bool) error {
	p, ok := s.profiles[id]
	if !ok {
		return fmt.Errorf("no profile with id %q", id)
	}
	p.Enabled = enabled

	trial := s.cloneProfiles()
	trial[id] = p
	if err := validate(trial); err != nil {
		return err
	}

	s.profiles[id] = p
	return s.save()
}

// LastUsed returns the ID of the last-used profile, or "" if none has been
// set.
func (s *Store) LastUsed() string {
	return s.lastUsed
}

// SetLastUsed records id as the last-used profile (by ID, so a later rename
// of that profile does not lose the pointer) and persists it.
func (s *Store) SetLastUsed(id string) error {
	if _, ok := s.profiles[id]; !ok {
		return fmt.Errorf("no profile with id %q", id)
	}
	s.lastUsed = id
	return s.save()
}

func (s *Store) cloneProfiles() map[string]Profile {
	m := make(map[string]Profile, len(s.profiles))
	for id, p := range s.profiles {
		m[id] = p
	}
	return m
}

// validate checks the invariants that must hold across the whole profile
// set: at most one Local profile, and no two enabled RemoteSSH profiles
// resolving to the same "user@host:port" target.
func validate(profiles map[string]Profile) error {
	var localCount int
	seenTargets := map[string]string{} // target -> profile ID
	for _, p := range profiles {
		if p.Type == TypeLocal {
			localCount++
			if localCount > 1 {
				return errors.New("only one Local profile may exist")
			}
		}
		if p.Type == TypeRemoteSSH && p.Enabled {
			t := p.remoteTarget()
			if otherID, exists := seenTargets[t]; exists && otherID != p.ID {
				return fmt.Errorf("profile %q: target %q is already used by an enabled profile", p.ID, t)
			}
			seenTargets[t] = p.ID
		}
	}
	return nil
}

// save writes the store atomically (temp file + os.Rename) to its backing
// path. With an empty path it is a no-op, so an in-memory store never
// touches disk.
func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	schema := fileSchema{
		Version:  currentVersion,
		LastUsed: s.lastUsed,
		Profiles: s.List(),
	}
	data, err := yaml.Marshal(schema)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".profiles-*.yaml.tmp")
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
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, s.path)
}

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

	"github.com/lullabot/sandbar/internal/statelock"
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

// LoadReadOnly keeps the same parsed profiles and in-memory Local fallback
// without seeding a file or quarantining a corrupt one.
func LoadReadOnly() (*Store, error) {
	return loadFrom(defaultPath(), true)
}

// LoadFrom reads the store from an explicit path. A missing or empty file
// seeds a single enabled Local profile and persists it immediately, so an
// unconfigured sand behaves as today. A corrupt (unparseable) file is moved
// aside to "<path>.corrupt" (so a later save cannot silently clobber
// recoverable data), the error is returned, and the returned store is still
// seeded and usable — a mangled file never bricks startup.
//
// A non-ENOENT READ error (e.g. a permission error) is different: the file
// may be perfectly intact, just unreadable right now, so it is neither
// persisted over nor quarantined — doing either could destroy recoverable
// data. The returned store is still seeded with a usable, in-memory-only
// Local profile (never written to path) alongside the error, so a read
// failure degrades to "local-only, with a warning" rather than locking the
// user out of even purely-local VMs.
func LoadFrom(path string) (*Store, error) {
	return loadFrom(path, false)
}

func loadFrom(path string, readOnly bool) (*Store, error) {
	s := &Store{path: path, profiles: map[string]Profile{}}
	// A load that may WRITE (seed a missing file, quarantine a corrupt one)
	// holds the same lock as every other write, so it cannot seed over, or
	// move aside, a file another sand process wrote between this read and
	// that write. A read-only load writes nothing and takes no lock.
	if !readOnly {
		defer s.lock()()
	}
	seed := func() error {
		if readOnly {
			s.seedLocalInMemory()
			return nil
		}
		return s.seedLocal()
	}

	snap, err := readSnapshot(path)
	if err != nil {
		var perr *parseError
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return s, seed()
		case errors.As(err, &perr):
			if readOnly {
				s.seedLocalInMemory()
				return s, fmt.Errorf("profiles file at %s was unreadable: %w", path, perr.err)
			}
			_ = os.Rename(path, path+".corrupt")
			seedErr := s.seedLocal()
			wrapped := fmt.Errorf("profiles file at %s was unreadable (moved to %s.corrupt): %w", path, path, perr.err)
			if seedErr != nil {
				return s, seedErr
			}
			return s, wrapped
		default:
			s.seedLocalInMemory()
			return s, fmt.Errorf("profiles file at %s could not be read: %w", path, err)
		}
	}
	if len(snap.profiles) == 0 {
		return s, seed()
	}
	s.adopt(snap)
	return s, nil
}

// snapshot is one parsed read of the profiles file.
type snapshot struct {
	profiles map[string]Profile
	order    []string
	lastUsed string
}

// parseError marks a file that was read but could not be parsed — the one
// case LoadFrom quarantines, as distinct from a file it could not read.
type parseError struct{ err error }

func (e *parseError) Error() string { return e.err.Error() }

// readSnapshot reads and parses the file at path. A missing file returns an
// error satisfying errors.Is(err, fs.ErrNotExist); an empty file returns an
// empty snapshot; an unparseable one returns a *parseError.
func readSnapshot(path string) (snapshot, error) {
	snap := snapshot{profiles: map[string]Profile{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return snap, err
	}
	if len(data) == 0 {
		return snap, nil
	}
	var parsed fileSchema
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return snap, &parseError{err: err}
	}
	for _, p := range parsed.Profiles {
		if p.Type == TypeRemoteSSH && p.Port <= 0 {
			// Canonicalize a hand-edited profile's missing/zero port to the SSH
			// default (finding 8): resolveTargetConfig's retired defaultRemotePort
			// was always 22, so a scope/remoteTarget derived here must agree with
			// one derived from an explicit `port: 22` — never diverge as
			// "host:0" vs "host:22".
			p.Port = 22
		}
		snap.profiles[p.ID] = p
		snap.order = append(snap.order, p.ID)
	}
	snap.lastUsed = parsed.LastUsed
	return snap, nil
}

// adopt replaces the store's in-memory state with snap.
func (s *Store) adopt(snap snapshot) {
	s.profiles = snap.profiles
	s.order = snap.order
	s.lastUsed = snap.lastUsed
}

// lock takes the advisory lock beside the store's file (statelock) and
// returns its release. The directory is created first so the lock file can
// be, on a first run where nothing has been written yet. An in-memory store
// (empty path) has nothing to serialize.
func (s *Store) lock() (release func()) {
	if s.path == "" {
		return func() {}
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
	return statelock.Acquire(s.path)
}

// mutate is the ONLY write path after load. The file is rewritten whole from
// memory, and a long-lived holder (the TUI) and a short one (`sandbar
// create`'s SetLastUsed) each loaded it at their own startup — so saving
// in-memory state as-is lets one silently erase the other's change: a profile
// added in the TUI while a create runs, or the create's last-used pointer.
// mutate therefore takes the file lock, re-reads the file, applies the change
// to what is on disk NOW, and saves. apply must make its own existence and
// validation checks against that fresh state, and must return an error
// before changing anything to abort the write.
//
// A failed or empty re-read keeps this process's in-memory state as the best
// available source, matching internal/registry's mutate: the file may be
// briefly unreadable, and an empty one is never a reason to drop the
// permanent Local profile.
func (s *Store) mutate(apply func() error) error {
	if s.path == "" {
		return apply()
	}
	defer s.lock()()
	if snap, err := readSnapshot(s.path); err == nil && len(snap.profiles) > 0 {
		s.adopt(snap)
	}
	if err := apply(); err != nil {
		return err
	}
	return s.save()
}

// seedLocalInMemory populates the store with a single enabled Local profile
// WITHOUT persisting it — used when the backing file could not be read at
// all (LoadFrom's non-ENOENT branch), where writing anything to path risks
// clobbering a file that may be intact but merely unreadable right now.
func (s *Store) seedLocalInMemory() {
	p := Profile{
		ID:      LocalProfileID,
		Name:    DefaultLocalName,
		Type:    TypeLocal,
		Enabled: true,
	}
	s.profiles = map[string]Profile{p.ID: p}
	s.order = []string{p.ID}
}

// seedLocal populates the store with a single enabled Local profile and
// persists it. Only loadFrom calls it, with the file lock already held.
func (s *Store) seedLocal() error {
	s.seedLocalInMemory()
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
	if p.Type == TypeRemoteSSH && p.Port <= 0 {
		p.Port = 22 // finding 8: canonicalize before validate() so the scope key is stable
	}
	if p.Type != TypeLocal {
		id, err := generateID()
		if err != nil {
			return Profile{}, err
		}
		p.ID = id
	}

	err := s.mutate(func() error {
		if p.Type == TypeLocal {
			if _, exists := s.profiles[LocalProfileID]; exists {
				return errors.New("only one Local profile may exist")
			}
			p.ID = LocalProfileID
		}

		trial := s.cloneProfiles()
		trial[p.ID] = p
		if err := validate(trial); err != nil {
			return err
		}

		s.profiles[p.ID] = p
		s.order = append(s.order, p.ID)
		return nil
	})
	if err != nil {
		return Profile{}, err
	}
	return p, nil
}

// Update replaces the profile with the same ID (ID and Type must not
// change), validates the resulting set, and persists it. Name may change
// freely; last-used tracking is by ID, so a rename does not lose it.
func (s *Store) Update(p Profile) (Profile, error) {
	if p.Type == TypeRemoteSSH && p.Port <= 0 {
		p.Port = 22 // finding 8: canonicalize before validate() so the scope key is stable
	}
	err := s.mutate(func() error {
		existing, ok := s.profiles[p.ID]
		if !ok {
			return fmt.Errorf("no profile with id %q", p.ID)
		}
		if p.Type != existing.Type {
			return fmt.Errorf("profile %q: type is immutable", p.ID)
		}

		trial := s.cloneProfiles()
		trial[p.ID] = p
		if err := validate(trial); err != nil {
			return err
		}

		s.profiles[p.ID] = p
		return nil
	})
	if err != nil {
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
	return s.mutate(func() error {
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
		return nil
	})
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
	return s.mutate(func() error {
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
		return nil
	})
}

// LastUsed returns the ID of the last-used profile, or "" if none has been
// set.
func (s *Store) LastUsed() string {
	return s.lastUsed
}

// SetLastUsed records id as the last-used profile (by ID, so a later rename
// of that profile does not lose the pointer) and persists it.
func (s *Store) SetLastUsed(id string) error {
	return s.mutate(func() error {
		if _, ok := s.profiles[id]; !ok {
			return fmt.Errorf("no profile with id %q", id)
		}
		s.lastUsed = id
		return nil
	})
}

func (s *Store) cloneProfiles() map[string]Profile {
	m := make(map[string]Profile, len(s.profiles))
	for id, p := range s.profiles {
		m[id] = p
	}
	return m
}

// validate checks the invariants that must hold across the whole profile
// set: every profile has a recognised Type (finding 3 — an unrecognised
// Type, e.g. a hand-edited "remote_ssh" typo, must be a hard error here
// rather than silently falling through to LOCAL behaviour elsewhere), a
// RemoteSSH profile has a non-empty Host (finding 9 — an empty host produces
// a cryptic `ssh user@` failure far from here otherwise), a Proxmox profile
// has non-empty Host, Node, Pool, and TokenFile, at most one Local profile,
// and no two enabled RemoteSSH profiles resolving to the same
// "user@host:port" target, nor two enabled Proxmox profiles resolving to the
// same "host:node/pool" target.
//
// LoadFrom deliberately does NOT call validate — a bad hand-edited entry
// must not lock the user out of the rest of the file; it is the store's
// write path (Add/Update, here) and the provider layer (BuildFleet,
// providerForProfile) that must catch and surface it.
func validate(profiles map[string]Profile) error {
	var localCount int
	seenTargets := map[string]string{} // target -> profile ID
	for _, p := range profiles {
		switch p.Type {
		case TypeLocal:
			localCount++
			if localCount > 1 {
				return errors.New("only one Local profile may exist")
			}
		case TypeRemoteSSH:
			if p.Host == "" {
				return fmt.Errorf("profile %q: remote-ssh profile requires a host", p.ID)
			}
		case TypeProxmox:
			if p.Host == "" {
				return fmt.Errorf("profile %q: proxmox profile requires a host", p.ID)
			}
			if p.Node == "" {
				return fmt.Errorf("profile %q: proxmox profile requires a node", p.ID)
			}
			if p.Pool == "" {
				return fmt.Errorf("profile %q: proxmox profile requires a pool", p.ID)
			}
			if p.TokenFile == "" {
				return fmt.Errorf("profile %q: proxmox profile requires a token_file", p.ID)
			}
		default:
			return fmt.Errorf("profile %q: unknown profile type %q", p.ID, p.Type)
		}
		if p.Type == TypeRemoteSSH && p.Enabled {
			t := p.remoteTarget()
			if otherID, exists := seenTargets[t]; exists && otherID != p.ID {
				return fmt.Errorf("profile %q: target %q is already used by an enabled profile", p.ID, t)
			}
			seenTargets[t] = p.ID
		}
		if p.Type == TypeProxmox && p.Enabled {
			t := p.proxmoxTarget()
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
// touches disk. It is called only by mutate and seedLocal, which hold the
// file lock; saving from anywhere else reopens the lost-update race.
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

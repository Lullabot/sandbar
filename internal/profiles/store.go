package profiles

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/lullabot/sandbar/internal/filelock"
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
//
// Every mutation (Add, Update, Remove, Enable, Disable, SetLastUsed) is a
// lock-protected read-modify-write against the CURRENT on-disk file: it
// takes the cross-process lock, reloads the file fresh, applies only that
// operation's own narrow delta onto the reloaded set, validates the merged
// result, and persists it — see mutateLocked. This is what lets two sand
// processes sharing a profiles file each commit their own change without
// silently discarding the other's (the lost-update bug a blind read-then-
// save would otherwise have), including the order slice and lastUsed scalar,
// neither of which map-unions on its own (see profileSet and mutateLocked).
type Store struct {
	path     string
	profiles map[string]Profile
	order    []string // insertion order, for stable List() output
	lastUsed string
}

// profileSet is the in-memory value of one reload/mutate cycle: the profiles
// map, insertion order, and lastUsed scalar travel together because a locked
// read-modify-write must merge all three against the CURRENT on-disk state,
// not just the map — order and lastUsed do not map-union (see
// mutateLocked).
type profileSet struct {
	profiles map[string]Profile
	order    []string
	lastUsed string
}

// list returns the set's profiles in stable (insertion) order — the same
// shape save() persists and Store.List() returns.
func (set profileSet) list() []Profile {
	list := make([]Profile, 0, len(set.order))
	for _, id := range set.order {
		list = append(list, set.profiles[id])
	}
	return list
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
//
// The actual decode (+ RemoteSSH port canonicalization) is done by the pure,
// side-effect-free parseSet, which this process-start path and the locked
// reload (reloadUnlocked) both share so file-format handling lives in
// exactly one place. LoadFrom layers the Load()-only side effects around it:
// seeding an empty/missing file and quarantining a corrupt one. The locked
// reload does neither — see reloadUnlocked.
func LoadFrom(path string) (*Store, error) {
	s := &Store{path: path, profiles: map[string]Profile{}}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s, s.seedLocal()
		}
		s.seedLocalInMemory()
		return s, fmt.Errorf("profiles file at %s could not be read: %w", path, err)
	}

	set, err := parseSet(data)
	if err != nil {
		_ = os.Rename(path, path+".corrupt")
		seedErr := s.seedLocal()
		wrapped := fmt.Errorf("profiles file at %s was unreadable (moved to %s.corrupt): %w", path, path, err)
		if seedErr != nil {
			return s, seedErr
		}
		return s, wrapped
	}
	s.profiles = set.profiles
	s.order = set.order
	s.lastUsed = set.lastUsed

	if len(s.profiles) == 0 {
		return s, s.seedLocal()
	}
	return s, nil
}

// parseSet decodes the on-disk YAML bytes into a profileSet, canonicalizing
// each RemoteSSH profile's missing/zero port to 22 (finding 8:
// resolveTargetConfig's retired defaultRemotePort was always 22, so a
// scope/remoteTarget derived here must agree with one derived from an
// explicit `port: 22` — never diverge as "host:0" vs "host:22"). It is the
// single place the on-disk shape is interpreted, shared by both the
// process-start Load()/LoadFrom() path and the locked reload
// (reloadUnlocked), so the two can never drift.
//
// It is PURE: no file I/O, no save(), no lock, no .corrupt rename, no
// seeding. Empty input yields an empty set and is not an error (a missing or
// empty file has no profiles). LoadFrom and reloadUnlocked each decide, on
// their own side, whether an empty result warrants seeding — parseSet itself
// never seeds, which is what keeps a locked read-modify-write's reload from
// racing another process's seed-on-empty at process start.
func parseSet(data []byte) (profileSet, error) {
	out := profileSet{profiles: map[string]Profile{}}
	if len(data) == 0 {
		return out, nil
	}
	var parsed fileSchema
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return profileSet{}, fmt.Errorf("decode profiles file: %w", err)
	}
	for _, p := range parsed.Profiles {
		if p.Type == TypeRemoteSSH && p.Port <= 0 {
			p.Port = 22
		}
		out.profiles[p.ID] = p
		out.order = append(out.order, p.ID)
	}
	out.lastUsed = parsed.LastUsed
	return out, nil
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
// persists it.
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
// for a RemoteSSH profile), and performs a lock-protected read-modify-write
// (see mutateLocked) that reloads the CURRENT on-disk set, appends the new
// profile to the RELOADED order (never this process's own possibly-stale
// order — a concurrent process may already have appended a profile of its
// own), re-validates the merged set, persists it, and returns the stored
// profile (with its assigned ID). The "only one Local profile" invariant is
// enforced by validate() against the reloaded set, not a pre-check against
// this process's stale view, so it holds even under concurrent Add calls.
func (s *Store) Add(p Profile) (Profile, error) {
	if p.Type == TypeRemoteSSH && p.Port <= 0 {
		p.Port = 22 // finding 8: canonicalize before validate() so the scope key is stable
	}
	if p.Type == TypeLocal {
		p.ID = LocalProfileID
	} else {
		id, err := generateID()
		if err != nil {
			return Profile{}, err
		}
		p.ID = id
	}

	err := s.mutateLocked(func(cur *profileSet) error {
		if _, exists := cur.profiles[p.ID]; exists {
			// Add creates a NEW profile; unlike Update it must not silently
			// overwrite one that already exists under this id in the reloaded
			// set. This matters specifically for TypeLocal, whose id is the
			// fixed LocalProfileID: without this check, a second Add(Local)
			// would overwrite the existing entry under the same key, and
			// validate() below — which counts distinct map entries — would
			// never see two Local profiles to reject.
			return fmt.Errorf("profile with id %q already exists", p.ID)
		}
		cur.order = append(cur.order, p.ID)
		cur.profiles[p.ID] = p
		return validate(cur.profiles)
	})
	if err != nil {
		return Profile{}, err
	}
	return p, nil
}

// Update replaces the profile with the same ID (ID and Type must not
// change) via a lock-protected read-modify-write: it reloads the CURRENT
// on-disk set, checks existence/type-immutability and re-validates against
// THAT reloaded set (not this process's stale view), and persists the
// merged result. Name may change freely; last-used tracking is by ID, so a
// rename does not lose it.
func (s *Store) Update(p Profile) (Profile, error) {
	if p.Type == TypeRemoteSSH && p.Port <= 0 {
		p.Port = 22 // finding 8: canonicalize before validate() so the scope key is stable
	}

	err := s.mutateLocked(func(cur *profileSet) error {
		existing, ok := cur.profiles[p.ID]
		if !ok {
			return fmt.Errorf("no profile with id %q", p.ID)
		}
		if p.Type != existing.Type {
			return fmt.Errorf("profile %q: type is immutable", p.ID)
		}
		cur.profiles[p.ID] = p
		return validate(cur.profiles)
	})
	if err != nil {
		return Profile{}, err
	}
	return p, nil
}

// Remove deletes the profile with the given ID via a lock-protected
// read-modify-write: it reloads the CURRENT on-disk set and deletes exactly
// this one id from the reloaded map AND order, leaving every other entry
// (including one a concurrent process just added) intact. It refuses to
// remove the permanent Local profile.
func (s *Store) Remove(id string) error {
	if id == LocalProfileID {
		return errors.New("the local profile is permanent and cannot be removed")
	}
	return s.mutateLocked(func(cur *profileSet) error {
		if _, ok := cur.profiles[id]; !ok {
			return fmt.Errorf("no profile with id %q", id)
		}
		delete(cur.profiles, id)
		for i, oid := range cur.order {
			if oid == id {
				cur.order = append(cur.order[:i], cur.order[i+1:]...)
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

// setEnabled toggles Enabled on the profile with the given ID via a
// lock-protected read-modify-write: it reloads the CURRENT on-disk set,
// mutates only this one profile's Enabled flag, and re-validates the merged
// set (which catches, e.g., enabling a profile that would collide with
// another enabled profile's target — including one a concurrent process
// just added).
func (s *Store) setEnabled(id string, enabled bool) error {
	return s.mutateLocked(func(cur *profileSet) error {
		p, ok := cur.profiles[id]
		if !ok {
			return fmt.Errorf("no profile with id %q", id)
		}
		p.Enabled = enabled
		cur.profiles[id] = p
		return validate(cur.profiles)
	})
}

// LastUsed returns the ID of the last-used profile, or "" if none has been
// set.
func (s *Store) LastUsed() string {
	return s.lastUsed
}

// SetLastUsed records id as the last-used profile (by ID, so a later rename
// of that profile does not lose the pointer) via a lock-protected
// read-modify-write: it reloads the CURRENT on-disk set and updates ONLY the
// lastUsed scalar on it (last-writer-wins), leaving whatever a concurrent
// profile edit did to the reloaded profiles map/order untouched — the two
// narrow deltas do not collide because each mutation only ever touches its
// own field.
func (s *Store) SetLastUsed(id string) error {
	return s.mutateLocked(func(cur *profileSet) error {
		if _, ok := cur.profiles[id]; !ok {
			return fmt.Errorf("no profile with id %q", id)
		}
		cur.lastUsed = id
		return nil
	})
}

// validate checks the invariants that must hold across the whole profile
// set: every profile has a recognised Type (finding 3 — an unrecognised
// Type, e.g. a hand-edited "remote_ssh" typo, must be a hard error here
// rather than silently falling through to LOCAL behaviour elsewhere), a
// RemoteSSH profile has a non-empty Host (finding 9 — an empty host produces
// a cryptic `ssh user@` failure far from here otherwise), at most one Local
// profile, and no two enabled RemoteSSH profiles resolving to the same
// "user@host:port" target.
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
	}
	return nil
}

// reloadUnlocked re-reads the on-disk store fresh and decodes it in memory
// via the pure parseSet, returning the CURRENT profile set. It is the read
// half of every locked read-modify-write (see mutateLocked). It does NOT
// take the lock or seed — the caller already holds the lock (taken once at
// the mutation boundary), so re-acquiring here would self-deadlock on a
// fresh flock fd, and seeding here would persist a seeded Local profile onto
// what may simply be a not-yet-created file mid-mutation (seeding is
// reserved for the process-start Load()/LoadFrom() path). A missing or empty
// file yields an empty set — the caller's mutation then creates the file
// via the normal atomic write. A decode failure is surfaced so the caller
// ABORTS the mutation rather than clobbering an unreadable on-disk file.
func (s *Store) reloadUnlocked() (profileSet, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return profileSet{profiles: map[string]Profile{}}, nil
		}
		return profileSet{}, err
	}
	return parseSet(data)
}

// mutateLocked performs one lock-protected read-modify-write against the
// on-disk profile set. It takes the cross-process lock (best-effort: a lock
// it cannot take only WARNS and proceeds unserialized — a wedged or
// unwritable lock file must never fail an otherwise-valid write), reloads
// the CURRENT on-disk set via reloadUnlocked, hands it to apply so the
// caller can layer ONLY this operation's own delta onto it (see Add, Update,
// Remove, setEnabled, SetLastUsed), and — when apply succeeds — persists the
// merged result via the existing atomic temp+rename body (saveSet). On
// success it refreshes the in-memory working copy from the merged set, so
// later reads see exactly what hit disk (including entries a concurrent
// process added). On failure (apply rejects the mutation, or save fails)
// the in-memory store is left untouched.
//
// The lock is taken EXACTLY ONCE, here; neither reloadUnlocked nor saveSet
// re-acquires it, so there is no re-entrant self-deadlock (a fresh flock fd
// on the same path is a distinct open file description and would otherwise
// block).
func (s *Store) mutateLocked(apply func(cur *profileSet) error) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.warnf("could not create config dir for %s (%v); proceeding", s.path, err)
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
	if err := apply(&cur); err != nil {
		return err
	}
	if err := s.saveSet(cur); err != nil {
		return err
	}
	s.profiles = cur.profiles
	s.order = cur.order
	s.lastUsed = cur.lastUsed
	return nil
}

// warnf emits a best-effort operational note about a DEGRADED write — today
// only a failure to take the cross-process lock, after which the mutation
// proceeds unserialized. It never affects control flow and never fails a
// mutation. The note goes to stderr, so a user can tell a write was not
// serialized against concurrent sand processes; this mirrors the registry
// store's own warning channel (internal/registry/registry.go).
func (s *Store) warnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "warning: profiles store: "+format+"\n", args...)
}

// save persists the current in-memory set. It is used ONLY on the
// process-start seeding paths (seedLocal, and LoadFrom's corrupt-file
// reseed), which run before any concurrent mutation and are deliberately not
// lock-protected — see LoadFrom's doc comment. Every concurrent mutation
// writes through mutateLocked -> saveSet instead, under the lock, from the
// freshly-reloaded set — never from this long-lived in-memory snapshot.
func (s *Store) save() error {
	return s.saveSet(profileSet{profiles: s.profiles, order: s.order, lastUsed: s.lastUsed})
}

// saveSet atomically writes set to the backing file (temp file + os.Rename)
// at 0644. With an empty path it is a no-op, so an in-memory store never
// touches disk.
//
// Writes are now lock-protected read-modify-writes: every mutation reloads
// the current on-disk set under the cross-process lock (see mutateLocked)
// and calls this with the MERGED set, so two sand processes sharing a
// profiles file can no longer silently discard each other's committed
// changes.
func (s *Store) saveSet(set profileSet) error {
	if s.path == "" {
		return nil
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	schema := fileSchema{
		Version:  currentVersion,
		LastUsed: set.lastUsed,
		Profiles: set.list(),
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

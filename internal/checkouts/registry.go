// Package checkouts is the host-persisted "what git work lives inside this
// VM" spine for the land feature: a per-VM registry of every git checkout
// (and worktree) a slow-cadence guest sweep discovers, recording each one's
// branch, forge, push state, and dirty state. It is the single source of
// truth every land consumer reads: the unlanded-work tile badge, the
// zero-guest-contact delete guard, the Landing pane, and the headless
// `sand land` CLI.
//
// This package is a pure data layer. It knows nothing about Bubble Tea, the
// TUI model, or how a sweep talks to a guest (limactl shell, git plumbing) —
// it only stores and serves rows a caller hands it. That separation is
// deliberate: the concurrency contract below, and the on-disk format, must
// be stable and testable in isolation from the sweep that populates it and
// the TUI that reads it.
//
// # Connection scoping
//
// Like internal/registry and internal/secrets, every VM is identified by a
// (registry.Scope, name) pair, not a bare name: a VM called "web" on one
// connection profile and a same-named "web" on another (or on a remote host)
// must never collide or overwrite each other's checkout rows. This package
// reuses registry.Scope directly — the exact type internal/ui's vmHandle and
// internal/secrets' connection-scoped store already key on — rather than
// inventing a parallel identity.
//
// # The concurrency contract
//
// This mirrors the heartbeat's sample-state pattern (internal/ui/heartbeat.go):
// Bubble Tea passes its model BY VALUE, so any mutable state that must
// outlive one Update cannot live directly on it. A *Registry is therefore a
// POINTER a model holds (by field), guarding all mutation with a mutex, so
// every model copy shares the one registry. Set is the only path that writes
// (called from a single goroutine/message path — the sweep result handler);
// Get always hands back a value copy — a fresh slice, not an alias into the
// stored one — so a reader (the tile renderer, the delete guard) can never
// mutate shared state by accident, and never needs its own locking.
//
// # Persistence
//
// A single JSON file, ${XDG_DATA_HOME:-$HOME/.local/share}/sandbar/
// checkout-registry.json, sibling to managed-vms.json and secrets.json,
// written mode 0600 via atomic rewrite (unique temp file in the same
// directory, then os.Rename) — the same shape registry.go and secrets.go
// use. Load-on-start tolerates a missing, empty, or corrupt file: it never
// panics and always returns a usable, non-nil, empty registry. A corrupt
// file is moved aside to "<path>.corrupt" (mirroring registry.go/secrets.go)
// so a later save cannot silently clobber bytes a human might still want to
// recover, and the error is returned for the caller to surface.
package checkouts

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/lullabot/sandbar/internal/registry"
)

// Kind distinguishes a checkout that is a repository's own working tree from
// one that is a linked worktree (a `.git` FILE pointing at a parent repo's
// `.git` dir, per `git worktree add`) of another checkout in the registry.
type Kind string

const (
	// KindRepo is an ordinary repository checkout: `.git` is a directory.
	KindRepo Kind = "repo"

	// KindWorktree is a linked worktree: `.git` is a file, and Parent records
	// the path of the repo checkout it was created from.
	KindWorktree Kind = "worktree"
)

// PushState summarizes whether a checkout's current branch has reached
// GitHub, derived from the local remote-tracking ref
// (refs/remotes/<remote>/<branch>) rather than the configured upstream — a
// push without `-u` updates the former but never sets the latter. See the
// plan's push-state note: this is a cheap local heuristic; a host `gh pr
// list --head` check at Landing-pane-open is authoritative and corrects a
// stale ref.
type PushState string

const (
	// PushStatePushed means the local branch has a remote-tracking ref: some
	// version of it has reached the forge. Ahead/Behind then say how the
	// local branch relates to that ref.
	PushStatePushed PushState = "pushed"

	// PushStateUnpushed means the branch has commits that are not (yet, or
	// not any longer) reflected by any remote-tracking ref — including a
	// branch that once had one but was force-pushed over, or rebased locally
	// past it.
	PushStateUnpushed PushState = "unpushed"

	// PushStateNever means the branch has no remote-tracking ref at all: it
	// has never been pushed.
	PushStateNever PushState = "never"
)

// Checkout is one row: a single git checkout or worktree discovered under a
// VM's guest home, and everything the land feature needs to know about it
// without touching the guest again.
type Checkout struct {
	// Path is the checkout's absolute path inside the guest.
	Path string

	// Kind distinguishes an ordinary repo checkout from a linked worktree.
	Kind Kind

	// Parent is the parent repo's Path when Kind is KindWorktree, and "" for
	// a KindRepo row — a linked worktree is grouped under the checkout it was
	// created from.
	Parent string

	// Branch is the checked-out branch name (empty for a detached HEAD).
	Branch string

	// Forge is the remote's host, e.g. "github.com" or "gitlab.com" — empty
	// if the checkout has no remote configured.
	Forge string

	// OrgRepo is the remote's "org/repo" slug, empty if there is no remote.
	OrgRepo string

	// PushState summarizes whether Branch has reached the forge. See
	// PushState's doc.
	PushState PushState

	// Ahead is how many local commits on Branch are not in its
	// remote-tracking ref (0 if PushState is PushStateNever).
	Ahead int

	// Behind is how many commits the remote-tracking ref has that the local
	// branch does not (0 if PushState is PushStateNever).
	Behind int

	// Dirty is the count of uncommitted changes (tracked modifications plus
	// untracked files) `git status --porcelain` reports for the checkout.
	Dirty int

	// DefaultBranch is the remote's own default branch, read from the
	// checkout's `refs/remotes/<remote>/HEAD` — "main" for most clones. It is
	// empty when there is no remote, or when that ref is absent (a clone made
	// with `--no-checkout`, or one whose origin/HEAD was never set); see
	// NothingToLand for what that absence falls back to.
	DefaultBranch string

	// LastSeen is when the sweep last observed this checkout.
	LastSeen time.Time
}

// fallbackDefaultBranches are the branch names treated as a repo's trunk when
// the sweep could not read an authoritative `refs/remotes/<remote>/HEAD`. It
// exists only so a clone with no origin/HEAD still suppresses the badge; a
// repo whose real default branch is something else entirely just falls back to
// the pre-existing behaviour of showing it, which is the safe direction to
// err (a spurious "you have work here" beats silently hiding real work).
var fallbackDefaultBranches = map[string]bool{"main": true, "master": true}

// NothingToLand reports whether this checkout holds no work worth landing: it
// sits on the repo's default branch with nothing of its own to turn into a PR.
//
// This is the discriminator the raw PushState cannot make on its own.
// PushStatePushed means only "HEAD is reachable on the forge", which is
// trivially true of a PRISTINE CLONE — a fresh `git clone` puts HEAD exactly
// at origin/main with a tracking ref, so it classified as "pushed" and lit the
// amber "⚠ actionable" badge on a VM where nobody had done any work at all.
// The pane, reading the same state, offered to open a draft PR for main — a
// request GitHub would reject outright, since head and base would be the same
// branch.
//
// Being level with upstream is NOT on its own enough to say "nothing to land":
// a feature branch that has been fully pushed is level with its tracking ref
// too, and that is precisely the case the whole land feature exists to serve.
// The distinction is the BRANCH, not the commit count — so a checkout is only
// dismissed here when it is on the default branch. Someone who commits and
// pushes directly to main also lands here, correctly: there is no PR to open
// for work that is already on the trunk.
//
// A dirty or unpushed default-branch checkout is deliberately NOT covered:
// that work exists nowhere but the VM, so the at-risk half of the badge (and
// the delete guard) must still see it. This answers only the actionable
// half — "is there something here to turn into a PR".
func (c Checkout) NothingToLand() bool {
	if c.PushState != PushStatePushed || c.Branch == "" {
		return false
	}
	if c.DefaultBranch != "" {
		return c.Branch == c.DefaultBranch
	}
	return fallbackDefaultBranches[c.Branch]
}

// VMCheckouts is one VM's full set of discovered checkouts, as recorded by
// its most recent sweep.
type VMCheckouts struct {
	// Checkouts is every git checkout/worktree the sweep found, up to its cap.
	Checkouts []Checkout

	// Truncated is true when the sweep hit a cap (depth, checkout count, or a
	// per-repo timeout) and stopped before it could be exhaustive — so a
	// consumer (the badge, the delete guard) can flag that the picture may be
	// incomplete rather than silently presenting a partial list as the whole
	// truth.
	Truncated bool

	// SweptAt is when this VM's sweep that produced Checkouts ran.
	SweptAt time.Time
}

// vmHandle is a VM's full identity for this registry's purposes: which
// connection scope it lives under, plus its name. It mirrors
// internal/ui's vmHandle exactly (registry.Scope + Name) — the same
// composite key every other per-VM in-memory store in sand uses — so a VM
// named "web" on one connection profile and a same-named "web" on another
// can never collide here either. Deliberately unexported and redefined here
// (rather than imported from internal/ui) to keep this package free of any
// dependency on the TUI package, which is itself the point of this package
// being a pure data layer.
type vmHandle struct {
	scope registry.Scope
	name  string
}

// Registry is the pointer-held, mutex-guarded per-VM checkout store. A model
// holds a *Registry by field; every copy of that model shares the same
// registry, mirroring the heartbeat's concurrency contract (see the package
// doc). The zero value is not useful — construct with NewEmpty, Load, or
// LoadFrom.
type Registry struct {
	mu   sync.Mutex
	path string // "" disables persistence (used by NewEmpty and tests)
	vms  map[vmHandle]VMCheckouts
}

// NewEmpty returns an in-memory registry with no backing file: Set never
// touches disk, which is what every test — and any caller that wants a
// scratch registry — uses instead of pointing LoadFrom at a real path.
func NewEmpty() *Registry {
	return &Registry{vms: map[vmHandle]VMCheckouts{}}
}

// defaultPath mirrors internal/registry's and internal/secrets' XDG
// derivation: ${XDG_DATA_HOME:-$HOME/.local/share}/sandbar/checkout-registry.json.
func defaultPath() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			home = "."
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "sandbar", "checkout-registry.json")
}

// Load reads the registry from the default path.
func Load() (*Registry, error) {
	return LoadFrom(defaultPath())
}

// currentVersion is the schema version this build writes. There is no
// migration to perform yet (this is the format's first version), but the
// field is carried from day one — mirroring registry.go and secrets.go — so
// a later format change has a version to branch on instead of needing to
// invent one retroactively.
const currentVersion = 1

// diskEntry is one VM's on-disk record: a JSON array element that
// self-describes its own connection scope and name (an array, rather than a
// map keyed by some serialized scope+name string, keeps Provider/RemoteTarget
// individually readable — the same reasoning as registry.go's diskEntry and
// secrets.go's scopeGroup).
type diskEntry struct {
	Provider     string     `json:"provider"`
	RemoteTarget string     `json:"remote_target,omitempty"`
	Name         string     `json:"name"`
	Checkouts    []Checkout `json:"checkouts"`
	Truncated    bool       `json:"truncated"`
	SweptAt      time.Time  `json:"swept_at"`
}

// fileSchema is the on-disk JSON shape:
// {"version":1,"vms":[{"provider":"...","name":"...","checkouts":[...],...}]}.
type fileSchema struct {
	Version int         `json:"version"`
	VMs     []diskEntry `json:"vms"`
}

// versionProbe reads just the version field, mirroring registry.go/secrets.go:
// it lets LoadFrom reject a file from a future, not-yet-understood schema
// before attempting a full decode.
type versionProbe struct {
	Version int `json:"version"`
}

// LoadFrom reads the registry from an explicit path. A missing or empty file
// yields an empty, usable registry — not an error. A corrupt file (bytes that
// don't even parse as JSON) is moved aside to "<path>.corrupt", so a later
// Set cannot silently clobber recoverable bytes, and the error is returned
// for the caller to surface; the returned registry is always non-nil and
// usable either way. A file stamped with a schema version newer than this
// build understands is refused (empty registry, error returned) rather than
// misparsed and silently narrowed.
func LoadFrom(path string) (*Registry, error) {
	r := &Registry{path: path, vms: map[vmHandle]VMCheckouts{}}
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
		return r, fmt.Errorf("checkout registry at %s was unreadable (moved to %s.corrupt): %w", path, path, err)
	}
	if probe.Version > currentVersion {
		return r, fmt.Errorf(
			"checkout registry %s has schema version %d, but this sand only understands %d; upgrade sand",
			path, probe.Version, currentVersion)
	}

	var parsed fileSchema
	if err := json.Unmarshal(data, &parsed); err != nil {
		_ = os.Rename(path, path+".corrupt")
		return r, fmt.Errorf("checkout registry at %s was unreadable (moved to %s.corrupt): %w", path, path, err)
	}
	for _, de := range parsed.VMs {
		scope := registry.Scope{Provider: de.Provider, RemoteTarget: de.RemoteTarget}
		r.vms[vmHandle{scope: scope, name: de.Name}] = VMCheckouts{
			Checkouts: cloneCheckouts(de.Checkouts),
			Truncated: de.Truncated,
			SweptAt:   de.SweptAt,
		}
	}
	return r, nil
}

// cloneCheckouts returns a fresh copy of in's backing slice. Checkout has no
// slice/map/pointer fields of its own, so copying the slice header and
// backing array IS a deep copy — there is nothing nested left for a caller
// to alias into.
func cloneCheckouts(in []Checkout) []Checkout {
	if in == nil {
		return nil
	}
	out := make([]Checkout, len(in))
	copy(out, in)
	return out
}

// Get returns a deep copy of scope's vm's most recently recorded checkouts,
// and whether an entry exists at all. The copy means a caller can freely
// mutate the returned VMCheckouts (append to its Checkouts, edit a row) with
// no effect on the registry's own state and no data race with a concurrent
// Set — the whole point of the by-value-read half of the concurrency
// contract (see the package doc).
func (r *Registry) Get(scope registry.Scope, vm string) (VMCheckouts, bool) {
	if r == nil {
		return VMCheckouts{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	vc, ok := r.vms[vmHandle{scope: scope, name: vm}]
	if !ok {
		return VMCheckouts{}, false
	}
	return VMCheckouts{
		Checkouts: cloneCheckouts(vc.Checkouts),
		Truncated: vc.Truncated,
		SweptAt:   vc.SweptAt,
	}, true
}

// Set records c as scope's vm's checkouts and persists the change. c is
// deep-copied into the store before the lock is released, so a caller that
// goes on to mutate the VMCheckouts (or its Checkouts slice) it just passed
// in cannot reach back into the registry's own state — the by-value-write
// half of the concurrency contract mirrors Get's by-value-read half.
func (r *Registry) Set(scope registry.Scope, vm string, c VMCheckouts) error {
	if r == nil {
		return errors.New("checkouts: Set called on a nil Registry")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.vms[vmHandle{scope: scope, name: vm}] = VMCheckouts{
		Checkouts: cloneCheckouts(c.Checkouts),
		Truncated: c.Truncated,
		SweptAt:   c.SweptAt,
	}
	return r.save()
}

// save writes the registry atomically (unique temp file + rename), mirroring
// registry.go/secrets.go. It must be called with r.mu already held — every
// call site above takes the lock first. An empty path is a no-op, so
// NewEmpty's in-memory registry never touches disk. Entries are written in a
// stable (Provider, RemoteTarget, Name) sort order so two saves of the same
// logical state produce byte-identical output, rather than flapping with Go's
// randomized map iteration order.
func (r *Registry) save() error {
	if r.path == "" {
		return nil
	}
	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	keys := make([]vmHandle, 0, len(r.vms))
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
		vc := r.vms[k]
		vms = append(vms, diskEntry{
			Provider:     k.scope.Provider,
			RemoteTarget: k.scope.RemoteTarget,
			Name:         k.name,
			Checkouts:    vc.Checkouts,
			Truncated:    vc.Truncated,
			SweptAt:      vc.SweptAt,
		})
	}

	data, err := json.MarshalIndent(fileSchema{Version: currentVersion, VMs: vms}, "", "  ")
	if err != nil {
		return err
	}

	// os.CreateTemp opens at mode 0600, matching secrets.json/managed-vms.json.
	tmp, err := os.CreateTemp(dir, ".checkout-registry-*.json.tmp")
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

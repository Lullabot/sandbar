package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/lullabot/sandbar/internal/lima"
)

// limaprovenance.go implements Provenancer for limaProvider as a marker file
// at lima.MarkerPath(hf, name) — <LimaHome>/<name>/sandbar.json — read and
// written through the provider's own hostFiles handle (lima.HostFiles), so it
// works identically for local Lima (the real filesystem) and remote Lima (the
// same file over SSH). remoteLimaProvider embeds *limaProvider and inherits
// every method here unchanged: its hostFiles is the SSHHost NewRemoteLima
// wires in, so the marker just lands on the remote host instead.
//
// The Provenance payload type lives in THIS package (provenance.go), not
// internal/lima, because internal/lima cannot import it back without cycling
// (provider already imports lima) — see lima/provenance.go's doc comment. The
// JSON encode/decode therefore lives here too.

// decodeProvenance parses raw marker bytes into a Provenance, returning
// (Provenance{}, false) on ANY parse error rather than surfacing it. A single
// malformed marker must degrade to "unmanaged", never abort a batched read or
// hide a VM's peers — see Provenancer.Provenance and .ProvenanceOf.
func decodeProvenance(data []byte) (Provenance, bool) {
	var p Provenance
	if err := json.Unmarshal(data, &p); err != nil {
		return Provenance{}, false
	}
	return p, true
}

// Provenance returns a marker for every instance under this provider's Lima
// home that carries one, read in ONE host round trip via the HostFiles
// batched read (lima.HostFiles.ReadInstanceMarkers — see that method for the
// local vs SSH implementations). A missing or unparseable marker is simply
// absent from the result, never an error that aborts the whole batch.
func (p *limaProvider) Provenance(ctx context.Context) (map[string]Provenance, error) {
	hf := p.hostFiles
	raw, err := hf.ReadInstanceMarkers(ctx, hf.LimaHome(), lima.MarkerFilename)
	if err != nil {
		return nil, fmt.Errorf("read provenance markers: %w", err)
	}
	out := make(map[string]Provenance, len(raw))
	for name, data := range raw {
		if pv, ok := decodeProvenance(data); ok {
			out[name] = pv
		}
	}
	return out, nil
}

// ProvenanceOf returns the marker for one instance. ok is false both when no
// marker file exists AND when one exists but fails to parse — a malformed
// marker must degrade to "unmanaged", never surface as an error, exactly like
// the batched Provenance read.
func (p *limaProvider) ProvenanceOf(ctx context.Context, name string) (Provenance, bool, error) {
	hf := p.hostFiles
	data, err := hf.ReadFile(lima.MarkerPath(hf, name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Provenance{}, false, nil
		}
		return Provenance{}, false, fmt.Errorf("read provenance marker for %s: %w", name, err)
	}
	pv, ok := decodeProvenance(data)
	return pv, ok, nil
}

// MarkManaged writes (or overwrites) the provenance marker for name, and
// REFUSES to write one for an instance that does not exist yet — returning
// ErrNoInstance, having created nothing.
//
// That refusal is the whole point of the Stat below, and it is load-bearing.
// WriteFile creates missing parents, so without the check a marker write for a
// not-yet-cloned instance manufactures <LimaHome>/<name>/ containing nothing but
// a sandbar.json — precisely the lima.yaml-less directory that makes EVERY later
// `limactl list` fatal (see provision/cleanup.go's package comment) and wedges
// the board for a VM that was never created. `limactl clone` then also refuses
// the name, so the user cannot even retry without hand-removing a directory they
// have never heard of.
//
// This used to be a comment asserting "the instance directory already exists by
// the time this is called (it runs from the create path, after clone/boot)".
// That precondition held for the create path and then quietly stopped holding
// when progress republishing began calling this from the streamed-output
// handler, which starts firing at the FIRST phase banner — long before the
// clone. An invariant that matters this much is enforced here rather than
// assumed of every caller.
func (p *limaProvider) MarkManaged(ctx context.Context, name string, pv Provenance) error {
	data, err := json.Marshal(pv)
	if err != nil {
		return fmt.Errorf("encode provenance marker for %s: %w", name, err)
	}
	hf := p.hostFiles
	if _, err := hf.Stat(lima.InstanceDir(hf, name)); err != nil {
		return fmt.Errorf("provenance marker for %s: %w", name, ErrNoInstance)
	}
	if err := hf.WriteFile(lima.MarkerPath(hf, name), data, 0o700, 0o600); err != nil {
		return fmt.Errorf("write provenance marker for %s: %w", name, err)
	}
	return nil
}

// Unmark clears any provenance marker for name. RemoveAll's "missing path is
// not an error" contract means unmarking an already-unmanaged instance is a
// silent no-op, not a failure.
func (p *limaProvider) Unmark(ctx context.Context, name string) error {
	hf := p.hostFiles
	if err := hf.RemoveAll(lima.MarkerPath(hf, name)); err != nil {
		return fmt.Errorf("remove provenance marker for %s: %w", name, err)
	}
	return nil
}

// var _ Provenancer = (*limaProvider)(nil) is the compile-time proof that the
// local Lima provider satisfies Provenancer. remoteLimaProvider embeds
// *limaProvider and inherits these four methods unchanged, so it needs no
// method of its own — only its own compile-time assertion (see remote.go).
var _ Provenancer = (*limaProvider)(nil)

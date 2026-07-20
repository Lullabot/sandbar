package provider

// proxmoxprovenance.go implements Provenancer for proxmoxProvider by writing
// sandbar's provenance INTO Proxmox's own VM metadata rather than a sidecar
// file: there is no "host proxmoxFiles runs on" for a file to live on (see
// proxmoxfiles.go's ReadInstanceMarkers, which always reports zero markers
// for exactly that reason — THIS file is the real source of truth for this
// backend, not that one).
//
// Storage shape: two complementary places on the same VM's own config, each
// load-bearing for a different reason.
//
//   - A `sandbar` TAG (PVE's `tags` config key, semicolon-separated with any
//     operator tags already present). This is what makes a managed fleet
//     filterable as `tag:sandbar` in the Proxmox web UI — a real operator
//     benefit distinct from sand's own bookkeeping — and, just as
//     importantly, it is the CHEAP field: GET /cluster/resources returns it
//     for every VM in the pool in one call, which is what lets Provenance
//     decide which VMs are worth a second, per-VM round trip without ever
//     paying for the ones that are not (see Provenance's own comment).
//   - A FENCED block inside the `description` config key, carrying the full
//     Provenance payload as compact JSON:
//
//       <!-- sandbar:begin -->
//       {"schema":3,"base":"...", ...}
//       <!-- sandbar:end -->
//
//     The fence is what lets Unmark surgically remove sandbar's own text
//     while leaving anything an operator typed into the same description box
//     — a maintenance note, a ticket link — completely untouched. Every
//     writer/reader pair below (splice/remove, decode) treats the fenced span
//     as the ONLY part of that string sandbar is ever allowed to own.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/pve"
)

// sandbarTag is the tag MarkManaged adds and Unmark removes. Proxmox VE
// lower-cases tags server-side, so a literal, case-sensitive comparison
// against this already-lowercase constant is safe everywhere it is used
// below.
const sandbarTag = "sandbar"

// provenanceBeginMarker / provenanceEndMarker fence the JSON payload inside a
// VM's description. HTML-comment syntax was chosen deliberately: it renders
// as nothing (not as visible clutter) in any Markdown-aware viewer of the
// description text, while remaining trivially greppable for a human staring
// at the raw field in the PVE UI.
const (
	provenanceBeginMarker = "<!-- sandbar:begin -->"
	provenanceEndMarker   = "<!-- sandbar:end -->"
)

// provenanceBlockRE matches the fenced block, capturing the JSON between the
// markers non-greedily so a description holding MULTIPLE HTML comments (an
// operator's own) can never make the match swallow text between an unrelated
// comment pair. (?s) lets '.' cross newlines, since compact JSON is one line
// today but nothing here depends on that staying true.
var provenanceBlockRE = regexp.MustCompile(`(?s)<!-- sandbar:begin -->\r?\n(.*?)\r?\n<!-- sandbar:end -->`)

// decodeProvenanceBlock extracts and parses the fenced sandbar block from a VM's
// description text, returning (Provenance{}, false) whenever EITHER the
// markers are absent OR the text between them fails to parse as JSON. The two
// failure modes are collapsed into one on purpose: an operator who deletes
// the markers by hand and one who mangles the JSON inside them both mean "not
// managed", never an error that would abort a batched Provenance read over
// the fleet's OTHER, perfectly readable markers — see limaprovenance.go's
// decodeProvenanceBlock for the sidecar-file twin of this same rule.
func decodeProvenanceBlock(description string) (Provenance, bool) {
	m := provenanceBlockRE.FindStringSubmatch(description)
	if m == nil {
		return Provenance{}, false
	}
	var pv Provenance
	if err := json.Unmarshal([]byte(m[1]), &pv); err != nil {
		return Provenance{}, false
	}
	return pv, true
}

// spliceDescriptionBlock returns description with the sandbar-managed block
// set to hold payload (compact JSON, no surrounding whitespace of its own).
// An EXISTING block is replaced IN PLACE — whatever text precedes or follows
// it is left untouched byte-for-byte — so re-marking an already-managed VM
// (a rebuild that writes a fresh Provenance) never disturbs operator notes
// sitting around it. When no block exists yet, the new one is appended after
// a blank-line separator from any pre-existing operator text, so the two
// read as visually distinct paragraphs in the PVE UI's description pane
// rather than running together.
func spliceDescriptionBlock(description string, payload []byte) string {
	block := provenanceBeginMarker + "\n" + string(payload) + "\n" + provenanceEndMarker
	if loc := provenanceBlockRE.FindStringIndex(description); loc != nil {
		return description[:loc[0]] + block + description[loc[1]:]
	}
	if trimmed := strings.TrimRight(description, "\n"); trimmed != "" {
		return trimmed + "\n\n" + block
	}
	return block
}

// removeDescriptionBlock strips the fenced sandbar block (markers included)
// from description, leaving any operator-authored text around it EXACTLY as
// it was. It also trims the blank-line separator spliceDescriptionBlock
// itself introduces when appending a new block, so a mark/unmark cycle is the
// exact inverse of a mark and repeated cycles never accumulate blank lines. A
// description carrying no block at all is returned unchanged — Unmark on an
// already-unmanaged VM is a no-op, not an error, matching the sidecar-file
// provider's RemoveAll-on-a-missing-path contract.
func removeDescriptionBlock(description string) string {
	loc := provenanceBlockRE.FindStringIndex(description)
	if loc == nil {
		return description
	}
	before := strings.TrimRight(description[:loc[0]], "\n")
	after := strings.TrimLeft(description[loc[1]:], "\n")
	switch {
	case before == "":
		return after
	case after == "":
		return before
	default:
		return before + "\n\n" + after
	}
}

// splitTags parses PVE's semicolon-separated tags value into its individual
// tags, dropping empty entries — an empty Tags string, or one with a stray
// leading/trailing/doubled separator, must never produce a phantom "" tag
// that mergeTag or removeTag would then mishandle.
func splitTags(tags string) []string {
	if tags == "" {
		return nil
	}
	raw := strings.Split(tags, ";")
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// hasTag reports whether tags (PVE's semicolon-separated form) contains tag
// EXACTLY — never a substring match, so a hypothetical operator tag like
// "sandbar-archive" is never mistaken for sandbar's own marker.
func hasTag(tags, tag string) bool {
	return slices.Contains(splitTags(tags), tag)
}

// mergeTag adds tag to tags without dropping any operator-set tag already
// there, and without duplicating it if MarkManaged runs twice against the
// same VM (a rebuild, a retried create).
func mergeTag(tags, tag string) string {
	parts := splitTags(tags)
	if slices.Contains(parts, tag) {
		return tags
	}
	return strings.Join(append(parts, tag), ";")
}

// removeTag drops tag from tags, leaving every operator tag exactly as it
// was and in its original order.
func removeTag(tags, tag string) string {
	parts := splitTags(tags)
	kept := make([]string, 0, len(parts))
	for _, t := range parts {
		if t != tag {
			kept = append(kept, t)
		}
	}
	return strings.Join(kept, ";")
}

// provenanceFetchConcurrency bounds how many /config requests Provenance
// fires in parallel while fetching descriptions for the VMs its tag filter
// selected. It is deliberately small and deliberately NOT "as many as
// candidates": PVE closes the underlying connection on ANY response >=400
// and applies no admission control of its own on this endpoint, so an
// unbounded fan-out would turn one slow node, one erroring VM, or a
// momentary blip into connection churn across the ENTIRE batch rather than a
// single retry-able failure. 4-8 keeps the batch fast on the common case (a
// handful of managed VMs) while never asking a PVE node to answer more than a
// handful of connections at once.
const provenanceFetchConcurrency = 6

// provenanceCandidate is one VM Provenance decided is worth a config fetch:
// it carries the sandbar tag on the listing the tag filter already paid for.
type provenanceCandidate struct {
	name string
	vmid int
}

// Provenance returns a marker for every instance in the pool that carries
// one. The interface's contract is "one host round trip for the whole
// fleet" — a contract this backend genuinely cannot meet to the LETTER,
// because GET /cluster/resources (the one cluster-wide listing PVE offers)
// returns tags but never description, and description is where the payload
// lives. What it honours instead is the INTENT behind that contract: constant
// work per MANAGED VM, and zero cost for every unmanaged one.
//
// The shape is therefore: one /cluster/resources call to find every VM
// carrying the sandbar tag (using the SAME name->VMID resolution List() uses,
// so an ambiguous name is never attributed to a different VM than every
// other verb in this provider would act on), then a GetConfig call per
// tagged VM to read its description — the only place the fenced block can
// be. A pool with no managed VMs costs exactly the one listing call; a pool
// with a thousand VMs and five managed ones costs six calls, not a thousand
// and six.
//
// A VM whose description has no fenced block, or an unparseable one, is
// simply absent from the result (see decodeProvenanceBlock) — never an error that
// would hide its (valid) peers. The same tolerance extends to a per-VM
// GetConfig failure: one VM's config being momentarily unreachable must not
// sink the whole batch.
func (p *proxmoxProvider) Provenance(ctx context.Context) (map[string]Provenance, error) {
	resources, err := p.client.ListVMs(ctx, p.pool)
	if err != nil {
		return nil, fmt.Errorf("proxmox: listing pool %q for provenance: %w", p.pool, err)
	}

	// index is the SAME name->VMID resolution List() builds (lowest VMID
	// wins an ambiguous name), reused here so a name's provenance is always
	// attributed to the identical VM every other verb in this provider would
	// act on for that name.
	index := p.indexOf(resources)
	byVMID := make(map[int]pve.VMResource, len(resources))
	for _, r := range resources {
		byVMID[r.VMID] = r
	}

	var candidates []provenanceCandidate
	for name, vmid := range index {
		if r, ok := byVMID[vmid]; ok && hasTag(r.Tags, sandbarTag) {
			candidates = append(candidates, provenanceCandidate{name: name, vmid: vmid})
		}
	}

	out := make(map[string]Provenance, len(candidates))
	if len(candidates) == 0 {
		return out, nil
	}

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, provenanceFetchConcurrency)
	)
	for _, c := range candidates {
		wg.Add(1)
		sem <- struct{}{}
		go func(c provenanceCandidate) {
			defer wg.Done()
			defer func() { <-sem }()

			cfg, err := p.client.GetConfig(ctx, c.vmid)
			if err != nil {
				// A single VM's config being unreachable must not hide every
				// other (perfectly readable) marker in the batch.
				return
			}
			desc, _ := cfg["description"].(string)
			if pv, ok := decodeProvenanceBlock(desc); ok {
				mu.Lock()
				out[c.name] = pv
				mu.Unlock()
			}
		}(c)
	}
	wg.Wait()
	return out, nil
}

// ProvenanceOf returns the marker for one instance, resolving name to a VMID
// through the SAME verified resolve() every lifecycle method uses (so a
// recycled VMID can never attribute one VM's provenance to another). An
// instance absent from the pool reads back as (zero, false, nil) — "not
// managed" — never an error; a config read failure for an instance that DOES
// exist is the one case that legitimately propagates, since unlike the
// batched Provenance a single unreachable VM has no peers to protect.
func (p *proxmoxProvider) ProvenanceOf(ctx context.Context, name string) (Provenance, bool, error) {
	vmid, _, err := p.resolve(ctx, name)
	if err != nil {
		if errors.Is(err, lima.ErrNoSuchInstance) {
			return Provenance{}, false, nil
		}
		return Provenance{}, false, err
	}
	cfg, err := p.client.GetConfig(ctx, vmid)
	if err != nil {
		return Provenance{}, false, fmt.Errorf("proxmox: reading %s's provenance: %w", name, err)
	}
	desc, _ := cfg["description"].(string)
	pv, ok := decodeProvenanceBlock(desc)
	return pv, ok, nil
}

// MarkManaged writes (or overwrites) the provenance marker for name: the
// sandbar tag merged into whatever tags are already there, and the fenced
// description block spliced in without disturbing any operator text around
// it. Both are written in ONE PUT (SetConfigSync), so a reader can never
// observe the tag present without the block, or vice versa.
//
// Like the sidecar-file provider, this REFUSES to mark an instance that does
// not exist — resolve's ErrNoSuchInstance becomes ErrNoInstance here for the
// same reason limaprovenance.go's Stat check exists: a marker write with
// nowhere legitimate to land is a bug at the call site, not something to
// paper over by inventing config for a VMID sand never created.
func (p *proxmoxProvider) MarkManaged(ctx context.Context, name string, pv Provenance) error {
	vmid, _, err := p.resolve(ctx, name)
	if err != nil {
		if errors.Is(err, lima.ErrNoSuchInstance) {
			return fmt.Errorf("proxmox: mark %s managed: %w", name, ErrNoInstance)
		}
		return err
	}

	cfg, err := p.client.GetConfig(ctx, vmid)
	if err != nil {
		return fmt.Errorf("proxmox: reading %s's config to mark it managed: %w", name, err)
	}
	payload, err := json.Marshal(pv)
	if err != nil {
		return fmt.Errorf("proxmox: encoding provenance for %s: %w", name, err)
	}

	desc, _ := cfg["description"].(string)
	tags, _ := cfg["tags"].(string)

	// Description text travels as a normal form value; url.Values.Encode
	// (inside SetConfigSync's request body) is the only escaping it ever
	// gets — hand-escaping it here would double-encode it on the wire.
	form := url.Values{
		"description": {spliceDescriptionBlock(desc, payload)},
		"tags":        {mergeTag(tags, sandbarTag)},
	}
	if err := p.client.SetConfigSync(ctx, vmid, form); err != nil {
		return fmt.Errorf("proxmox: writing provenance for %s: %w", name, err)
	}
	return nil
}

// Unmark clears any provenance marker for name: the sandbar tag and the
// fenced description block, both in the SAME PUT, while leaving any
// operator-authored tags or description text exactly as it was. An instance
// no longer in the pool has nothing to unmark and is treated as a silent
// no-op, matching the sidecar-file provider's RemoveAll-on-a-missing-path
// contract; any other resolve failure (notably a permission error) is the
// caller's to see verbatim.
func (p *proxmoxProvider) Unmark(ctx context.Context, name string) error {
	vmid, _, err := p.resolve(ctx, name)
	if err != nil {
		if errors.Is(err, lima.ErrNoSuchInstance) {
			return nil
		}
		return err
	}

	cfg, err := p.client.GetConfig(ctx, vmid)
	if err != nil {
		return fmt.Errorf("proxmox: reading %s's config to unmark it: %w", name, err)
	}
	desc, _ := cfg["description"].(string)
	tags, _ := cfg["tags"].(string)

	form := url.Values{
		"description": {removeDescriptionBlock(desc)},
		"tags":        {removeTag(tags, sandbarTag)},
	}
	if err := p.client.SetConfigSync(ctx, vmid, form); err != nil {
		return fmt.Errorf("proxmox: clearing provenance for %s: %w", name, err)
	}
	return nil
}

// var _ Provenancer = (*proxmoxProvider)(nil) is the compile-time proof that
// the Proxmox provider satisfies Provenancer — the DoD this whole file
// exists to meet, and the exact assertion the Provenancer doc comment
// anticipated when it said a future Proxmox backend could do this "with no
// redesign".
var _ Provenancer = (*proxmoxProvider)(nil)

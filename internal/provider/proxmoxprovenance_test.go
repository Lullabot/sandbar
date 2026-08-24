package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/vm"
)

// --- stateful config mock --------------------------------------------------

// configStore is a tiny in-memory stand-in for PVE's per-VM configuration,
// keyed by VMID, so a test can write via SetConfigSync and read the SAME
// state back via GetConfig — exactly the round trip MarkManaged and
// Provenance/ProvenanceOf need to prove. pveMock's own m.data/m.on helpers
// (proxmox_test.go) answer every request with one FIXED body, which cannot
// express "what GetConfig returns depends on what an earlier SetConfigSync
// wrote" — hence this small stateful layer on top of it.
type configStore struct {
	mu   sync.Mutex
	cfgs map[int]map[string]string
}

func (s *configStore) set(vmid int, cfg map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfgs[vmid] = cfg
}

// get returns a COPY of vmid's config, so a caller mutating it can never
// corrupt the store behind the mock's back.
func (s *configStore) get(vmid int) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.cfgs[vmid]))
	for k, v := range s.cfgs[vmid] {
		out[k] = v
	}
	return out
}

// newStatefulConfigMock wires GET/PUT .../qemu/{vmid}/config for every vmid
// in vmids (default 100 and 101, matching clusterResources' "web" and "api")
// to a shared configStore, so a MarkManaged/Unmark write and a subsequent
// Provenance/ProvenanceOf/GetConfig read observe the SAME state a real PVE
// node would hold between the two calls.
func newStatefulConfigMock(t *testing.T, vmids ...int) (*pveMock, *configStore) {
	t.Helper()
	m := newPVEMock(t)
	store := &configStore{cfgs: map[int]map[string]string{}}
	if len(vmids) == 0 {
		vmids = []int{100, 101}
	}
	for _, id := range vmids {
		id := id
		store.set(id, map[string]string{})
		path := fmt.Sprintf("/nodes/pve1/qemu/%d/config", id)
		m.on(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Method {
			case http.MethodGet:
				body, err := json.Marshal(store.get(id))
				if err != nil {
					t.Fatalf("marshal stored config for vmid %d: %v", id, err)
				}
				fmt.Fprintf(w, `{"data":%s}`, body)
			case http.MethodPut:
				if err := r.ParseForm(); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprintf(w, `{"data":null,"message":%q}`, err.Error())
					return
				}
				cfg := store.get(id)
				for k := range r.PostForm {
					cfg[k] = r.PostForm.Get(k)
				}
				store.set(id, cfg)
				fmt.Fprint(w, `{"data":null}`)
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		})
	}
	return m, store
}

// --- Provenancer interface ---------------------------------------------------

// TestProxmoxSatisfiesProvenancerInterface is the DoD's compile-time proof
// restated as a named test, exactly as TestProxmoxSatisfiesProviderInterface
// does for provider.Provider in proxmox_test.go.
func TestProxmoxSatisfiesProvenancerInterface(t *testing.T) {
	var _ Provenancer = (*proxmoxProvider)(nil)
}

// --- MarkManaged / ProvenanceOf round trip ----------------------------------

// TestProxmoxProvenanceRoundTrip proves MarkManaged writes a marker (tag +
// fenced description block) that a subsequent ProvenanceOf reads back with
// every field unchanged.
func TestProxmoxProvenanceRoundTrip(t *testing.T) {
	m, cfgs := newStatefulConfigMock(t)
	m.data("/cluster/resources", clusterResources)
	m.data("/nodes/pve1/qemu/100/status/current", `{"vmid":100,"name":"web","status":"running"}`)
	p := newProxmoxForTest(t, m)

	if _, err := p.List(); err != nil {
		t.Fatalf("List: %v", err)
	}

	want := Provenance{
		SchemaVersion:  MarkerSchemaVersion,
		Base:           "sandbar-base",
		Config:         vm.CreateConfig{Name: "web", BaseName: "sandbar-base", CPUs: 4},
		SandbarVersion: "0.6.0",
		CreatedAt:      "2026-07-20T00:00:00Z",
	}
	if err := p.MarkManaged(context.Background(), "web", want); err != nil {
		t.Fatalf("MarkManaged: %v", err)
	}

	got, ok, err := p.ProvenanceOf(context.Background(), "web")
	if err != nil {
		t.Fatalf("ProvenanceOf: %v", err)
	}
	if !ok {
		t.Fatal("ProvenanceOf ok = false after MarkManaged, want true")
	}
	if got != want {
		t.Fatalf("ProvenanceOf = %+v, want %+v", got, want)
	}

	// The tag must be present too: it is what makes the fleet filterable as
	// tag:sandbar in the Proxmox web UI, a real operator benefit distinct
	// from the JSON payload sand itself reads back.
	if tags := cfgs.get(100)["tags"]; tags != "sandbar" {
		t.Errorf("tags after MarkManaged = %q, want %q", tags, "sandbar")
	}
	if desc := cfgs.get(100)["description"]; desc == "" {
		t.Error("description after MarkManaged is empty, want the fenced provenance block")
	}
}

// TestProxmoxMarkManagedMergesTagsWithoutDroppingOperatorTags and
// TestProxmoxUnmarkPreservesOperatorText together cover the acceptance
// criterion that Unmark removes exactly sandbar's own contribution — the tag
// and the fenced block — while leaving any operator-authored tags and
// description text completely untouched.
func TestProxmoxUnmarkPreservesOperatorText(t *testing.T) {
	m, cfgs := newStatefulConfigMock(t)
	m.data("/cluster/resources", clusterResources)
	m.data("/nodes/pve1/qemu/100/status/current", `{"vmid":100,"name":"web","status":"running"}`)
	p := newProxmoxForTest(t, m)
	if _, err := p.List(); err != nil {
		t.Fatalf("List: %v", err)
	}

	// Seed operator-authored config exactly as if a human had set it up
	// before sand ever touched this VM.
	cfgs.set(100, map[string]string{
		"description": "Owner: platform team\nTicket: OPS-123",
		"tags":        "team-x;prod",
	})

	pv := Provenance{SchemaVersion: MarkerSchemaVersion, Base: "sandbar-base"}
	if err := p.MarkManaged(context.Background(), "web", pv); err != nil {
		t.Fatalf("MarkManaged: %v", err)
	}
	marked := cfgs.get(100)
	if !strings.Contains(marked["description"], "Owner: platform team") || !strings.Contains(marked["description"], "Ticket: OPS-123") {
		t.Fatalf("MarkManaged clobbered operator description text: %q", marked["description"])
	}
	if want := "team-x;prod;sandbar"; marked["tags"] != want {
		t.Fatalf("tags after MarkManaged = %q, want %q (operator tags kept, sandbar appended)", marked["tags"], want)
	}

	if err := p.Unmark(context.Background(), "web"); err != nil {
		t.Fatalf("Unmark: %v", err)
	}
	unmarked := cfgs.get(100)
	if strings.Contains(unmarked["description"], "sandbar:begin") {
		t.Errorf("Unmark left the fenced block behind: %q", unmarked["description"])
	}
	if !strings.Contains(unmarked["description"], "Owner: platform team") || !strings.Contains(unmarked["description"], "Ticket: OPS-123") {
		t.Errorf("Unmark clobbered operator description text: %q", unmarked["description"])
	}
	if want := "team-x;prod"; unmarked["tags"] != want {
		t.Errorf("tags after Unmark = %q, want %q (operator tags preserved, sandbar removed)", unmarked["tags"], want)
	}

	if _, ok, err := p.ProvenanceOf(context.Background(), "web"); err != nil || ok {
		t.Fatalf("ProvenanceOf after Unmark = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

// TestProxmoxMarkManagedRefusesMissingInstance mirrors the sidecar-file
// provider's refusal to conjure a marker for a VM that does not exist:
// resolve's lima.ErrNoSuchInstance must surface as provider.ErrNoInstance.
func TestProxmoxMarkManagedRefusesMissingInstance(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", `[]`)
	p := newProxmoxForTest(t, m)

	err := p.MarkManaged(context.Background(), "ghost", Provenance{SchemaVersion: MarkerSchemaVersion})
	if !errors.Is(err, ErrNoInstance) {
		t.Fatalf("MarkManaged(ghost) = %v; want ErrNoInstance", err)
	}
}

// TestProxmoxUnmarkOnMissingInstanceIsNoop matches RemoveAll's
// missing-path-is-not-an-error contract the sidecar-file provider relies on:
// unmarking a VM that no longer exists must not be treated as a failure.
func TestProxmoxUnmarkOnMissingInstanceIsNoop(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", `[]`)
	p := newProxmoxForTest(t, m)

	if err := p.Unmark(context.Background(), "ghost"); err != nil {
		t.Fatalf("Unmark(ghost) = %v; want nil (nothing to unmark)", err)
	}
}

// --- ProvenanceOf: unparseable block --------------------------------------

// TestProxmoxProvenanceOfUnparseableBlockIsNotManaged proves that garbage
// between the fence markers reads back as "not managed", never as an error —
// the same tolerance limaprovenance.go's ProvenanceOf gives a malformed
// sidecar file.
func TestProxmoxProvenanceOfUnparseableBlockIsNotManaged(t *testing.T) {
	m, cfgs := newStatefulConfigMock(t)
	m.data("/cluster/resources", clusterResources)
	m.data("/nodes/pve1/qemu/100/status/current", `{"vmid":100,"name":"web","status":"running"}`)
	p := newProxmoxForTest(t, m)
	if _, err := p.List(); err != nil {
		t.Fatalf("List: %v", err)
	}
	cfgs.set(100, map[string]string{
		"description": provenanceBeginMarker + "\nnot valid json\n" + provenanceEndMarker,
		"tags":        "sandbar",
	})

	got, ok, err := p.ProvenanceOf(context.Background(), "web")
	if err != nil {
		t.Fatalf("ProvenanceOf: %v", err)
	}
	if ok {
		t.Fatalf("ProvenanceOf ok = true for an unparseable block, want false (got %+v)", got)
	}
	if got != (Provenance{}) {
		t.Fatalf("ProvenanceOf value = %+v, want the zero value", got)
	}
}

// --- Provenance: batching --------------------------------------------------

// TestProxmoxProvenanceIsOneBatchedClusterResourcesCall is the acceptance
// criterion's core assertion: Provenance must make exactly ONE
// /cluster/resources call for the whole fleet, and must fetch /config ONLY
// for VMs the tag filter selected — never for an untagged VM, which is what
// keeps the cost constant per MANAGED VM instead of scaling with pool size.
func TestProxmoxProvenanceIsOneBatchedClusterResourcesCall(t *testing.T) {
	m, cfgs := newStatefulConfigMock(t)
	resources := `[
	  {"vmid":100,"name":"web","node":"pve1","pool":"sandbar","status":"running","type":"qemu","tags":"sandbar"},
	  {"vmid":101,"name":"api","node":"pve1","pool":"sandbar","status":"stopped","type":"qemu","tags":"team-x"}
	]`
	m.data("/cluster/resources", resources)
	payload, err := json.Marshal(Provenance{SchemaVersion: MarkerSchemaVersion, Base: "sandbar-base"})
	if err != nil {
		t.Fatalf("marshal fixture provenance: %v", err)
	}
	cfgs.set(100, map[string]string{
		"description": provenanceBeginMarker + "\n" + string(payload) + "\n" + provenanceEndMarker,
		"tags":        "sandbar",
	})
	p := newProxmoxForTest(t, m)

	got, err := p.Provenance(context.Background())
	if err != nil {
		t.Fatalf("Provenance: %v", err)
	}
	if n := m.count("/cluster/resources"); n != 1 {
		t.Errorf("/cluster/resources requested %d time(s); want exactly 1 for the whole fleet", n)
	}
	if m.sawPath("/nodes/pve1/qemu/101/config") {
		t.Errorf("Provenance fetched config for an UNTAGGED VM; requests: %v", m.seen())
	}
	if !m.sawPath("/nodes/pve1/qemu/100/config") {
		t.Errorf("Provenance never fetched config for the tagged VM; requests: %v", m.seen())
	}
	if len(got) != 1 {
		t.Fatalf("Provenance() = %+v; want exactly the one managed entry (web)", got)
	}
	if _, ok := got["web"]; !ok {
		t.Errorf("Provenance() missing web: %+v", got)
	}
}

// TestProxmoxProvenanceBatchToleratesOneUnparseableMarker proves the DoD's
// last requirement directly against the batched path: a tagged VM whose
// description block does not parse must be ABSENT from the result, and must
// not prevent its (perfectly valid) peer from being reported.
func TestProxmoxProvenanceBatchToleratesOneUnparseableMarker(t *testing.T) {
	m, cfgs := newStatefulConfigMock(t)
	resources := `[
	  {"vmid":100,"name":"web","node":"pve1","pool":"sandbar","status":"running","type":"qemu","tags":"sandbar"},
	  {"vmid":101,"name":"api","node":"pve1","pool":"sandbar","status":"stopped","type":"qemu","tags":"sandbar"}
	]`
	m.data("/cluster/resources", resources)
	goodPayload, err := json.Marshal(Provenance{SchemaVersion: MarkerSchemaVersion, Base: "good"})
	if err != nil {
		t.Fatalf("marshal fixture provenance: %v", err)
	}
	cfgs.set(100, map[string]string{
		"description": provenanceBeginMarker + "\n" + string(goodPayload) + "\n" + provenanceEndMarker,
		"tags":        "sandbar",
	})
	cfgs.set(101, map[string]string{
		"description": provenanceBeginMarker + "\nnot json at all\n" + provenanceEndMarker,
		"tags":        "sandbar",
	})
	p := newProxmoxForTest(t, m)

	got, err := p.Provenance(context.Background())
	if err != nil {
		t.Fatalf("Provenance: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Provenance() = %+v; want exactly the one valid marker (web) — the broken one (api) must not abort the batch", got)
	}
	if _, ok := got["web"]; !ok {
		t.Errorf("Provenance() missing web (the valid marker): %+v", got)
	}
	if _, ok := got["api"]; ok {
		t.Errorf("Provenance() included api, whose marker is unparseable: %+v", got)
	}
}

// TestProxmoxProvenanceEmptyPoolCostsOnlyTheListing proves the "zero cost per
// unmanaged VM" half of Provenance's documented intent: a pool with no
// sandbar-tagged VM at all makes no /config request whatsoever.
func TestProxmoxProvenanceEmptyPoolCostsOnlyTheListing(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", clusterResources) // none of these carry a tags field
	p := newProxmoxForTest(t, m)

	got, err := p.Provenance(context.Background())
	if err != nil {
		t.Fatalf("Provenance: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Provenance() = %+v; want empty (no VM in the fixture carries the sandbar tag)", got)
	}
	for _, seen := range m.seen() {
		if strings.HasSuffix(seen, "/config") {
			t.Errorf("Provenance fetched a config despite no tagged VM; requests: %v", m.seen())
		}
	}
}

// --- pure helper functions --------------------------------------------------

func TestSplitTagsAndHasTag(t *testing.T) {
	cases := []struct {
		name, tags string
		want       []string
	}{
		{"empty", "", nil},
		{"single", "sandbar", []string{"sandbar"}},
		{"multiple", "team-x;sandbar;prod", []string{"team-x", "sandbar", "prod"}},
		{"stray separators", ";team-x;;sandbar;", []string{"team-x", "sandbar"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := splitTags(tc.tags); !slices.Equal(got, tc.want) {
				t.Errorf("splitTags(%q) = %v, want %v", tc.tags, got, tc.want)
			}
		})
	}
	if !hasTag("team-x;sandbar", "sandbar") {
		t.Error("hasTag(\"team-x;sandbar\", \"sandbar\") = false, want true")
	}
	if hasTag("sandbar-archive", "sandbar") {
		t.Error("hasTag matched \"sandbar-archive\" as a substring; want an exact-tag match only")
	}
}

func TestMergeTagAndRemoveTag(t *testing.T) {
	if got := mergeTag("", "sandbar"); got != "sandbar" {
		t.Errorf("mergeTag(\"\", sandbar) = %q, want %q", got, "sandbar")
	}
	if got := mergeTag("team-x", "sandbar"); got != "team-x;sandbar" {
		t.Errorf("mergeTag(team-x, sandbar) = %q, want %q", got, "team-x;sandbar")
	}
	if got := mergeTag("team-x;sandbar", "sandbar"); got != "team-x;sandbar" {
		t.Errorf("mergeTag must not duplicate an already-present tag: got %q", got)
	}
	if got := removeTag("team-x;sandbar;prod", "sandbar"); got != "team-x;prod" {
		t.Errorf("removeTag(team-x;sandbar;prod, sandbar) = %q, want %q", got, "team-x;prod")
	}
	if got := removeTag("sandbar", "sandbar"); got != "" {
		t.Errorf("removeTag of the only tag = %q, want empty", got)
	}
}

// TestSpliceAndRemoveDescriptionBlock exercises the block writer/remover
// directly (append, replace-in-place, and remove), independent of any mock
// server, since the string surgery is the part most worth pinning precisely.
func TestSpliceAndRemoveDescriptionBlock(t *testing.T) {
	payload := []byte(`{"schema":1}`)

	appended := spliceDescriptionBlock("", payload)
	if want := provenanceBeginMarker + "\n{\"schema\":1}\n" + provenanceEndMarker; appended != want {
		t.Fatalf("splice into an empty description = %q, want %q", appended, want)
	}
	if pv, ok := decodeProvenanceBlock(appended); !ok || pv.SchemaVersion != 1 {
		t.Fatalf("decodeProvenanceBlock(appended) = %+v, %v; want schema 1, true", pv, ok)
	}

	withOperator := spliceDescriptionBlock("Owner: ops", payload)
	if !strings.HasPrefix(withOperator, "Owner: ops\n\n"+provenanceBeginMarker) {
		t.Fatalf("splice after operator text = %q; want it appended after a blank-line separator", withOperator)
	}

	replaced := spliceDescriptionBlock(withOperator, []byte(`{"schema":2}`))
	if !strings.HasPrefix(replaced, "Owner: ops\n\n") {
		t.Fatalf("replacing the block lost the surrounding operator text: %q", replaced)
	}
	if pv, ok := decodeProvenanceBlock(replaced); !ok || pv.SchemaVersion != 2 {
		t.Fatalf("decodeProvenanceBlock(replaced) = %+v, %v; want schema 2, true", pv, ok)
	}

	removed := removeDescriptionBlock(replaced)
	if removed != "Owner: ops" {
		t.Fatalf("removeDescriptionBlock(replaced) = %q, want exactly the operator text %q", removed, "Owner: ops")
	}

	if got := removeDescriptionBlock("just operator text, no block"); got != "just operator text, no block" {
		t.Fatalf("removeDescriptionBlock without a block = %q, want it unchanged", got)
	}

	if _, ok := decodeProvenanceBlock(provenanceBeginMarker + "\nnot json\n" + provenanceEndMarker); ok {
		t.Error("decodeProvenanceBlock accepted invalid JSON between the markers")
	}
	if _, ok := decodeProvenanceBlock("no fence markers here at all"); ok {
		t.Error("decodeProvenanceBlock accepted text with no fence at all")
	}
}

// TestRemoveDescriptionBlockKeepsTextOnEitherSide covers the two placements the
// round-trip test never produces, because spliceDescriptionBlock always appends:
// a block an operator has typed text AFTER, and one with text on both sides.
// Both must come back with the operator's own paragraphs intact and separated,
// and with no leading or trailing blank line left where the block was — the
// residue that would otherwise accumulate over repeated mark/unmark cycles.
func TestRemoveDescriptionBlockKeepsTextOnEitherSide(t *testing.T) {
	block := provenanceBeginMarker + "\n{\"schema\":1}\n" + provenanceEndMarker

	cases := []struct {
		name, description, want string
	}{
		{"block only", block, ""},
		{"text after the block", block + "\n\nOwner: ops", "Owner: ops"},
		{"text before the block", "Owner: ops\n\n" + block, "Owner: ops"},
		{"text on both sides", "Owner: ops\n\n" + block + "\n\nTicket: OPS-1", "Owner: ops\n\nTicket: OPS-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := removeDescriptionBlock(tc.description); got != tc.want {
				t.Errorf("removeDescriptionBlock(%q) = %q; want %q", tc.description, got, tc.want)
			}
		})
	}
}

// --- error propagation ------------------------------------------------------

// TestProxmoxProvenanceListFailureIsAnError pins that a fleet-wide listing
// failure is reported rather than returned as an empty marker set. The board
// reads an empty result as "no VM here is managed by sand", which is exactly the
// verdict that makes a delete guard let go of VMs it should be protecting.
func TestProxmoxProvenanceListFailureIsAnError(t *testing.T) {
	m := newPVEMock(t)
	m.fail("/cluster/resources", http.StatusForbidden, "Permission check failed")
	p := newProxmoxForTest(t, m)

	got, err := p.Provenance(context.Background())
	if err == nil {
		t.Fatalf("Provenance = %+v, nil; want the listing failure surfaced", got)
	}
	if !strings.Contains(err.Error(), "sandbar") {
		t.Errorf("err = %q; want it to name the pool that could not be listed", err)
	}
}

// TestProxmoxProvenanceBatchToleratesOneUnreadableConfig is the peer-protection
// rule stated for the fetch itself rather than the payload: one VM whose config
// cannot be read (migrating, locked, momentarily 500ing) must cost only its own
// marker, never the batch. The tagged-but-unreadable VM is the interesting case
// precisely because the tag filter already decided it was worth fetching.
func TestProxmoxProvenanceBatchToleratesOneUnreadableConfig(t *testing.T) {
	m, cfgs := newStatefulConfigMock(t, 100)
	m.data("/cluster/resources", `[
	  {"vmid":100,"name":"web","node":"pve1","pool":"sandbar","status":"running","type":"qemu","tags":"sandbar"},
	  {"vmid":101,"name":"api","node":"pve1","pool":"sandbar","status":"stopped","type":"qemu","tags":"sandbar"}
	]`)
	// 101 is tagged, so it IS fetched — and answers with a failure.
	m.fail("/nodes/pve1/qemu/101/config", http.StatusInternalServerError, "VM 101 is locked (migrate)")
	cfgs.set(100, map[string]string{
		"description": provenanceBeginMarker + "\n" + `{"schema":3,"base":"sandbar-base"}` + "\n" + provenanceEndMarker,
		"tags":        "sandbar",
	})
	p := newProxmoxForTest(t, m)

	got, err := p.Provenance(context.Background())
	if err != nil {
		t.Fatalf("Provenance: %v", err)
	}
	if _, ok := got["web"]; !ok {
		t.Errorf("Provenance() = %+v; want web's readable marker despite api's config failing", got)
	}
	if _, ok := got["api"]; ok {
		t.Errorf("Provenance() included api, whose config could not be read: %+v", got)
	}
}

// TestProxmoxProvenanceOfDistinguishesAbsenceFromFailure pins the one asymmetry
// in this file: a VM that is simply gone reads back as "not managed" with no
// error, while a VM that exists but cannot be read propagates. Collapsing the
// second into the first would report a 403 as "this VM was never sand's", which
// is the worst possible answer for a caller about to decide whether to delete it.
func TestProxmoxProvenanceOfDistinguishesAbsenceFromFailure(t *testing.T) {
	t.Run("listing failure propagates", func(t *testing.T) {
		m := newPVEMock(t)
		m.fail("/cluster/resources", http.StatusForbidden, "Permission check failed")
		p := newProxmoxForTest(t, m)

		if _, ok, err := p.ProvenanceOf(context.Background(), "web"); err == nil || ok {
			t.Fatalf("ProvenanceOf = %v, %v; want a propagated permission failure", ok, err)
		}
	})

	t.Run("config read failure propagates", func(t *testing.T) {
		m := newPVEMock(t)
		m.data("/cluster/resources", clusterResources)
		m.data("/nodes/pve1/qemu/100/status/current", `{"vmid":100,"name":"web","status":"running"}`)
		m.fail("/nodes/pve1/qemu/100/config", http.StatusForbidden, "Permission check failed")
		p := newProxmoxForTest(t, m)

		_, ok, err := p.ProvenanceOf(context.Background(), "web")
		if err == nil || ok {
			t.Fatalf("ProvenanceOf = %v, %v; want the config failure surfaced", ok, err)
		}
		if !strings.Contains(err.Error(), "web") {
			t.Errorf("err = %q; want it to name the instance", err)
		}
	})
}

// TestProxmoxMarkAndUnmarkPropagateFailures sweeps the write path's three
// distinct failure points. Each one leaves the VM's metadata in a state the
// caller did not ask for, so a swallowed error here means sand believes a VM is
// marked (and therefore safe to reclaim, or safe to skip) when PVE holds no such
// record. A permission failure on the resolve is called out separately from a
// missing VM, which Unmark treats as a legitimate no-op.
func TestProxmoxMarkAndUnmarkPropagateFailures(t *testing.T) {
	newMockAt := func(t *testing.T, configHandler http.HandlerFunc) *pveMock {
		m := newPVEMock(t)
		m.data("/cluster/resources", clusterResources)
		m.data("/nodes/pve1/qemu/100/status/current", `{"vmid":100,"name":"web","status":"running"}`)
		m.on("/nodes/pve1/qemu/100/config", configHandler)
		return m
	}
	readOK := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"data":null,"message":"Permission check failed (VM.Config.Options)"}`)
			return
		}
		fmt.Fprint(w, `{"data":{"description":"","tags":""}}`)
	}
	readFails := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"data":null,"message":"Permission check failed"}`)
	}

	t.Run("resolve failure is not mistaken for a missing VM", func(t *testing.T) {
		m := newPVEMock(t)
		m.fail("/cluster/resources", http.StatusForbidden, "Permission check failed")
		p := newProxmoxForTest(t, m)

		err := p.MarkManaged(context.Background(), "web", Provenance{SchemaVersion: MarkerSchemaVersion})
		if err == nil {
			t.Fatal("MarkManaged: expected the permission failure to propagate")
		}
		if errors.Is(err, ErrNoInstance) {
			t.Errorf("MarkManaged reported %v; a permission failure must never read as a missing VM", err)
		}
		// Unmark's missing-VM no-op must likewise not swallow a 403: the caller
		// would take "nothing to unmark" as proof the marker is gone.
		if err := p.Unmark(context.Background(), "web"); err == nil {
			t.Fatal("Unmark: expected the permission failure to propagate, not a silent no-op")
		}
	})

	t.Run("config read failure", func(t *testing.T) {
		p := newProxmoxForTest(t, newMockAt(t, readFails))
		if err := p.MarkManaged(context.Background(), "web", Provenance{SchemaVersion: MarkerSchemaVersion}); err == nil {
			t.Error("MarkManaged: expected the config read failure to propagate")
		}
		p2 := newProxmoxForTest(t, newMockAt(t, readFails))
		if err := p2.Unmark(context.Background(), "web"); err == nil {
			t.Error("Unmark: expected the config read failure to propagate")
		}
	})

	t.Run("config write failure", func(t *testing.T) {
		p := newProxmoxForTest(t, newMockAt(t, readOK))
		if err := p.MarkManaged(context.Background(), "web", Provenance{SchemaVersion: MarkerSchemaVersion}); err == nil {
			t.Error("MarkManaged: expected the config write failure to propagate")
		}
		p2 := newProxmoxForTest(t, newMockAt(t, readOK))
		if err := p2.Unmark(context.Background(), "web"); err == nil {
			t.Error("Unmark: expected the config write failure to propagate")
		}
	})
}

// TestHealingRewritesTheNotesFieldCleanly is the end-of-the-line assertion for
// the stale-marker repair: what the operator actually sees in a Proxmox VM's
// Notes pane afterwards.
//
// The payload below is a REAL marker taken off a VM that had been wedged on
// "Building" for ten days, so this pins the repair against the shape that
// actually occurs rather than one composed to pass. Two things must hold: the
// keys that cause the wedge are GONE from the JSON (not merely false — a
// lingering "provisioning":false would be noise in a field humans read), and
// everything describing the build survives byte-for-byte.
func TestHealingRewritesTheNotesFieldCleanly(t *testing.T) {
	const wedged = `<!-- sandbar:begin -->
{"schema":3,"base":"sandbar-base","config":{"Name":"lullabot-proposals","BaseName":"sandbar-base","CPUs":8,"Memory":"8GiB","Disk":"100GiB"},"sandbar_version":"318afb4","created_at":"2026-08-14T19:25:48Z","provisioning":true,"progress":{"role":"project","index":204}}
<!-- sandbar:end -->`

	pv, ok := decodeProvenanceBlock(wedged)
	if !ok {
		t.Fatal("could not decode the wedged marker")
	}
	if !pv.BuildAbandoned(time.Now()) {
		t.Fatal("a marker ten days in-flight must read as abandoned")
	}

	payload, err := json.Marshal(pv.Ready())
	if err != nil {
		t.Fatalf("encoding the repaired marker: %v", err)
	}
	got := string(payload)

	// The wedge is gone from the text, not just from the decoded struct.
	if strings.Contains(got, "provisioning") {
		t.Errorf("the repaired marker still mentions provisioning: %s", got)
	}
	if strings.Contains(got, "progress") {
		t.Errorf("the repaired marker still carries a progress block: %s", got)
	}
	// The build's record survives — recreate-gating reads Base and Config, and
	// created_at/sandbar_version are the only trace of which build made this VM.
	for _, want := range []string{
		`"schema":3`,
		`"base":"sandbar-base"`,
		`"Name":"lullabot-proposals"`,
		`"CPUs":8`,
		`"Disk":"100GiB"`,
		`"sandbar_version":"318afb4"`,
		`"created_at":"2026-08-14T19:25:48Z"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the repair dropped %s from the marker: %s", want, got)
		}
	}

	// And the repaired block splices back over the old one in place, so operator
	// text around it in the same Notes field is untouched.
	spliced := spliceDescriptionBlock("Ticket: OPS-441\n\n"+wedged+"\n\nDo not delete.", payload)
	if !strings.Contains(spliced, "Ticket: OPS-441") || !strings.Contains(spliced, "Do not delete.") {
		t.Errorf("the repair disturbed operator text in the description: %s", spliced)
	}
	if strings.Contains(spliced, `"provisioning":true`) {
		t.Errorf("the wedged payload survived the splice: %s", spliced)
	}
	// Re-decoding what we would write back must yield a ready marker: the repair
	// has to survive its own round trip through the host, not just look right.
	back, ok := decodeProvenanceBlock(spliced)
	if !ok {
		t.Fatal("the repaired description no longer decodes")
	}
	if back.Provisioning || back.BuildAbandoned(time.Now()) {
		t.Errorf("the repaired marker still reads as building: %+v", back)
	}
}

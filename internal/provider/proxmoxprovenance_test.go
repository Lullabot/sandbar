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

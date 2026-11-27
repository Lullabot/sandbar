package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/provision"
)

// The base template is server-side state every controller of a pool shares, so
// the version it was built from has to be readable from the template itself.
// These tests drive the stateful fake below through the cases where a
// controller's local stamp file and the template's own record disagree or are
// missing: a second workstation, a wiped state directory, a template built by an
// older sandbar.

// baseTemplateFake is a stateful stand-in for the pool's base template (VMID
// 100). It models what the stamp logic observes: whether the template exists,
// its description and tags, how many DELETEs reached it and what config writes
// were sent. A DELETE removes the template together with its description, and
// the template conversion makes it exist, like the real API.
type baseTemplateFake struct {
	mu      sync.Mutex
	exists  bool
	desc    string
	tags    string
	deletes int
	puts    []url.Values
}

func (f *baseTemplateFake) snapshot() (exists bool, desc string, deletes int, puts []url.Values) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exists, f.desc, f.deletes, append([]url.Values(nil), f.puts...)
}

// newBaseTemplateFake wires the cold-build fixture to a template with the given
// state. VMID 100 is both the existing template and what a rebuild allocates
// (nextid starts at 99), so a rebuild after a delete is traceable end to end.
func newBaseTemplateFake(t *testing.T, exists bool, desc, tags string) (*pveMock, *createRecorder, *baseTemplateFake) {
	t.Helper()
	m := newPVEMock(t)
	rec := &createRecorder{}
	registerBaseBuild(m, rec)
	f := &baseTemplateFake{exists: exists, desc: desc, tags: tags}

	m.on("/cluster/resources", func(w http.ResponseWriter, _ *http.Request) {
		ok, _, _, _ := f.snapshot()
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"vmid":100,"name":"sandbar-base","node":"pve1","pool":"sandbar","status":"stopped","type":"qemu","template":1}]}`))
	})
	m.on("/nodes/pve1/qemu/100", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			f.mu.Lock()
			f.deletes++
			f.exists = false
			f.desc, f.tags = "", ""
			f.mu.Unlock()
		}
		upidData(w, testUPID)
	})
	m.on("/nodes/pve1/qemu/100/template", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.exists = true
		f.mu.Unlock()
		upidData(w, testUPID)
	})
	m.on("/nodes/pve1/qemu/100/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			_ = r.ParseForm()
			f.mu.Lock()
			f.puts = append(f.puts, r.PostForm)
			if d, ok := r.PostForm["description"]; ok {
				f.desc = d[0]
			}
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"data":null}`))
			return
		}
		f.mu.Lock()
		body := `{"net0":"virtio=` + testMAC + `,bridge=vmbr0"`
		if f.desc != "" {
			body += `,"description":` + strconv.Quote(f.desc)
		}
		if f.tags != "" {
			body += `,"tags":` + strconv.Quote(f.tags)
		}
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"data":` + body + `}}`))
	})
	// VMID allocation follows the pool: the base takes 100 whenever it does not
	// exist (first build, or a rebuild after a delete), and clones take 101 up.
	nextClone := 101
	m.on("/cluster/nextid", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		id := 100
		if f.exists {
			id = nextClone
			nextClone++
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"` + strconv.Itoa(id) + `"}`))
	})
	registerVM(m, rec, 102)
	return m, rec, f
}

// stampOf extracts the base revision a description block records.
func stampOf(desc string) string {
	if pv, ok := decodeProvenanceBlock(desc); ok {
		return pv.BaseRevision
	}
	return ""
}

func createOn(t *testing.T, p *proxmoxProvider, opts provision.CreateOptions) (revision string, revisionSeen bool, out string) {
	t.Helper()
	var buf bytes.Buffer
	opts.OnSourceRevision = func(v string) { revision, revisionSeen = v, true }
	cfg := webConfig()
	cfg.Name = "api"
	if err := p.Create(context.Background(), cfg, opts, &buf); err != nil {
		t.Fatalf("Create: %v\n%s", err, buf.String())
	}
	return revision, revisionSeen, buf.String()
}

// A second controller, with an empty state directory, must reuse the template
// the first controller built and stamped. This is the multi-laptop topology the
// provenance design supports: no controller's local files may decide that a
// shared template is stale.
func TestProxmoxSecondControllerReusesCurrentBaseTemplate(t *testing.T) {
	m, _, f := newBaseTemplateFake(t, false, "", "")

	// Controller A: cold pool, builds the template and stamps it in both places.
	a := newCreateProvider(t, m)
	cfg := webConfig()
	if err := a.Create(context.Background(), cfg, provision.CreateOptions{}, nil); err != nil {
		t.Fatalf("controller A create: %v", err)
	}
	want, err := a.templateVersion(cfg)
	if err != nil {
		t.Fatal(err)
	}
	exists, desc, deletes, _ := f.snapshot()
	if !exists || deletes != 0 {
		t.Fatalf("controller A left exists=%v deletes=%d; want a built template", exists, deletes)
	}
	if got := stampOf(desc); got != want {
		t.Fatalf("the template's own record = %q; want %q", got, want)
	}

	// Controller B: same pool, fresh empty XDG_STATE_HOME, so no local stamp.
	m.reset()
	b := newCreateProvider(t, m)
	if got := provision.ReadBaseVersion(b.files, "sandbar-base"); got != "" {
		t.Fatalf("controller B unexpectedly has a local stamp %q", got)
	}
	revision, seen, out := createOn(t, b, provision.CreateOptions{})
	t.Logf("controller B API requests: %v", m.seen())

	if _, _, deletes, _ := f.snapshot(); deletes != 0 {
		t.Errorf("controller B destroyed the current base template; output:\n%s", out)
	}
	if m.sawPath("/cluster/nextid") && m.count("/nodes/pve1/qemu") > 0 {
		t.Errorf("controller B rebuilt the template: requests %v", m.seen())
	}
	if !seen || revision != want {
		t.Errorf("clone's recorded base revision = %q (seen %v); want the template's %q", revision, seen, want)
	}
	if got := provision.ReadBaseVersion(b.files, "sandbar-base"); got != want {
		t.Errorf("controller B's local cache = %q; want it refreshed from the template to %q", got, want)
	}
}

// A different version on the template is the one thing that rebuilds it, and the
// rebuild records the new version on the template.
func TestProxmoxBaseTemplateWithDifferentServerStampIsRebuilt(t *testing.T) {
	old := stampBlockFor(t, "an-older-image"+templateGeneration)
	m, _, f := newBaseTemplateFake(t, true, old, "")
	p := newCreateProvider(t, m)
	// A current local stamp must not outvote the template's own record.
	want, _ := p.templateVersion(webConfig())
	if err := provision.WriteBaseVersion(p.files, "sandbar-base", want, time.Now()); err != nil {
		t.Fatal(err)
	}

	_, _, out := createOn(t, p, provision.CreateOptions{})
	exists, desc, deletes, _ := f.snapshot()
	if deletes != 1 || !exists {
		t.Errorf("deletes=%d exists=%v; want the stale template destroyed once and rebuilt\n%s", deletes, exists, out)
	}
	if got := stampOf(desc); got != want {
		t.Errorf("rebuilt template's record = %q; want %q", got, want)
	}
}

// A template this workstation built with an older sandbar has only a local
// stamp. A current one is trusted and copied onto the template for the other
// controllers; nothing is destroyed.
func TestProxmoxBaseTemplateBackfillsServerStampFromCurrentLocalStamp(t *testing.T) {
	m, _, f := newBaseTemplateFake(t, true, "", "")
	p := newCreateProvider(t, m)
	want, _ := p.templateVersion(webConfig())
	if err := provision.WriteBaseVersion(p.files, "sandbar-base", want, time.Now()); err != nil {
		t.Fatal(err)
	}

	revision, _, out := createOn(t, p, provision.CreateOptions{})
	_, desc, deletes, _ := f.snapshot()
	if deletes != 0 {
		t.Errorf("a current locally-stamped template was destroyed\n%s", out)
	}
	if got := stampOf(desc); got != want {
		t.Errorf("template's record after backfill = %q; want %q", got, want)
	}
	if revision != want {
		t.Errorf("clone's base revision = %q; want %q", revision, want)
	}
}

// A template with no stamp anywhere has an unknown version. It is used as it is:
// not destroyed, not stamped (a stamp would claim a version nobody verified),
// the clone's base revision stays empty, and the user is told how to refresh it.
func TestProxmoxBaseTemplateWithNoStampIsUsedAsUnknown(t *testing.T) {
	m, _, f := newBaseTemplateFake(t, true, "", "")
	p := newCreateProvider(t, m)

	revision, seen, out := createOn(t, p, provision.CreateOptions{})
	_, desc, deletes, puts := f.snapshot()
	if deletes != 0 {
		t.Errorf("an unstamped template was destroyed\n%s", out)
	}
	if len(puts) != 0 || desc != "" {
		t.Errorf("an unstamped template was written to: puts=%v description=%q", puts, desc)
	}
	if got := provision.ReadBaseVersion(p.files, "sandbar-base"); got != "" {
		t.Errorf("a local stamp %q was written for a template this run did not build", got)
	}
	if !seen || revision != "" {
		t.Errorf("clone's base revision = %q (seen %v); want it recorded as unknown (empty)", revision, seen)
	}
	if !strings.Contains(out, "unknown to this workstation") || !strings.Contains(out, "--rebuild") {
		t.Errorf("missing the unknown-version notice; output:\n%s", out)
	}
}

// --rebuild destroys and rebuilds even a template whose record is current.
func TestProxmoxRebuildDestroysCurrentBaseTemplate(t *testing.T) {
	p0 := newProxmoxForTest(t, newPVEMock(t))
	want, _ := p0.templateVersion(webConfig())
	m, _, f := newBaseTemplateFake(t, true, stampBlockFor(t, want), "")
	p := newCreateProvider(t, m)

	_, _, out := createOn(t, p, provision.CreateOptions{Rebuild: true})
	exists, desc, deletes, _ := f.snapshot()
	if deletes != 1 || !exists {
		t.Errorf("deletes=%d exists=%v; want the template destroyed and rebuilt\n%s", deletes, exists, out)
	}
	if got := stampOf(desc); got != want {
		t.Errorf("rebuilt template's record = %q; want %q", got, want)
	}
}

// Operator-written description text and tags survive the stamp write, and the
// write touches the description key alone: no sandbar tag, so the template does
// not become a board tile.
func TestProxmoxBaseStampKeepsOperatorDescriptionAndTags(t *testing.T) {
	const note = "Owned by the platform team.\nDo not delete."
	m, _, f := newBaseTemplateFake(t, true, note, "platform;golden")
	p := newCreateProvider(t, m)
	want, _ := p.templateVersion(webConfig())
	if err := provision.WriteBaseVersion(p.files, "sandbar-base", want, time.Now()); err != nil {
		t.Fatal(err)
	}

	createOn(t, p, provision.CreateOptions{})
	_, desc, _, puts := f.snapshot()
	if !strings.HasPrefix(desc, note) {
		t.Errorf("operator text was altered: %q", desc)
	}
	if got := stampOf(desc); got != want {
		t.Errorf("record = %q; want %q", got, want)
	}
	if len(puts) != 1 {
		t.Fatalf("config writes to the template = %v; want exactly one", puts)
	}
	for key := range puts[0] {
		if key != "description" {
			t.Errorf("stamp write sent %q; it must send the description alone (tags are the operator's)", key)
		}
	}
	if f.tags != "platform;golden" {
		t.Errorf("tags = %q; want them untouched", f.tags)
	}
}

// stampBlockFor renders a description holding only a base-version block.
func stampBlockFor(t *testing.T, version string) string {
	t.Helper()
	p := Provenance{SchemaVersion: MarkerSchemaVersion, Base: "sandbar-base", BaseRevision: version}
	p.Config.Name = "sandbar-base"
	payload, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return spliceDescriptionBlock("", payload)
}

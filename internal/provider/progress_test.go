package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/vm"
)

type progressBarrierFiles struct {
	lima.HostFiles
	pauseRead  atomic.Bool
	entered    chan struct{}
	release    chan struct{}
	readyWrite chan struct{}
}

func (f *progressBarrierFiles) ReadFile(path string) ([]byte, error) {
	b, err := f.HostFiles.ReadFile(path)
	if f.pauseRead.Swap(false) {
		close(f.entered)
		<-f.release
	}
	return b, err
}

func (f *progressBarrierFiles) WriteFile(path string, data []byte, dirMode, fileMode fs.FileMode) error {
	var marker Provenance
	if json.Unmarshal(data, &marker) == nil && !marker.Provisioning {
		select {
		case f.readyWrite <- struct{}{}:
		default:
		}
	}
	return f.HostFiles.WriteFile(path, data, dirMode, fileMode)
}

func TestLimaProgressCannotEraseRevisions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIMA_HOME", home)
	p := &limaProvider{hostFiles: lima.LocalFiles()}
	ctx := context.Background()
	cfg := vm.CreateConfig{Name: "web", BaseName: "base"}
	progress := NewProvenance(cfg, true)
	progress.Progress = BuildProgress{Role: "project", Index: 3, Total: 10}
	if err := p.MarkProgress(ctx, cfg.Name, progress); !errors.Is(err, ErrNoInstance) {
		t.Fatalf("progress before clone = %v, want ErrNoInstance", err)
	}
	if _, err := os.Stat(filepath.Join(home, cfg.Name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("early progress created an instance directory: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, cfg.Name), 0o700); err != nil {
		t.Fatal(err)
	}
	inFlight := NewProvenance(cfg, true)
	inFlight.BaseRevision = "base-v1"
	if err := p.MarkManaged(ctx, cfg.Name, inFlight); err != nil {
		t.Fatal(err)
	}
	if err := p.MarkProgress(ctx, cfg.Name, progress); err != nil {
		t.Fatal(err)
	}
	got, ok, err := p.ProvenanceOf(ctx, cfg.Name)
	if err != nil || !ok {
		t.Fatalf("progress marker: ok=%v err=%v", ok, err)
	}
	if !got.Provisioning || got.BaseRevision != "base-v1" || got.Progress != progress.Progress {
		t.Fatalf("progress lost actual source revision: %+v", got)
	}
	ready := NewProvenance(cfg, false)
	ready.BaseRevision, ready.SetupRevision = "base-v1", "setup-v1"
	if err := p.MarkManaged(ctx, cfg.Name, ready); err != nil {
		t.Fatal(err)
	}
	if err := p.MarkProgress(ctx, cfg.Name, progress); err != nil {
		t.Fatal(err)
	}
	got, ok, err = p.ProvenanceOf(ctx, cfg.Name)
	if err != nil || !ok || got != ready {
		t.Fatalf("late progress overwrote successful marker: got=%+v ok=%v err=%v", got, ok, err)
	}
	// An explicit reset still needs to replace the ready marker with a new build.
	if err := p.MarkManaged(ctx, cfg.Name, inFlight); err != nil {
		t.Fatal(err)
	}
	got, _, _ = p.ProvenanceOf(ctx, cfg.Name)
	if !got.Provisioning || got.BaseRevision != "base-v1" {
		t.Fatalf("new clone could not enter in-flight state: %+v", got)
	}
}

func TestProxmoxProgressCannotEraseRevisions(t *testing.T) {
	m, configs := newStatefulConfigMock(t, 100)
	m.data("/cluster/resources", clusterResources)
	m.data("/nodes/pve1/qemu/100/status/current", `{"vmid":100,"name":"web","status":"running"}`)
	p := newProxmoxForTest(t, m)
	ctx := context.Background()
	cfg := vm.CreateConfig{Name: "web", BaseName: "base"}
	progress := NewProvenance(cfg, true)
	progress.Progress = BuildProgress{Role: "project", Index: 3, Total: 10}
	if err := p.MarkProgress(ctx, "missing", progress); !errors.Is(err, ErrNoInstance) {
		t.Fatalf("progress before VM exists = %v, want ErrNoInstance", err)
	}
	inFlight := NewProvenance(cfg, true)
	inFlight.BaseRevision = "base-v1"
	if err := p.MarkManaged(ctx, cfg.Name, inFlight); err != nil {
		t.Fatal(err)
	}
	if err := p.MarkProgress(ctx, cfg.Name, progress); err != nil {
		t.Fatal(err)
	}
	got, ok, err := p.ProvenanceOf(ctx, cfg.Name)
	if err != nil || !ok || got.BaseRevision != "base-v1" || got.Progress != progress.Progress {
		t.Fatalf("progress lost source revision in PVE description: got=%+v ok=%v err=%v", got, ok, err)
	}
	ready := NewProvenance(cfg, false)
	ready.BaseRevision, ready.SetupRevision = "base-v1", "setup-v1"
	if err := p.MarkManaged(ctx, cfg.Name, ready); err != nil {
		t.Fatal(err)
	}
	if err := p.MarkProgress(ctx, cfg.Name, progress); err != nil {
		t.Fatal(err)
	}
	got, ok, err = p.ProvenanceOf(ctx, cfg.Name)
	if err != nil || !ok || got != ready {
		t.Fatalf("late progress overwrote PVE ready marker: got=%+v ok=%v err=%v", got, ok, err)
	}
	if configs.get(100)["description"] == "" {
		t.Fatal("PVE marker did not reach config")
	}
	if err := p.MarkManaged(ctx, cfg.Name, inFlight); err != nil {
		t.Fatal(err)
	}
	got, _, _ = p.ProvenanceOf(ctx, cfg.Name)
	if !got.Provisioning || got.BaseRevision != "base-v1" {
		t.Fatalf("new clone could not enter in-flight state: %+v", got)
	}
}

func TestLimaProgressAndCompletionWritesSerialize(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIMA_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "web"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := &progressBarrierFiles{HostFiles: lima.LocalFiles(), entered: make(chan struct{}), release: make(chan struct{}), readyWrite: make(chan struct{}, 1)}
	p := &limaProvider{hostFiles: files}
	ctx := context.Background()
	cfg := vm.CreateConfig{Name: "web", BaseName: "base"}
	inFlight := NewProvenance(cfg, true)
	inFlight.BaseRevision = "actual-base"
	if err := p.MarkManaged(ctx, cfg.Name, inFlight); err != nil {
		t.Fatal(err)
	}
	ready := NewProvenance(cfg, false)
	ready.BaseRevision, ready.SetupRevision = "actual-base", "actual-setup"
	files.pauseRead.Store(true)
	progressDone := make(chan error, 1)
	go func() { progressDone <- p.MarkProgress(ctx, cfg.Name, NewProvenance(cfg, true)) }()
	<-files.entered // progress has read the old in-flight marker but has not written it
	readyDone := make(chan error, 1)
	go func() { readyDone <- p.MarkManaged(ctx, cfg.Name, ready) }()
	// A completion write observed while progress holds its read barrier means
	// the two read/write transactions were allowed to interleave.
	interleaved := false
	select {
	case <-files.readyWrite:
		interleaved = true
	case <-time.After(50 * time.Millisecond):
	}
	close(files.release)
	if err := <-progressDone; err != nil {
		t.Fatal(err)
	}
	if err := <-readyDone; err != nil {
		t.Fatal(err)
	}
	if interleaved {
		t.Fatal("completion wrote while progress read-modify-write was in flight")
	}
	got, ok, err := p.ProvenanceOf(ctx, cfg.Name)
	if err != nil || !ok || got != ready {
		t.Fatalf("interleaved progress erased successful marker: got=%+v ok=%v err=%v", got, ok, err)
	}
}

func TestProxmoxProgressAndCompletionWritesSerialize(t *testing.T) {
	m, _ := newStatefulConfigMock(t, 100)
	m.data("/cluster/resources", clusterResources)
	m.data("/nodes/pve1/qemu/100/status/current", `{"vmid":100,"name":"web","status":"running"}`)
	p := newProxmoxForTest(t, m)
	ctx := context.Background()
	cfg := vm.CreateConfig{Name: "web", BaseName: "base"}
	inFlight := NewProvenance(cfg, true)
	inFlight.BaseRevision = "actual-base"
	if err := p.MarkManaged(ctx, cfg.Name, inFlight); err != nil {
		t.Fatal(err)
	}
	ready := NewProvenance(cfg, false)
	ready.BaseRevision, ready.SetupRevision = "actual-base", "actual-setup"

	path := "/nodes/pve1/qemu/100/config"
	m.mu.Lock()
	original := m.routes["/api2/json"+path]
	m.mu.Unlock()
	var pauseRead atomic.Bool
	pauseRead.Store(true)
	entered := make(chan struct{})
	release := make(chan struct{})
	readyWrite := make(chan struct{}, 1)
	m.on(path, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && pauseRead.Swap(false) {
			// Capture the old response, then pause delivery. A concurrent ready
			// write cannot make this pending progress read see the new marker.
			recorded := httptest.NewRecorder()
			original(recorded, r)
			close(entered)
			<-release
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(recorded.Code)
			_, _ = w.Write(recorded.Body.Bytes())
			return
		}
		if r.Method == http.MethodPut {
			_ = r.ParseForm()
			marker, ok := decodeProvenanceBlock(r.PostForm.Get("description"))
			if ok && !marker.Provisioning {
				select {
				case readyWrite <- struct{}{}:
				default:
				}
			}
		}
		original(w, r)
	})
	progressDone := make(chan error, 1)
	go func() { progressDone <- p.MarkProgress(ctx, cfg.Name, NewProvenance(cfg, true)) }()
	<-entered
	readyDone := make(chan error, 1)
	go func() { readyDone <- p.MarkManaged(ctx, cfg.Name, ready) }()
	interleaved := false
	select {
	case <-readyWrite:
		interleaved = true
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-progressDone; err != nil {
		t.Fatal(err)
	}
	if err := <-readyDone; err != nil {
		t.Fatal(err)
	}
	if interleaved {
		t.Fatal("PVE completion wrote while progress read-modify-write was in flight")
	}
	got, ok, err := p.ProvenanceOf(ctx, cfg.Name)
	if err != nil || !ok || got != ready {
		t.Fatalf("interleaved PVE progress erased ready marker: got=%+v ok=%v err=%v", got, ok, err)
	}
}

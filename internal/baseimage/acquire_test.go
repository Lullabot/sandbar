package baseimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
)

type testStore struct {
	lima.ImageStore
	root string
}

func (s testStore) LimaHome() string { return s.root }

func TestAcquireVerifiesAndCachesOnLimaHost(t *testing.T) {
	const payload = "trusted qcow2 fixture"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()
	sha := sha256.Sum256([]byte(payload))
	manifest := Manifest{Version: "test-release", Images: map[string]ImageEntry{"amd64": {
		URL: server.URL, Filename: "image.qcow2", SHA256: hex.EncodeToString(sha[:]), Size: int64(len(payload)),
	}}}
	store := testStore{ImageStore: lima.LocalFiles().(lima.ImageStore), root: t.TempDir()}
	var progress bytes.Buffer
	path, err := Acquire(context.Background(), store, manifest, "amd64", server.Client(), &progress)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != payload {
		t.Fatalf("cached data = %q, %v", data, err)
	}
	if !strings.Contains(progress.String(), "downloading base image") {
		t.Fatalf("missing progress: %q", progress.String())
	}
	if _, err := Acquire(context.Background(), store, manifest, "amd64", server.Client(), &progress); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("cache hit made %d HTTP requests; want 1", got)
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(context.Background(), store, manifest, "amd64", server.Client(), &progress); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("corrupt cache made %d HTTP requests; want 2", got)
	}
	if filepath.Dir(path) != filepath.Join(store.root, "_sand", "images", manifest.Version) {
		t.Fatalf("cache path = %q", path)
	}
}

func TestAcquireRejectsBadDownloadWithoutPublishing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("bad"))
	}))
	defer server.Close()
	sha := sha256.Sum256([]byte("good"))
	manifest := Manifest{Version: "test-release", Images: map[string]ImageEntry{"amd64": {
		URL: server.URL, Filename: "image.qcow2", SHA256: hex.EncodeToString(sha[:]), Size: 3,
	}}}
	store := testStore{ImageStore: lima.LocalFiles().(lima.ImageStore), root: t.TempDir()}
	path, err := Acquire(context.Background(), store, manifest, "amd64", server.Client(), nil)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("Acquire = %q, %v; want digest failure", path, err)
	}
	files, err := filepath.Glob(filepath.Join(store.root, "_sand", "images", manifest.Version, "*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("failed download left files %v, %v", files, err)
	}
}

func TestAcquireRejectsTruncatedDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("short"))
	}))
	defer server.Close()
	sha := sha256.Sum256([]byte("short"))
	manifest := Manifest{Version: "test-release", Images: map[string]ImageEntry{"amd64": {
		URL: server.URL, Filename: "image.qcow2", SHA256: hex.EncodeToString(sha[:]), Size: 10,
	}}}
	store := testStore{ImageStore: lima.LocalFiles().(lima.ImageStore), root: t.TempDir()}
	if _, err := Acquire(context.Background(), store, manifest, "amd64", server.Client(), nil); err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("truncated download error = %v", err)
	}
	files, err := filepath.Glob(filepath.Join(store.root, "_sand", "images", manifest.Version, "*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("truncated download left files %v, %v", files, err)
	}
}

// Run explicitly for the release smoke check; the normal suite stays offline.
func TestPublishedImageAcquisitionSmoke(t *testing.T) {
	if os.Getenv("SAND_BASE_IMAGE_SMOKE") != "1" {
		t.Skip("set SAND_BASE_IMAGE_SMOKE=1 to download the published image")
	}
	store := testStore{ImageStore: lima.LocalFiles().(lima.ImageStore), root: t.TempDir()}
	start := time.Now()
	var first bytes.Buffer
	path, err := Acquire(context.Background(), store, PinnedManifest, "amd64", nil, &first)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("first acquisition %s: %s", time.Since(start), strings.TrimSpace(first.String()))
	start = time.Now()
	var second bytes.Buffer
	if _, err := Acquire(context.Background(), store, PinnedManifest, "amd64", nil, &second); err != nil {
		t.Fatal(err)
	}
	t.Logf("second acquisition %s: %s", time.Since(start), strings.TrimSpace(second.String()))
	if !strings.Contains(second.String(), "cache hit") {
		t.Fatalf("second acquisition missed cache: %q", second.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

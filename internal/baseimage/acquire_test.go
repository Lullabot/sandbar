package baseimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
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

type failingImageStore struct {
	lima.ImageStore
	root                         string
	statErr, removeErr, writeErr error
	shaErr, renameErr            error
	shaValue                     string
}

func (s failingImageStore) LimaHome() string { return s.root }

func (s failingImageStore) Stat(path string) (fs.FileInfo, error) {
	if s.statErr != nil {
		return nil, s.statErr
	}
	return s.ImageStore.Stat(path)
}

func (s failingImageStore) RemoveAll(path string) error {
	if s.removeErr != nil {
		return s.removeErr
	}
	return s.ImageStore.RemoveAll(path)
}

func (s failingImageStore) WriteStream(ctx context.Context, path string, src io.Reader) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	return s.ImageStore.WriteStream(ctx, path, src)
}

func (s failingImageStore) SHA256(ctx context.Context, path string) (string, error) {
	if s.shaErr != nil {
		return "", s.shaErr
	}
	if s.shaValue != "" {
		return s.shaValue, nil
	}
	return s.ImageStore.SHA256(ctx, path)
}

func (s failingImageStore) Rename(ctx context.Context, oldPath, newPath string) error {
	if s.renameErr != nil {
		return s.renameErr
	}
	return s.ImageStore.Rename(ctx, oldPath, newPath)
}

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

func TestAcquireRejectsInvalidConfigurationBeforeDownload(t *testing.T) {
	sha := strings.Repeat("a", 64)
	valid := Manifest{Version: "release", Images: map[string]ImageEntry{"amd64": {
		URL: "https://example.invalid/image.qcow2", Filename: "image.qcow2", SHA256: sha, Size: 1,
	}}}
	store := testStore{ImageStore: lima.LocalFiles().(lima.ImageStore), root: t.TempDir()}

	tests := []struct {
		name     string
		manifest Manifest
		arch     string
		store    lima.ImageStore
	}{
		{name: "unsupported architecture", manifest: valid, arch: "riscv64", store: store},
		{name: "invalid entry", manifest: Manifest{Version: "release", Images: map[string]ImageEntry{"amd64": {Filename: "../image.qcow2"}}}, arch: "amd64", store: store},
		{name: "invalid version", manifest: Manifest{Version: "../release", Images: valid.Images}, arch: "amd64", store: store},
		{name: "missing Lima home", manifest: valid, arch: "amd64", store: testStore{ImageStore: lima.LocalFiles().(lima.ImageStore)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Acquire(context.Background(), tt.store, tt.manifest, tt.arch, nil, nil); err == nil {
				t.Fatal("Acquire succeeded with invalid configuration")
			}
		})
	}
}

func TestAcquireReportsStorageAndTransportFailures(t *testing.T) {
	const payload = "verified payload"
	digest := sha256.Sum256([]byte(payload))
	manifest := Manifest{Version: "release", Images: map[string]ImageEntry{"amd64": {
		URL: "", Filename: "image.qcow2", SHA256: hex.EncodeToString(digest[:]), Size: int64(len(payload)),
	}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()
	manifest.Images["amd64"] = ImageEntry{URL: server.URL, Filename: "image.qcow2", SHA256: hex.EncodeToString(digest[:]), Size: int64(len(payload))}
	local := lima.LocalFiles().(lima.ImageStore)
	failure := errors.New("injected failure")

	t.Run("checking cache", func(t *testing.T) {
		store := failingImageStore{ImageStore: local, root: t.TempDir(), statErr: failure}
		if _, err := Acquire(context.Background(), store, manifest, "amd64", server.Client(), nil); !errors.Is(err, failure) {
			t.Fatalf("Acquire error = %v, want wrapped %v", err, failure)
		}
	})

	t.Run("writing download", func(t *testing.T) {
		store := failingImageStore{ImageStore: local, root: t.TempDir(), writeErr: failure}
		if _, err := Acquire(context.Background(), store, manifest, "amd64", server.Client(), nil); !errors.Is(err, failure) {
			t.Fatalf("Acquire error = %v, want wrapped %v", err, failure)
		}
	})

	t.Run("hashing downloaded file", func(t *testing.T) {
		store := failingImageStore{ImageStore: local, root: t.TempDir(), shaErr: failure}
		if _, err := Acquire(context.Background(), store, manifest, "amd64", server.Client(), nil); !errors.Is(err, failure) {
			t.Fatalf("Acquire error = %v, want wrapped %v", err, failure)
		}
	})

	t.Run("publishing verified file", func(t *testing.T) {
		store := failingImageStore{ImageStore: local, root: t.TempDir(), renameErr: failure}
		if _, err := Acquire(context.Background(), store, manifest, "amd64", server.Client(), nil); !errors.Is(err, failure) {
			t.Fatalf("Acquire error = %v, want wrapped %v", err, failure)
		}
	})

	t.Run("HTTP status", func(t *testing.T) {
		failureServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		}))
		defer failureServer.Close()
		failedManifest := manifest
		entry := failedManifest.Images["amd64"]
		entry.URL = failureServer.URL
		failedManifest.Images = map[string]ImageEntry{"amd64": entry}
		store := testStore{ImageStore: local, root: t.TempDir()}
		if _, err := Acquire(context.Background(), store, failedManifest, "amd64", failureServer.Client(), nil); err == nil || !strings.Contains(err.Error(), "503") {
			t.Fatalf("Acquire error = %v, want HTTP 503", err)
		}
	})
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

package releasecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStableVersionsAndReleaseLinks(t *testing.T) {
	tests := []struct {
		installed, latest string
		want              bool
	}{
		{"v0.9.0", "v0.10.0", true},
		{"0.12.0", "v0.12.1", true},
		{"v1.0.0", "v0.99.99", false},
		{"v0.12.0", "v0.12.0", false},
		{"dev", "v1.0.0", false},
		{"a1b2c3d", "v1.0.0", false},
		{"v1.0.0-dirty", "v1.0.1", false},
		{"v1.0.0-rc1", "v1.0.1", false},
		{"v1.0.0", "v1.1.0-beta", false},
		{"v1.0.0", "v1.1.0/../../evil", false},
		{"v01.0.0", "v1.1.0", false},
		{"v18446744073709551616.0.0", "v2.0.0", false},
	}
	for _, tt := range tests {
		if got := UpdateAvailable(tt.installed, tt.latest); got != tt.want {
			t.Errorf("UpdateAvailable(%q, %q) = %v, want %v", tt.installed, tt.latest, got, tt.want)
		}
	}
	if got := ReleaseNotesURL("0.12.0"); got != "https://github.com/Lullabot/sandbar/releases/tag/v0.12.0" {
		t.Errorf("release URL = %q", got)
	}
	if got := ReleaseNotesURL("dev"); got != "https://github.com/Lullabot/sandbar/releases" {
		t.Errorf("development URL = %q", got)
	}
	if got := ReleaseNotesURL("v1.0.0/../../evil"); got != "https://github.com/Lullabot/sandbar/releases" {
		t.Errorf("untrusted URL = %q", got)
	}
}

func TestCachePathUsesXDGCacheHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	if got, want := CachePath(), filepath.Join(dir, "sandbar", "release-check.json"); got != want {
		t.Fatalf("CachePath() = %q, want %q", got, want)
	}
}

func TestCachePathWithoutHomeFailsClosed(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "")
	if got := CachePath(); got != "" {
		t.Fatalf("CachePath without XDG cache or home = %q, want empty", got)
	}
}

func TestConcurrentReservationAndDailyThrottle(t *testing.T) {
	entered := make(chan struct{})
	finish := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		close(entered)
		<-finish
		_, _ = w.Write([]byte(`{"tag_name":"v0.13.0","html_url":"https://github.com/Lullabot/sandbar/releases/tag/v0.13.0"}`))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "release-check.json")
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	checker := New(Config{Path: path, Endpoint: server.URL, Now: func() time.Time { return now }})
	first := make(chan Release, 1)
	go func() { first <- checker.Check(context.Background()) }()
	<-entered
	second := New(Config{Path: path, Endpoint: server.URL, Now: func() time.Time { return now }})
	if got := second.Check(context.Background()); got.Tag != "" {
		t.Fatalf("second process saw an unpublished result: %+v", got)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("concurrent checks made %d requests, want 1", got)
	}
	close(finish)
	if got := <-first; got.Tag != "v0.13.0" || got.NotesURL != "https://github.com/Lullabot/sandbar/releases/tag/v0.13.0" {
		t.Fatalf("validated release = %+v", got)
	}
	if got := second.Check(context.Background()); got.Tag != "v0.13.0" {
		t.Fatalf("cached release = %+v", got)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("same-day checks made %d requests", got)
	}
}

func TestFailedAttemptConsumesWindowAndKeepsRelease(t *testing.T) {
	var requests atomic.Int32
	response := `{"tag_name":"v0.13.0"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if response == "failure" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(response))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "release-check.json")
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	checker := New(Config{Path: path, Endpoint: server.URL, Now: func() time.Time { return now }})
	if got := checker.Check(context.Background()); got.Tag != "v0.13.0" {
		t.Fatalf("initial release = %+v", got)
	}
	response = "failure"
	now = now.Add(24 * time.Hour)
	if got := checker.Check(context.Background()); got.Tag != "v0.13.0" {
		t.Fatalf("failed refresh lost valid release: %+v", got)
	}
	if got := checker.Check(context.Background()); got.Tag != "v0.13.0" {
		t.Fatalf("same-day cached release = %+v", got)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("failed attempt retried: %d requests", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cache missing: %v", err)
	}
}

func TestCorruptAndUnwritableCacheFailQuietly(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"tag_name":"v1.0.0"}`))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "release-check.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	checker := New(Config{Path: path, Endpoint: server.URL})
	if got := checker.Cached(); got.Tag != "" {
		t.Fatalf("corrupt cache yielded %+v", got)
	}
	if got := checker.Check(context.Background()); got.Tag != "v1.0.0" {
		t.Fatalf("corrupt cache did not recover: %+v", got)
	}
	badPath := filepath.Join(t.TempDir(), "occupied", "release-check.json")
	if err := os.WriteFile(filepath.Dir(badPath), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := New(Config{Path: badPath, Endpoint: server.URL}).Check(context.Background()); got.Tag != "" {
		t.Fatalf("unwritable cache yielded %+v", got)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("unwritable cache requested network: %d", got)
	}
	blockedRename := filepath.Join(t.TempDir(), "release-check.json")
	if err := os.Mkdir(blockedRename, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := New(Config{Path: blockedRename, Endpoint: server.URL}).Check(context.Background()); got.Tag != "" {
		t.Fatalf("failed reservation write yielded %+v", got)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("failed reservation write requested network: %d", got)
	}
}

func TestRejectsUnsafeOrUnstableAPIRelease(t *testing.T) {
	for _, body := range []string{
		`{"tag_name":"v1.0.0-rc1"}`,
		`{"tag_name":"v1.0.0","draft":true}`,
		`{"tag_name":"v1.0.0","prerelease":true}`,
		`{"tag_name":"v1.0.0/../../evil"}`,
		`{"tag_name":"v1.0.0","html_url":"https://evil.example/"}`,
		`{"tag_name":"v1.0.0","html_url":"http://github.com/Lullabot/sandbar/releases/tag/v1.0.0"}`,
		`{"tag_name":"v1.0.0","html_url":"https://github.com/elsewhere/sandbar/releases/tag/v1.0.0"}`,
		`{"tag_name":"v1.0.0"} trailing garbage`,
		`broken json`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			got := New(Config{Path: filepath.Join(t.TempDir(), "release-check.json"), Endpoint: server.URL}).Check(context.Background())
			if got.Tag != "" || got.NotesURL != "" {
				t.Fatalf("unsafe API release accepted: %+v", got)
			}
		})
	}
}

func TestOlderCompletionCannotReplaceNewerReservation(t *testing.T) {
	firstEntered := make(chan struct{})
	finishFirst := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(firstEntered)
			<-finishFirst
			_, _ = w.Write([]byte(`{"tag_name":"v0.13.0"}`))
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"v0.14.0"}`))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "release-check.json")
	var clockMu sync.Mutex
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	checker := New(Config{Path: path, Endpoint: server.URL, Now: clock})
	first := make(chan Release, 1)
	go func() { first <- checker.Check(context.Background()) }()
	<-firstEntered
	clockMu.Lock()
	now = now.Add(25 * time.Hour)
	clockMu.Unlock()
	if got := checker.Check(context.Background()); got.Tag != "v0.14.0" {
		t.Fatalf("newer reservation fetched %+v", got)
	}
	close(finishFirst)
	if got := <-first; got.Tag != "v0.14.0" {
		t.Fatalf("older completion replaced newer result: %+v", got)
	}
	if got := checker.Cached(); got.Tag != "v0.14.0" {
		t.Fatalf("cache rolled back: %+v", got)
	}
}

type deadlineTransport struct{}

func (deadlineTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	<-req.Context().Done()
	return nil, req.Context().Err()
}

func TestTimeoutAndLockFailureStayQuiet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "release-check.json")
	checker := New(Config{
		Path: path, Client: &http.Client{Transport: deadlineTransport{}},
		Timeout: 20 * time.Millisecond,
	})
	start := time.Now()
	if got := checker.Check(context.Background()); got.Tag != "" {
		t.Fatalf("timed-out check yielded %+v", got)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("request exceeded timeout bound: %s", elapsed)
	}
	blocked := New(Config{Path: filepath.Join(t.TempDir(), "release-check.json"), AcquireLock: func(string) (func(), bool) {
		return nil, false
	}, Client: &http.Client{Transport: deadlineTransport{}}})
	if got := blocked.Check(context.Background()); got.Tag != "" {
		t.Fatalf("failed lock yielded %+v", got)
	}
}

func TestCacheRejectsUntrustedStoredURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "release-check.json")
	for _, content := range []string{
		`{"release":{"tag":"v1.0.0","notes_url":"https://evil.example/release"}}`,
		`{"release":{"tag":"","notes_url":"https://evil.example/release"}}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := New(Config{Path: path}).Cached(); got.Tag != "" || got.NotesURL != "" {
			t.Fatalf("untrusted cached URL accepted: %+v", got)
		}
	}
}

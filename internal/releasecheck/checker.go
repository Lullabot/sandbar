// Package releasecheck performs Sand's optional, once-daily GitHub release
// lookup. The cache is advisory; failures never interrupt the TUI.
package releasecheck

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lullabot/sandbar/internal/statelock"
)

const (
	defaultEndpoint = "https://api.github.com/repos/Lullabot/sandbar/releases/latest"
	checkInterval   = 24 * time.Hour
	requestTimeout  = 5 * time.Second
)

// Release contains only a validated stable tag and a trusted notes URL.
type Release struct {
	Tag      string `json:"tag"`
	NotesURL string `json:"notes_url"`
}

type cache struct {
	LastAttempt time.Time `json:"last_attempt"`
	Release     Release   `json:"release"`
}

// Config provides seams for local HTTP, clock, cache, and lock tests. Zero
// values select the production defaults.
type Config struct {
	Path        string
	Endpoint    string
	Client      *http.Client
	Now         func() time.Time
	AcquireLock func(path string) (release func(), ok bool)
	Timeout     time.Duration
}

type Checker struct {
	config Config
}

// CachePath is the disposable release cache's default location.
func CachePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "sandbar", "release-check.json")
}

func New(config Config) *Checker {
	if config.Path == "" {
		config.Path = CachePath()
	}
	if config.Endpoint == "" {
		config.Endpoint = defaultEndpoint
	}
	if config.Client == nil {
		config.Client = http.DefaultClient
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.AcquireLock == nil {
		config.AcquireLock = statelock.AcquireStrict
	}
	if config.Timeout <= 0 {
		config.Timeout = requestTimeout
	}
	return &Checker{config: config}
}

// Cached reads only local state. It is safe to use during model construction.
func (c *Checker) Cached() Release {
	return readCache(c.config.Path).Release
}

// Check reserves a due attempt before networking. Call it from an asynchronous
// startup command; it deliberately returns only validated cached metadata and
// never propagates optional lookup or cache errors.
func (c *Checker) Check(ctx context.Context) Release {
	path := c.config.Path
	if path == "" || os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return c.Cached()
	}
	release, ok := c.config.AcquireLock(path)
	if !ok {
		return c.Cached()
	}
	state := readCache(path)
	now := c.config.Now().UTC()
	if !state.LastAttempt.IsZero() && now.Sub(state.LastAttempt) < checkInterval {
		release()
		return state.Release
	}
	state.LastAttempt = now
	if err := writeCache(path, state); err != nil {
		release()
		return state.Release
	}
	release()

	latest, valid := c.fetch(ctx)
	if !valid {
		return c.Cached()
	}
	release, ok = c.config.AcquireLock(path)
	if !ok {
		return c.Cached()
	}
	defer release()
	state = readCache(path)
	if !state.LastAttempt.Equal(now) {
		return state.Release // A newer reservation owns the cache now.
	}
	state.Release = latest
	if err := writeCache(path, state); err != nil {
		return readCache(path).Release
	}
	return latest
}

func (c *Checker) fetch(ctx context.Context) (Release, bool) {
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.config.Endpoint, nil)
	if err != nil {
		return Release{}, false
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "sandbar-release-check")
	resp, err := c.config.Client.Do(req)
	if err != nil {
		return Release{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, false
	}
	var result struct {
		TagName    string `json:"tag_name"`
		HTMLURL    string `json:"html_url"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if err != nil || len(body) > 64<<10 || json.Unmarshal(body, &result) != nil {
		return Release{}, false
	}
	url := tagNotesURL(result.TagName)
	if result.Draft || result.Prerelease || url == "" || (result.HTMLURL != "" && !trustedNotesURL(result.HTMLURL, result.TagName)) {
		return Release{}, false
	}
	return Release{Tag: result.TagName, NotesURL: url}, true
}

func trustedNotesURL(raw, tag string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.Port() != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return parsed.EscapedPath() == "/Lullabot/sandbar/releases/tag/"+tag
}

func readCache(path string) cache {
	var state cache
	if path == "" {
		return state
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &state) != nil {
		return cache{}
	}
	if state.Release.NotesURL != tagNotesURL(state.Release.Tag) {
		state.Release = Release{}
	}
	return state
}

func writeCache(path string, state cache) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".release-check-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

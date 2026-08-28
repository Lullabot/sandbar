package drupalorg

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient starts an httptest.Server running handler, builds a Client
// pointed at it, and registers the server's cleanup — the same six lines
// every test in this package would otherwise repeat, since New only ever
// fails on an unparseable BaseURL, which server.URL never is.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	c, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// TestClient_URLEscaping feeds a hostile project path — containing "/",
// "..", and shell metacharacters — through Project (which builds a request
// URL from it) and asserts the *request path the server actually received*
// is a single, safely percent-escaped segment. This is the boundary the
// plan calls out explicitly: guest-derived text must never be concatenated
// into a URL (or a command string — there is none here, this package never
// shells out) in a way that lets it alter the request's structure.
func TestClient_URLEscaping(t *testing.T) {
	const hostile = `issue/drupal-3181657/../../etc/passwd; rm -rf $(whoami)`

	var gotPath, gotRawPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRawPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":1,"default_branch":"main"}`))
	})

	if _, err := c.Project(context.Background(), hostile); err != nil {
		t.Fatalf("Project: %v", err)
	}

	// The escaped request path must contain exactly one "/projects/<blob>"
	// segment: the hostile string's own "/" characters must show up as
	// "%2F", never as additional path segments that could, in a real
	// server, be interpreted as path traversal or extra routing.
	const wantPrefix = "/projects/"
	if !strings.HasPrefix(gotRawPath, wantPrefix) {
		t.Fatalf("escaped request path = %q, want prefix %q", gotRawPath, wantPrefix)
	}
	rest := strings.TrimPrefix(gotRawPath, wantPrefix)
	if strings.Contains(rest, "/") {
		t.Errorf("escaped request path %q contains an unescaped '/': the hostile input was not fully percent-encoded", gotRawPath)
	}
	// "..%2F.." is expected to survive only as escaped text within the
	// single segment, never as an actual "../" traversal sequence in the
	// escaped request path — which is already guaranteed by the "no
	// unescaped '/'" check above, but asserted again explicitly here since a
	// literal "../" is the concrete shape a real path-traversal exploit
	// would need.
	if strings.Contains(gotRawPath, "../") {
		t.Errorf("escaped request path %q contains an unescaped \"../\" traversal sequence", gotRawPath)
	}
	// The decoded path must round-trip back to exactly one "projects/<hostile>"
	// segment count, i.e. Go's own URL decoding recovers the original text
	// rather than the server seeing a shorter/altered path.
	wantDecoded := wantPrefix + hostile
	if gotPath != wantDecoded {
		t.Errorf("decoded request path = %q, want %q", gotPath, wantDecoded)
	}
}

// TestClient_BlockedEndpoint asserts that an HTML response — the shape
// drupal.org's edge allowlist returns for a blocked endpoint — is reported
// as ErrDrupalOrgRefused via errors.Is, distinct from a JSON decode error
// and distinct from a plain *APIError 404, even though the status code
// itself is 404 in both the blocked and the "genuinely not found" case.
func TestClient_BlockedEndpoint(t *testing.T) {
	const hugeHTML = `<!DOCTYPE html><html><head><title>Page not found</title></head><body>Not Found</body></html>`

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(hugeHTML))
	})

	_, err := c.Project(context.Background(), "project/access_tokens")
	if err == nil {
		t.Fatal("Project: got nil error, want ErrDrupalOrgRefused")
	}
	if !errors.Is(err, ErrDrupalOrgRefused) {
		t.Errorf("Project error = %v, want errors.Is(err, ErrDrupalOrgRefused)", err)
	}
	var ae *APIError
	if errors.As(err, &ae) {
		t.Errorf("Project error = %v, want NOT an *APIError (must not look like a plain 404)", err)
	}
	if !strings.Contains(err.Error(), "GET") {
		t.Errorf("error %q does not name the HTTP method", err)
	}
}

// TestClient_BlockedEndpoint_ContentTypeMissing covers the fallback path:
// a server that mislabels its Content-Type (or omits it) but still sends an
// HTML body is still recognised as blocked, since the plan's discriminator
// is "content type AND, as a fallback, body shape" — not status code, and
// not Content-Type alone.
func TestClient_BlockedEndpoint_ContentTypeMissing(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// No Content-Type set at all; body still starts with an HTML doctype
		// after leading whitespace.
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("  \n<!doctype html><html><body>nope</body></html>"))
	})

	_, err := c.Project(context.Background(), "project/deploy_tokens")
	if !errors.Is(err, ErrDrupalOrgRefused) {
		t.Errorf("Project error = %v, want errors.Is(err, ErrDrupalOrgRefused)", err)
	}
}

// TestClient_NotFound_IsRealAPIError asserts that a genuine GitLab JSON 404
// (routed, but the resource doesn't exist) is a plain *APIError, checkable
// with IsNotFound, and is NOT mistaken for a blocked endpoint.
func TestClient_NotFound_IsRealAPIError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"404 Project Not Found"}`))
	})

	_, err := c.Project(context.Background(), "issue/drupal-9999999")
	if err == nil {
		t.Fatal("Project: got nil error, want *APIError 404")
	}
	if errors.Is(err, ErrDrupalOrgRefused) {
		t.Errorf("Project error = %v, want NOT ErrDrupalOrgRefused", err)
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}

// TestClient_NoCredentialHeader asserts that anonymous requests never carry
// a PRIVATE-TOKEN header (or any Authorization header) — authentication
// belongs entirely to a later task.
func TestClient_NoCredentialHeader(t *testing.T) {
	var gotPrivateToken, gotAuthorization string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPrivateToken = r.Header.Get("PRIVATE-TOKEN")
		gotAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":1}`))
	})
	if _, err := c.Project(context.Background(), "project/drupal"); err != nil {
		t.Fatalf("Project: %v", err)
	}

	if gotPrivateToken != "" {
		t.Errorf("PRIVATE-TOKEN header = %q, want empty (anonymous only)", gotPrivateToken)
	}
	if gotAuthorization != "" {
		t.Errorf("Authorization header = %q, want empty (anonymous only)", gotAuthorization)
	}
}

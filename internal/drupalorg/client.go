package drupalorg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// defaultBaseURL is git.drupalcode.org's GitLab REST API root.
const defaultBaseURL = "https://git.drupalcode.org/api/v4"

// Config configures a Client.
type Config struct {
	// BaseURL overrides the API root; empty uses defaultBaseURL. Tests point
	// this at an httptest.Server so no test ever contacts git.drupalcode.org.
	BaseURL string
	// HTTPClient overrides the transport; nil uses http.DefaultClient.
	HTTPClient *http.Client
}

// Client is a credential-free REST client for git.drupalcode.org's GitLab
// API. Every method here only ever reads — no request sets a PRIVATE-TOKEN
// header or any other credential. Authenticated writes (commit replay, merge
// requests) are deliberately out of scope for this package's client and
// belong to the publication path that holds the account PAT.
type Client struct {
	http *http.Client
	base *url.URL
}

// New builds a Client from cfg.
func New(cfg Config) (*Client, error) {
	raw := cfg.BaseURL
	if raw == "" {
		raw = defaultBaseURL
	}
	base, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("drupalorg: parsing base URL %q: %w", raw, err)
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &Client{http: httpClient, base: base}, nil
}

// ErrDrupalOrgRefused reports that drupal.org's edge blocked a request
// before it ever reached GitLab. drupal.org runs a per-path, per-method
// allowlist in front of the GitLab API: a blocked request returns a
// byte-identical, tens-of-kilobytes HTML 404 page from the drupal.org Drupal
// site itself, on every project tried, authenticated or not — never GitLab
// JSON. Reporting this distinctly (rather than as a JSON decode error, or
// worse, as a plain "404 not found" that looks like the project is missing)
// is deliberate: a generic error here would send someone hunting for a bug
// in the wrong place instead of recognising drupal.org's own edge policy.
var ErrDrupalOrgRefused = errors.New("drupal.org refused this request")

// APIError is a non-2xx JSON response from git.drupalcode.org's GitLab API.
// It is never constructed for a blocked-endpoint response — do() recognises
// and diverts those to ErrDrupalOrgRefused before a status code is even
// consulted, so an *APIError always reflects something GitLab itself said.
type APIError struct {
	// Status is the HTTP status code.
	Status int
	// Path is the request path that failed, for context in Error().
	Path string
	// Message is the response body's "message" field, when present. GitLab
	// error bodies are typically {"message": "..."} or
	// {"message": {"field": ["..."]}}; only the string form is captured here
	// since nothing in this package needs the structured variant.
	Message string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("drupalorg: %s: %d", e.Path, e.Status)
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// IsNotFound reports whether err is an *APIError for a 404 — a genuine
// GitLab "no such project/branch/merge request" response. This is distinct
// from ErrDrupalOrgRefused's edge-blocked HTML 404, which do() never lets
// reach this far: by the time an *APIError exists, the 404 came from GitLab
// itself, so a caller can trust it (e.g. "this fork does not exist yet").
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// do issues one anonymous request against path (already assembled with any
// necessary percent-escaping — see encodedProjectPath) and, on a 2xx
// response with out non-nil, decodes the JSON body into out. No request ever
// carries a PRIVATE-TOKEN header or any other credential.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, out any) error {
	full, err := c.endpoint(method, path, query)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, method, full, nil)
	if err != nil {
		return fmt.Errorf("drupalorg: building %s %s request: %w", method, path, err)
	}
	req.Header.Set("Accept", "application/json")

	return send(c.http, req, method, path, out)
}

// send executes req and classifies the response. It is the single place a
// request is actually put on the wire, shared by the anonymous do() above
// and by the authenticated requests in publish.go so that transport errors
// and response classification read identically whether or not a credential
// was attached. httpClient is a parameter rather than c.http because the
// authenticated path deliberately uses a transport that refuses redirects
// (see NewPublisher). method and path name the request in errors; they are
// passed rather than read back off req because path is the API path, not
// the escaped URL path.
func send(httpClient *http.Client, req *http.Request, method, path string, out any) error {
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("drupalorg: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	return interpretResponse(method, path, resp, out)
}

// endpoint renders the absolute URL for path (already percent-escaped where
// needed — see encodedProjectPath) and query. method is used only to name
// the failing request in an error.
//
// The URL is parsed from the concatenated string (not built by assigning to
// full.Path directly) so that path, which already contains percent-escapes
// from encodedProjectPath, is not double-encoded: url.URL.Path stores the
// DECODED path, so assigning an already-escaped string to it would have its
// own "%" characters re-escaped to "%25" when the request is finally
// rendered to the wire.
func (c *Client) endpoint(method, path string, query url.Values) (string, error) {
	full, err := url.Parse(strings.TrimSuffix(c.base.String(), "/") + path)
	if err != nil {
		return "", fmt.Errorf("drupalorg: building %s %s url: %w", method, path, err)
	}
	if len(query) > 0 {
		full.RawQuery = query.Encode()
	}
	return full.String(), nil
}

// interpretResponse reads resp and classifies it: a drupal.org edge refusal,
// a GitLab *APIError, or a 2xx whose JSON body is decoded into out when out
// is non-nil. Reached through send() from both the anonymous do() and the
// authenticated requests in publish.go, so a blocked endpoint, a GitLab
// error body, and a decode failure are recognised identically whether or not
// a credential was attached. It never reads the request's headers and so can
// never surface a credential in an error.
func interpretResponse(method, path string, resp *http.Response, out any) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("drupalorg: reading %s %s response: %w", method, path, err)
	}

	// Blocked-endpoint detection runs before status-code handling and before
	// any JSON decode attempt: a blocked response is HTML (characteristically
	// a 404, but the discriminator is content-type/body shape, not the status
	// code) and must never be mistaken for a JSON decode failure or a
	// GitLab-issued 404.
	if looksBlocked(resp.Header.Get("Content-Type"), body) {
		return fmt.Errorf("drupalorg: %s %s: %w", method, path, ErrDrupalOrgRefused)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		ae := &APIError{Status: resp.StatusCode, Path: path}
		var eb struct {
			Message string `json:"message"`
		}
		// Best-effort: a body that isn't {"message": "..."} (e.g. GitLab's
		// structured per-field validation shape) simply leaves Message zero
		// rather than failing the call.
		if len(body) > 0 && json.Unmarshal(body, &eb) == nil {
			ae.Message = eb.Message
		}
		return ae
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("drupalorg: decoding %s %s response: %w", method, path, err)
	}
	return nil
}

// looksBlocked reports whether a response looks like drupal.org's edge
// allowlist blocked the request rather than routing it to GitLab: GitLab
// always answers with JSON, so an HTML content type, or a body that starts
// with an HTML doctype/tag once whitespace is trimmed, is never a legitimate
// GitLab response.
func looksBlocked(contentType string, body []byte) bool {
	if strings.Contains(strings.ToLower(contentType), "text/html") {
		return true
	}
	trimmed := bytes.ToUpper(bytes.TrimSpace(body))
	return bytes.HasPrefix(trimmed, []byte("<!DOCTYPE")) || bytes.HasPrefix(trimmed, []byte("<HTML"))
}

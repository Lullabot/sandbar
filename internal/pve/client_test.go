package pve

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestClient starts an httptest.NewTLSServer running handler and returns a
// Client pointed at it with InsecureSkipVerify set, exactly as the task's
// Implementation Notes prescribe. The returned func tears the server down.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	ts := httptest.NewTLSServer(handler)
	t.Cleanup(ts.Close)

	c, err := New(Config{
		Host:               strings.TrimPrefix(ts.URL, "https://"),
		Node:               "node1",
		TokenID:            "user@pve!token=11111111-2222-3333-4444-555555555555",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// TestNewRequiresHostNodeAndToken pins that the constructor refuses a config it
// cannot build a working client from, rather than deferring the failure to the
// first request. Node is the one most easily mistaken for optional — it is NOT
// derivable from Host (PVE's node name is an identity in the URL path, not the
// address you reach it at), so a client built without it would emit
// "/nodes//qemu" and 404 in a way that reads like a missing VM.
func TestNewRequiresHostNodeAndToken(t *testing.T) {
	full := Config{Host: "pve.example", Node: "pve1", TokenID: "user@pve!tok=1"}

	tests := []struct {
		name  string
		clear func(*Config)
		want  string
	}{
		{"missing host", func(c *Config) { c.Host = "" }, "Host"},
		{"missing node", func(c *Config) { c.Node = "" }, "Node"},
		{"missing token", func(c *Config) { c.TokenID = "" }, "TokenID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := full
			tt.clear(&cfg)
			c, err := New(cfg)
			if err == nil {
				t.Fatalf("New(%+v) = %v, nil; want an error naming %s", cfg, c, tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %q; want it to name the missing field %q", err, tt.want)
			}
		})
	}
}

// TestNewAppendsDefaultPortToBareHost guards the difference between a profile
// that writes "pve.example" (the normal case, which must reach :8006) and one
// that writes an explicit "host:port" — which the tests themselves rely on to
// point at an httptest server on an arbitrary port, so appending :8006 to it
// would make every test in this package unroutable.
func TestNewAppendsDefaultPortToBareHost(t *testing.T) {
	bare, err := New(Config{Host: "pve.example", Node: "pve1", TokenID: "user@pve!tok=1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := bare.base.Host; got != "pve.example:8006" {
		t.Errorf("base host = %q; want pve.example:8006", got)
	}

	explicit, err := New(Config{Host: "127.0.0.1:44301", Node: "pve1", TokenID: "user@pve!tok=1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := explicit.base.Host; got != "127.0.0.1:44301" {
		t.Errorf("base host = %q; an explicit port must be used as-is", got)
	}
}

// TestNewCAFilePinsCertificateInsteadOfSkippingVerification exercises the
// alternative to InsecureSkipVerify: pinning PVE's own root CA. It is worth a
// real TLS handshake rather than a field assertion, because a CAFile that is
// loaded but never installed into tls.Config.RootCAs would still "succeed" in
// New and then fail — or, worse, silently verify against the system pool — at
// request time.
func TestNewCAFilePinsCertificateInsteadOfSkippingVerification(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"release":"9.0"}}`))
	}))
	defer ts.Close()

	caPath := filepath.Join(t.TempDir(), "pve-root-ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw})
	if err := os.WriteFile(caPath, pemBytes, 0o600); err != nil {
		t.Fatalf("write CA file: %v", err)
	}

	c, err := New(Config{
		Host:    strings.TrimPrefix(ts.URL, "https://"),
		Node:    "node1",
		TokenID: "user@pve!token=1",
		CAFile:  caPath,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var out struct {
		Release string `json:"release"`
	}
	if err := c.do(context.Background(), http.MethodGet, "/version", nil, nil, &out); err != nil {
		t.Fatalf("do with a pinned CA and verification ON: %v", err)
	}
	if out.Release != "9.0" {
		t.Errorf("release = %q; want 9.0", out.Release)
	}
}

// TestNewRejectsUnusableCAFile covers the two ways a pinned CA can be wrong. An
// unreadable or non-PEM file must fail loudly at construction: silently falling
// back to the system pool would leave the operator believing they had pinned
// PVE's self-signed CA while every request verified against something else.
func TestNewRejectsUnusableCAFile(t *testing.T) {
	dir := t.TempDir()
	garbage := filepath.Join(dir, "not-a-cert.pem")
	if err := os.WriteFile(garbage, []byte("this is not a certificate\n"), 0o600); err != nil {
		t.Fatalf("write garbage CA: %v", err)
	}

	tests := []struct {
		name string
		path string
		want string
	}{
		{"unreadable", filepath.Join(dir, "absent.pem"), "reading CA file"},
		{"not PEM", garbage, "no certificates found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(Config{Host: "pve.example", Node: "pve1", TokenID: "user@pve!tok=1", CAFile: tt.path})
			if err == nil {
				t.Fatalf("New with CAFile %q: expected an error", tt.path)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %q; want it to contain %q", err, tt.want)
			}
		})
	}
}

// TestDoRejectsUnusableMethodAndPath pins that a caller mistake is reported as a
// request-construction failure naming the method and path, not as an opaque
// transport error — and, more importantly, that no request is sent at all.
func TestDoRejectsUnusableMethodAndPath(t *testing.T) {
	var contacted bool
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		contacted = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":null}`))
	})

	// A control character cannot appear in a URL, so this fails in url.Parse
	// before any connection is attempted.
	if err := c.do(context.Background(), http.MethodGet, "/nodes/node\n1/status", nil, nil, nil); err == nil {
		t.Error("do with a control character in the path: expected an error")
	}
	// A method with a space is not a valid HTTP token.
	if err := c.do(context.Background(), "BAD METHOD", "/version", nil, nil, nil); err == nil {
		t.Error("do with an invalid method: expected an error")
	}
	if contacted {
		t.Error("server was contacted; a malformed request must fail before it is sent")
	}
}

// TestDoTruncatedResponseBodyIsAnError guards the read of the response body
// itself. A connection that dies mid-body is indistinguishable from a short
// success unless the read error is propagated — and a swallowed one would leave
// `out` at its zero value, which for a task-status poll reads as "not running
// yet" and spins forever.
func TestDoTruncatedResponseBodyIsAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "4096") // far more than is written
		_, _ = w.Write([]byte(`{"data":`))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Returning without writing the promised bytes drops the connection.
	})

	var out map[string]any
	err := c.do(context.Background(), http.MethodGet, "/version", nil, nil, &out)
	if err == nil {
		t.Fatal("do: expected an error for a truncated response body")
	}
}

// TestDoNullDataLeavesOutUntouched pins the tri-state PVE responses use:
// {"data":null} is a legitimate success (every synchronous mutation returns it)
// and must NOT be treated as a decode failure. WaitTask depends on this — it
// polls into a struct and relies on a null envelope leaving the zero value
// behind so the poll continues rather than aborting.
func TestDoNullDataLeavesOutUntouched(t *testing.T) {
	for _, body := range []string{`{"data":null}`, `{}`} {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		})

		out := taskStatusResponse{Status: "sentinel"}
		if err := c.do(context.Background(), http.MethodGet, "/version", nil, nil, &out); err != nil {
			t.Fatalf("do(%s): %v", body, err)
		}
		if out.Status != "sentinel" {
			t.Errorf("do(%s) overwrote out with %+v; a null envelope must leave it untouched", body, out)
		}
	}
}

// TestDoDecodeFailuresNameTheRequest covers the two distinct decode stages: a
// body that is not JSON at all, and a well-formed envelope whose data has the
// wrong shape for the caller's type. Both are silent-corruption bugs if they
// return nil, and both must name the method and path, since the caller sees only
// the error and PVE has many endpoints.
func TestDoDecodeFailuresNameTheRequest(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"body is not JSON", `<html>502 Bad Gateway</html>`, "envelope"},
		{"data has the wrong shape", `{"data":"a string"}`, "data"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			})

			var out struct {
				Name string `json:"name"`
			}
			err := c.do(context.Background(), http.MethodGet, "/nodes/node1/status", nil, nil, &out)
			if err == nil {
				t.Fatalf("do: expected an error decoding %s", tt.body)
			}
			if !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "/nodes/node1/status") {
				t.Errorf("err = %q; want it to mention %q and the request path", err, tt.want)
			}
		})
	}
}

// TestReasonPhraseFallsBackToCanonicalText covers the non-PVE case: a status
// line with no space (a proxy or a test server writing a bare code) leaves
// APIError.Reason empty unless the canonical text is substituted — and for a
// bare 403, Reason is the ONLY detail there is.
func TestReasonPhraseFallsBackToCanonicalText(t *testing.T) {
	if got := reasonPhrase("403 Forbidden", http.StatusForbidden); got != "Forbidden" {
		t.Errorf("reasonPhrase(%q) = %q; want Forbidden", "403 Forbidden", got)
	}
	if got := reasonPhrase("", http.StatusForbidden); got != "Forbidden" {
		t.Errorf("reasonPhrase(\"\") = %q; want the canonical text Forbidden", got)
	}
	if got := reasonPhrase("", 599); got != "" {
		t.Errorf("reasonPhrase(\"\", 599) = %q; want \"\" for a code with no canonical text", got)
	}
}

// TestHasErrorKeyOnNilError pins the nil guard. WaitTask calls HasErrorKey via
// errors.As, which leaves the target nil when the error is not an *APIError, and
// a nil-pointer panic there would crash every create the moment a task poll hit
// a transport error instead of an API error.
func TestHasErrorKeyOnNilError(t *testing.T) {
	var ae *APIError
	if ae.HasErrorKey("upid") {
		t.Error("(*APIError)(nil).HasErrorKey = true; want false")
	}
	if (&APIError{Status: 400}).HasErrorKey("upid") {
		t.Error("HasErrorKey on an APIError with no errors map = true; want false")
	}
}

func TestClientSendsExactlyOneAuthHeaderAndNoCSRFToken(t *testing.T) {
	const wantAuth = "PVEAPIToken=user@pve!token=11111111-2222-3333-4444-555555555555"

	var gotAuth string
	var csrfPresent bool
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, csrfPresent = r.Header["Csrfpreventiontoken"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":null}`))
	})

	if err := c.do(context.Background(), http.MethodGet, "/version", nil, nil, nil); err != nil {
		t.Fatalf("do: %v", err)
	}

	if gotAuth != wantAuth {
		t.Errorf("Authorization header = %q; want %q", gotAuth, wantAuth)
	}
	if csrfPresent {
		t.Errorf("CSRFPreventionToken header was sent; token auth must never send one")
	}
}

func TestDoUnwrapsDataEnvelope(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"name":"node1","cpus":4}}`))
	})

	var out struct {
		Name string `json:"name"`
		CPUs int    `json:"cpus"`
	}
	if err := c.do(context.Background(), http.MethodGet, "/nodes/node1/status", nil, nil, &out); err != nil {
		t.Fatalf("do: %v", err)
	}
	if out.Name != "node1" || out.CPUs != 4 {
		t.Fatalf("out = %+v; want Name=node1 CPUs=4", out)
	}
}

func TestDoEncodesBodyAsFormURLEncoded(t *testing.T) {
	var gotContentType, gotBody string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		gotBody = r.PostForm.Get("vmid")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":null}`))
	})

	body := map[string][]string{"vmid": {"100"}}
	if err := c.do(context.Background(), http.MethodPost, "/nodes/node1/qemu", nil, body, nil); err != nil {
		t.Fatalf("do: %v", err)
	}
	if gotContentType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q; want application/x-www-form-urlencoded", gotContentType)
	}
	if gotBody != "100" {
		t.Errorf("posted vmid = %q; want 100", gotBody)
	}
}

// TestForbiddenSurfacesReasonPhrase covers the acceptance criterion that a 403
// with an empty body ({"data":null}, no "message") still surfaces detail in
// err.Error() — because for a bare 403, the HTTP reason phrase ("Forbidden")
// is the ONLY place PVE puts it.
func TestForbiddenSurfacesReasonPhrase(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"data":null}`))
	})

	err := c.do(context.Background(), http.MethodGet, "/nodes/node1/qemu/100/status/current", nil, nil, nil)
	if err == nil {
		t.Fatal("do: expected an error for a 403 response")
	}
	if !strings.Contains(err.Error(), "Forbidden") {
		t.Fatalf("err.Error() = %q; want it to contain the reason phrase %q", err.Error(), "Forbidden")
	}
	if !IsPermission(err) {
		t.Errorf("IsPermission(err) = false; want true for a 403")
	}
	if IsNotFound(err) {
		t.Errorf("IsNotFound(err) = true; want false for a 403")
	}
}

func TestNotFoundClassification(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"data":null,"message":"no such resource"}`))
	})

	err := c.do(context.Background(), http.MethodGet, "/nodes/node1/qemu/999/status/current", nil, nil, nil)
	if err == nil {
		t.Fatal("do: expected an error for a 404 response")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(err) = false; want true for a 404")
	}
	if IsPermission(err) {
		t.Errorf("IsPermission(err) = true; want false for a 404")
	}
	if !strings.Contains(err.Error(), "no such resource") {
		t.Errorf("err.Error() = %q; want it to contain the body message", err.Error())
	}
}

func TestErrorsMapSurfacedInErrorString(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		body, _ := json.Marshal(map[string]any{
			"data":    nil,
			"message": "parameter verification failed",
			"errors":  map[string]string{"upid": "unable to parse worker upid"},
		})
		_, _ = w.Write(body)
	})

	err := c.do(context.Background(), http.MethodGet, "/nodes/node1/tasks/bogus/status", nil, nil, nil)
	if err == nil {
		t.Fatal("do: expected an error for a 400 response")
	}
	if !strings.Contains(err.Error(), "unable to parse worker upid") {
		t.Errorf("err.Error() = %q; want it to contain the errors map detail", err.Error())
	}
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("expected err to be an *APIError, got %T: %v", err, err)
	}
	if !ae.HasErrorKey("upid") {
		t.Errorf("HasErrorKey(%q) = false; want true", "upid")
	}
	if ae.HasErrorKey("other") {
		t.Errorf("HasErrorKey(%q) = true; want false", "other")
	}
}

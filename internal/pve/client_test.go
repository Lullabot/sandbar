package pve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

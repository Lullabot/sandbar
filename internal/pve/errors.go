package pve

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// APIError is a non-2xx response from the Proxmox VE API. It is deliberately
// richer than "status code + body": PVE's most important failure mode, a 403,
// carries its ONLY useful detail in the HTTP reason phrase — the body is just
// {"data":null} — so Reason is captured unconditionally, not just as a
// fallback when the body is empty.
type APIError struct {
	// Status is the HTTP status code.
	Status int
	// Reason is the HTTP status line's reason phrase (e.g. "Forbidden" for a
	// 403). This is the ONLY place a bare 403's detail appears.
	Reason string
	// Message is the response body's "message" field, when present.
	Message string
	// Errors is the response body's "errors" map, when present — PVE's
	// per-field validation detail (e.g. {"upid": "no such task"}).
	Errors map[string]string
}

// Error renders Status and Reason unconditionally (Reason may be all a caller
// has, per the 403 case above), then appends Message and Errors when present.
func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "pve: %d %s", e.Status, e.Reason)
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	if len(e.Errors) > 0 {
		keys := make([]string, 0, len(e.Errors))
		for k := range e.Errors {
			keys = append(keys, k)
		}
		sort.Strings(keys) // deterministic Error() text: map order is not
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = fmt.Sprintf("%s: %s", k, e.Errors[k])
		}
		fmt.Fprintf(&b, " (%s)", strings.Join(parts, ", "))
	}
	return b.String()
}

// HasErrorKey reports whether e's body "errors" map contains k. This is how
// WaitTask distinguishes "task not yet registered" (errors["upid"] set) from a
// genuinely malformed request — both return HTTP 400 with indistinguishable
// status codes, so callers must match on the key, never the code alone.
func (e *APIError) HasErrorKey(k string) bool {
	if e == nil {
		return false
	}
	_, ok := e.Errors[k]
	return ok
}

// IsPermission reports whether err is an *APIError for a 401 or 403 — a
// PERMANENT failure that retry loops (including WaitTask) must never spin on.
func IsPermission(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && (ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden)
}

// IsNotFound reports whether err is an *APIError for a 404.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// reasonPhrase extracts the reason phrase from an *http.Response's status
// line ("403 Forbidden" -> "Forbidden"). It falls back to the canonical text
// for the status code if the status line is missing or unparseable — which
// should not happen against a real PVE server, but keeps this total.
func reasonPhrase(status string, code int) string {
	if _, phrase, ok := strings.Cut(status, " "); ok {
		return phrase
	}
	return http.StatusText(code)
}

// errorBody is the JSON shape of a PVE error response body, e.g.
// {"data":null,"message":"...","errors":{"upid":"no such task"}}. A 403 body
// is just {"data":null} — Message and Errors are correctly left zero.
type errorBody struct {
	Message string            `json:"message"`
	Errors  map[string]string `json:"errors"`
}

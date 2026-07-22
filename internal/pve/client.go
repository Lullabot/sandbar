// Package pve is a minimal, dependency-free client for the Proxmox VE REST
// API. It exists rather than a third-party library because API-token auth is
// a single static header with no CSRF token and no ticket lifecycle, so the
// transport is trivial — while the semantics that actually matter (tri-state
// task exit status, fields that report 0 or silently change meaning) must be
// encoded here regardless of who unmarshals the JSON.
package pve

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// apiBasePath is the JSON API root. /api2/extjs is deliberately never used:
// it rewrites API errors onto HTTP 200, which would make every error path in
// this package (and everything built on IsPermission/IsNotFound) silently
// wrong.
const apiBasePath = "/api2/json"

// defaultPort is PVE's fixed API port.
const defaultPort = "8006"

// Config configures a Client.
type Config struct {
	// Host is the PVE node's hostname or IP. A bare host (no ":port") gets
	// ":8006" appended; a "host:port" pair (used by tests pointed at an
	// httptest server on an arbitrary port) is used as-is.
	Host string
	// Node is this PVE node's name — distinct from Host, and the identifier
	// PVE itself uses in path segments like /nodes/{node}/status.
	Node string
	// TokenID is the full PVE API token identity, "user@realm!tokenid=uuid".
	TokenID string
	// InsecureSkipVerify disables TLS certificate verification. PVE ships a
	// self-signed certificate by default, so this is a real, expected
	// per-profile opt-out rather than a footgun left in by omission.
	InsecureSkipVerify bool
	// CAFile optionally pins a CA certificate (e.g. PVE's own
	// /etc/pve/pve-root-ca.pem) instead of disabling verification outright.
	CAFile string
}

// Client is a REST client bound to one Proxmox VE node, authenticating every
// request with a single static API token header.
type Client struct {
	http        *http.Client
	base        *url.URL
	tokenHeader string
	node        string
}

// New builds a Client from cfg. The token header is built once here —
// "PVEAPIToken=<tokenID>" — and sent as-is on every request; unlike ticket
// auth, PVE API-token auth has no CSRFPreventionToken and this client never
// sends one.
func New(cfg Config) (*Client, error) {
	if cfg.Host == "" {
		return nil, errors.New("pve: Host is required")
	}
	if cfg.Node == "" {
		return nil, errors.New("pve: Node is required")
	}
	if cfg.TokenID == "" {
		return nil, errors.New("pve: TokenID is required")
	}

	hostport := cfg.Host
	if !strings.Contains(hostport, ":") {
		hostport = hostport + ":" + defaultPort
	}

	tlsConfig := &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("pve: reading CA file %s: %w", cfg.CAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("pve: no certificates found in %s", cfg.CAFile)
		}
		tlsConfig.RootCAs = pool
	}

	return &Client{
		http:        &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}},
		base:        &url.URL{Scheme: "https", Host: hostport, Path: apiBasePath},
		tokenHeader: fmt.Sprintf("PVEAPIToken=%s", cfg.TokenID),
		node:        cfg.Node,
	}, nil
}

// do issues one request and, on a 2xx response with out non-nil, unwraps the
// {"data": ...} envelope every PVE response uses into out. body is encoded as
// application/x-www-form-urlencoded (PVE accepts this everywhere and it avoids
// JSON type-coercion surprises — e.g. a bool field that PVE reads as 0/1).
func (c *Client) do(ctx context.Context, method, path string, query, body url.Values, out any) error {
	// Parsed from the concatenated string (not built by assigning to
	// u.Path directly) so that a path already containing percent-escapes
	// (WaitTask escapes the UPID itself) is not double-encoded.
	full, err := url.Parse(c.base.String() + path)
	if err != nil {
		return fmt.Errorf("pve: building %s %s url: %w", method, path, err)
	}
	if len(query) > 0 {
		full.RawQuery = query.Encode()
	}

	var reqBody io.Reader
	if len(body) > 0 {
		reqBody = strings.NewReader(body.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, method, full.String(), reqBody)
	if err != nil {
		return fmt.Errorf("pve: building %s %s request: %w", method, path, err)
	}
	req.Header.Set("Authorization", c.tokenHeader)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("pve: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("pve: reading %s %s response: %w", method, path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		ae := &APIError{
			Status: resp.StatusCode,
			Reason: reasonPhrase(resp.Status, resp.StatusCode),
		}
		// Best-effort: an empty or non-JSON body (a bare 403 has neither)
		// simply leaves Message/Errors zero rather than failing the call.
		var eb errorBody
		if len(respBody) > 0 && json.Unmarshal(respBody, &eb) == nil {
			ae.Message = eb.Message
			ae.Errors = eb.Errors
		}
		return ae
	}

	if out == nil {
		return nil
	}

	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return fmt.Errorf("pve: decoding %s %s envelope: %w", method, path, err)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("pve: decoding %s %s data: %w", method, path, err)
	}
	return nil
}

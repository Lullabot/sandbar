---
id: 1
group: "pve-api-client"
dependencies: []
status: "completed"
created: 2026-07-20
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "First HTTP client in a repo with zero HTTP dependencies; the UPID waiter encodes non-obvious tri-state success semantics whose misreading is a known duplicate-VM bug class."
skills:
  - go-http-client
  - proxmox-api
---
# internal/pve: HTTP transport, token auth, error shaping, and the UPID task waiter

## Objective

Create the `internal/pve` package with a dependency-free `net/http` client for
the Proxmox VE REST API: token authentication, TLS handling, `{"data": ...}`
envelope unwrapping, faithful error shaping, and an asynchronous task (UPID)
waiter that classifies Proxmox's tri-state `exitstatus` correctly.

## Skills Required

`go-http-client` (stdlib `net/http`, TLS config, JSON decoding, context
cancellation) and `proxmox-api` (PVE auth model, UPID grammar, task semantics).

## Acceptance Criteria

- [ ] `go build ./internal/pve/` succeeds and `go vet ./internal/pve/` is clean.
- [ ] `go.mod` gains **no new direct dependency** — verify with
      `git diff --exit-code go.mod go.sum` showing no change.
- [ ] `go test ./internal/pve/ -race` passes, including tests that assert:
      `exitstatus: "OK"` is success; `exitstatus: "WARNINGS: 3"` is **success**;
      any other `exitstatus` is an error carrying that text; a 403 response with
      an empty body surfaces the HTTP reason phrase in `err.Error()`; a UPID with
      a 9-hex-digit `pstart` parses correctly.
- [ ] The client sends exactly one auth header,
      `Authorization: PVEAPIToken=<user>@<realm>!<tokenid>=<uuid>`, and sends
      **no** `CSRFPreventionToken` — asserted against an `httptest` server.

## Technical Requirements

- Package `internal/pve`. Base path is `/api2/json` (never `/api2/extjs`, which
  rewrites errors to HTTP 200).
- `Client` struct: `*http.Client`, base URL, token header value, node name.
  Constructor takes a config struct including `InsecureSkipVerify bool` and an
  optional `CAFile string` for pinning `/etc/pve/pve-root-ca.pem`.
- All responses unwrap `{"data": ...}`. Provide a generic `do` helper taking a
  method, path, query/body values, and a destination pointer.
- **Errors** must include: HTTP status code, the HTTP **reason phrase**, and the
  body's `message` and `errors` map when present. A 403's detail lives *only* in
  the reason phrase (body is `{"data":null}`) — losing it is a known failure mode.
- Classify errors: expose `IsPermission(err)` (401/403) and `IsNotFound(err)`.
  Permission errors must be flagged as **permanent** so retry loops never spin on
  them.
- **UPID parsing**: `UPID:<node>:<pid_hex>:<pstart_hex>:<starttime_hex>:<type>:<id>:<user>:`
  — split into 9 elements; the trailing colon is mandatory; `pstart` may be 8 or
  9 hex digits; `id` may be empty.
- **`WaitTask(ctx, upid)`**: polls `GET /nodes/{node}/tasks/{upid}/status` with
  the UPID percent-encoded in the path. Success iff `exitstatus == "OK"` or it
  matches `^WARNINGS: \d+$`. Tolerate, as transient: HTTP 400 whose error body
  has an `upid` key (task not yet visible), and HTTP 596/599 (proxy artifacts).
  Honour `ctx` cancellation. On failure, fetch the task log
  (`/tasks/{upid}/log` with an explicit high `limit` — the default of 50 usually
  misses the trailing `TASK ERROR:` line) and fold it into the error.

## Input Dependencies

None.

## Output Artifacts

`internal/pve/client.go`, `internal/pve/errors.go`, `internal/pve/task.go`, and
their tests — consumed by tasks 3, 4, and every provider task.

## Implementation Notes

<details>

Create `internal/pve/client.go`:

```go
// Package pve is a minimal, dependency-free client for the Proxmox VE REST
// API. It exists rather than a third-party library because API-token auth is a
// single static header with no CSRF token and no ticket lifecycle, so the
// transport is trivial — while the semantics that actually matter (tri-state
// task exit status, fields that report 0 or silently change meaning) must be
// encoded here regardless of who unmarshals the JSON.
package pve
```

`Client` fields: `http *http.Client`, `base *url.URL` (scheme https, host
`host:8006`, path `/api2/json`), `tokenHeader string`, `node string`.

Build the token header once in the constructor:
`fmt.Sprintf("PVEAPIToken=%s", tokenID)` where `tokenID` is the full
`user@realm!name=uuid`. Set it as the `Authorization` header on every request.
**Do not set `CSRFPreventionToken`** — token auth does not use it, for any
method.

TLS: when `CAFile` is set, load it into an `x509.CertPool` and set
`RootCAs`; when `InsecureSkipVerify` is set, set it on the `tls.Config`. Default
is normal verification.

`do(ctx, method, path string, query url.Values, body url.Values, out any) error`:
- Build the URL from `base` + path; attach `query`.
- For POST/PUT, encode `body` as `application/x-www-form-urlencoded` (PVE accepts
  this everywhere and it avoids JSON type-coercion surprises).
- On non-2xx, build the error described below and return it.
- On 2xx with `out != nil`, decode `{"data": <out>}`.

`errors.go`:

```go
type APIError struct {
    Status  int
    Reason  string            // HTTP reason phrase — the ONLY place a 403's detail appears
    Message string            // body "message"
    Errors  map[string]string // body "errors"
}
```

`Error()` must include `Reason`. Implement `IsPermission(err) bool` (403 or 401)
and `IsNotFound(err) bool` (404). Add `func (e *APIError) HasErrorKey(k string) bool`.

`task.go`:

```go
type UPID struct { Raw, Node, Type, ID, User string }

func ParseUPID(s string) (UPID, error) {
    parts := strings.Split(s, ":")
    if len(parts) != 9 || parts[0] != "UPID" { return UPID{}, fmt.Errorf(...) }
    // parts[8] is the empty string after the mandatory trailing colon.
    return UPID{Raw: s, Node: parts[1], Type: parts[5], ID: parts[6], User: parts[7]}, nil
}
```

Do **not** validate `pstart` as exactly 8 hex digits — it is 9 on hosts with
uptime over 497 days.

```go
var warnRE = regexp.MustCompile(`^WARNINGS: \d+$`)

// taskSucceeded reports whether a finished task's exitstatus means success.
// Proxmox treats "WARNINGS: n" as SUCCESS; classifying it as failure is a real
// bug class that produces duplicate VMs (the caller creates the VM, decides the
// task failed, drops it from state, and creates another on the next run).
func taskSucceeded(exitStatus string) bool {
    return exitStatus == "OK" || warnRE.MatchString(exitStatus)
}
```

`WaitTask(ctx, upid string) error`: poll every ~1s (with a small backoff up to
~5s). `GET /nodes/{node}/tasks/{url.PathEscape(upid)}/status` returns
`{status: "running"|"stopped", exitstatus: "..."}`. Loop while
`status == "running"`. When stopped, apply `taskSucceeded`.

Transient handling inside the loop — return the error only if it is *not* one of:
- `IsPermission(err)` → **return immediately, never retry**;
- an `*APIError` with `Status == 400` and `HasErrorKey("upid")` → the task is not
  yet registered; keep polling. Match on the key, **not** the status code: `400`
  is also what a genuinely malformed request returns, and the two messages
  ("no such task" / "unable to parse worker upid") are indistinguishable by code.
- `Status == 596 || Status == 599` → proxy artifacts, keep polling.

On failure, best-effort `GET /nodes/{node}/tasks/{upid}/log?limit=1000` (the
default limit of 50 typically truncates before the `TASK ERROR:` line) and
append the last few lines to the returned error.

Tests (`client_test.go`, `task_test.go`) use `httptest.NewTLSServer` with the
client pointed at it and `InsecureSkipVerify: true`. Cover every bullet in the
Acceptance Criteria. Keep tests focused on this custom logic — do not test
`net/http` itself.

</details>

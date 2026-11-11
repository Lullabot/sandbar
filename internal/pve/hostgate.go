package pve

// hostgate.go is the client's load discipline toward ONE Proxmox host, shared by
// every Client that talks to it. It exists because a sandbar TUI once took a PVE
// node down: an unbounded number of concurrent requests (each its own TCP+TLS
// connection, since Go's default transport keeps only two idle connections per
// host) exhausted pveproxy's file descriptors, and once the proxy in front of it
// answered 502, the client kept polling at full rate into the outage.
//
// Three rules, each enforced here rather than at the call sites, because a call
// site is one forgotten loop away from breaking them:
//
//  1. A hard cap on concurrent requests per host (maxConcurrentPerHost). A
//     request waits for a slot, honouring its context, before it touches the
//     network. The transport's MaxConnsPerHost matches it, so the cap also
//     bounds the connections pveproxy has to hold open.
//  2. Connection reuse: one shared Transport per (host, TLS settings), with
//     enough idle connections kept to serve the whole cap without a fresh
//     handshake per request.
//  3. Exponential backoff with jitter after the host fails to answer — a
//     gateway 5xx (the reverse proxy could not reach pveproxy), PVE's own
//     596/599 proxy errors, or a transport failure. While the gate is cooling
//     down, requests fail FAST with *UnavailableError and never reach the
//     network. Plain 500s do NOT trip it: PVE answers ordinary application
//     errors ("guest agent is not running", "VM is locked") with 500, and a
//     host that is answering those is healthy.

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/lullabot/sandbar/internal/backoff"
)

// maxConcurrentPerHost caps simultaneous requests to one PVE host across every
// Client in this process. Four is enough to keep a refresh and a lifecycle
// operation moving at the same time, and few enough that a misbehaving caller
// cannot cost pveproxy more than a handful of workers and descriptors.
const maxConcurrentPerHost = 4

// Backoff schedule for an unavailable host: backoffBase doubled per
// consecutive failure, capped at backoffMax, then jittered (see backoffFor).
// Vars, not consts, so tests can shrink them.
var (
	backoffBase = time.Second
	backoffMax  = 30 * time.Second
)

// Transport timeouts. Request deadlines come from each caller's context; these
// bound the phases a context-less stall could otherwise hang in forever.
const (
	dialTimeout         = 10 * time.Second
	tlsHandshakeTimeout = 10 * time.Second
	idleConnTimeout     = 90 * time.Second
)

// ErrUnavailable is the sentinel every *UnavailableError matches with
// errors.Is: the host is not answering, so the request was refused locally or
// failed in transit. Callers branch on it (IsUnavailable) to treat the failure
// as transient and to show a degraded state rather than a hard error.
var ErrUnavailable = errors.New("pve: API unavailable")

// UnavailableError reports a request refused because its host is cooling down
// after failing to answer. RetryAfter is how long until the gate reopens; Cause
// is the failure that closed it.
type UnavailableError struct {
	Host       string
	RetryAfter time.Duration
	Cause      error
}

func (e *UnavailableError) Error() string {
	msg := fmt.Sprintf("pve: %s is not answering; backing off, next attempt in %s", e.Host, e.RetryAfter.Round(time.Second))
	if e.Cause != nil {
		msg += " (last error: " + e.Cause.Error() + ")"
	}
	return msg
}

func (e *UnavailableError) Is(target error) bool { return target == ErrUnavailable }
func (e *UnavailableError) Unwrap() error        { return e.Cause }

// IsUnavailable reports whether err means the host did not answer: a request
// refused by the backoff gate, a gateway/proxy 5xx, or a transport failure.
// It is the "transient, slow down" class — never a permission error and never
// one of PVE's application-level 500s.
func IsUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrUnavailable) {
		return true
	}
	var ae *APIError
	if errors.As(err, &ae) {
		return unavailableStatus(ae.Status)
	}
	return false
}

// RetryAfter returns how long err asks a poller to wait before trying again,
// or 0 when err carries no such hint.
func RetryAfter(err error) time.Duration {
	var ue *UnavailableError
	if errors.As(err, &ue) {
		return ue.RetryAfter
	}
	return 0
}

// unavailableStatus is the set of HTTP statuses that mean "the host did not
// answer this request", as opposed to "the host answered with an error".
// 502/503/504 are what a reverse proxy returns when pveproxy is down or
// saturated; 596 and 599 are pveproxy's own proxying failures.
func unavailableStatus(code int) bool {
	switch code {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 596, 599:
		return true
	}
	return false
}

// hostGate is the shared per-host state: the concurrency semaphore and the
// backoff window.
type hostGate struct {
	host string
	sem  chan struct{}

	mu        sync.Mutex
	failures  int
	openUntil time.Time
	cause     error
}

var (
	gatesMu sync.Mutex
	gates   = map[string]*hostGate{}

	transportsMu sync.Mutex
	transports   = map[string]*http.Client{}
)

// gateFor returns the process-wide gate for hostport, creating it on first use.
// Keyed by host alone, NOT by TLS settings or token: two profiles that point at
// one node share that node's pveproxy, so they share its budget too.
func gateFor(hostport string) *hostGate {
	gatesMu.Lock()
	defer gatesMu.Unlock()
	g, ok := gates[hostport]
	if !ok {
		g = &hostGate{host: hostport, sem: make(chan struct{}, maxConcurrentPerHost)}
		gates[hostport] = g
	}
	return g
}

// sharedHTTPClient returns the process-wide client for hostport with this TLS
// configuration. The key includes the TLS settings (and the CA bundle's
// content, not just its path) because a Transport's TLS config is fixed at
// construction; two profiles that differ only in verification must not share
// connections.
func sharedHTTPClient(hostport string, insecure bool, caPEM []byte, tlsConfig *tls.Config) *http.Client {
	sum := sha256.Sum256(caPEM)
	key := fmt.Sprintf("%s|%t|%s", hostport, insecure, hex.EncodeToString(sum[:]))

	transportsMu.Lock()
	defer transportsMu.Unlock()
	if c, ok := transports[key]; ok {
		return c
	}
	c := &http.Client{Transport: &http.Transport{
		TLSClientConfig:     tlsConfig,
		DialContext:         (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: tlsHandshakeTimeout,
		IdleConnTimeout:     idleConnTimeout,
		// Matched to the semaphore: every request in flight can find a warm
		// connection, and pveproxy never holds more than the cap per process.
		MaxIdleConns:        maxConcurrentPerHost * 4,
		MaxIdleConnsPerHost: maxConcurrentPerHost,
		MaxConnsPerHost:     maxConcurrentPerHost,
	}}
	transports[key] = c
	return c
}

// acquire waits for a request slot. It first refuses outright while the gate
// is cooling down, so a request that would only queue behind an outage never
// waits for a slot it could not use.
func (g *hostGate) acquire(ctx context.Context) error {
	if err := g.refusal(); err != nil {
		return err
	}
	select {
	case g.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	// The gate may have closed while this request waited for its slot.
	if err := g.refusal(); err != nil {
		<-g.sem
		return err
	}
	return nil
}

func (g *hostGate) release() { <-g.sem }

// refusal returns an *UnavailableError while the backoff window is open.
func (g *hostGate) refusal() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if wait := time.Until(g.openUntil); wait > 0 {
		return &UnavailableError{Host: g.host, RetryAfter: wait, Cause: g.cause}
	}
	return nil
}

// succeed records that the host produced a response that is not a gateway
// failure. Any such response, including a 4xx or an application 500, proves the
// host is up and resets the backoff.
func (g *hostGate) succeed() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failures = 0
	g.openUntil = time.Time{}
	g.cause = nil
}

// fail records that the host did not answer and returns the failure as an
// *UnavailableError, so it classifies (IsUnavailable) exactly like the refusals
// that follow it. Without that, the FIRST failure of an outage looked like an
// ordinary error: waitTask abandoned a task still running on the node, and the
// board labelled the member broken rather than degraded.
//
// The window is extended from the CURRENT failure count, so a burst of requests
// that were all in flight when the host went down advances the schedule rather
// than each restarting it.
func (g *hostGate) fail(cause error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failures++
	g.cause = cause
	if until := time.Now().Add(backoffFor(g.failures)); until.After(g.openUntil) {
		g.openUntil = until
	}
	return &UnavailableError{Host: g.host, RetryAfter: time.Until(g.openUntil), Cause: cause}
}

// backoffFor is the cooldown after the nth consecutive failure: backoffBase
// doubled per failure up to backoffMax, then "equal jitter" — a uniformly
// random point in the upper half of that window. The jitter keeps several
// sandbar processes (or several profiles) that saw the same outage from
// retrying in lockstep; the floor of half keeps the schedule meaningfully
// exponential.
func backoffFor(n int) time.Duration {
	d := backoff.Doubling(backoffBase, n, backoffMax)
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + rand.N(half+1)
}

// callerCanceled reports whether a failed round trip was the CALLER giving up.
// That says nothing about the host, so it must not move the gate at all. A
// deadline that expired waiting on the host, by contrast, IS a sign it is not
// keeping up, as is every dial/TLS/reset failure.
func callerCanceled(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.Canceled)
}

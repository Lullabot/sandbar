package pve

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// withFastBackoff shrinks the host gate's backoff so a test that trips it
// does not wait out real seconds.
func withFastBackoff(t *testing.T) {
	t.Helper()
	origBase, origMax := backoffBase, backoffMax
	backoffBase, backoffMax = 20*time.Millisecond, 80*time.Millisecond
	t.Cleanup(func() { backoffBase, backoffMax = origBase, origMax })
}

// countingServer is a TLS test server that counts requests and new TCP
// connections, so a test can assert on what actually reached the host.
type countingServer struct {
	ts       *httptest.Server
	requests atomic.Int64
	conns    atomic.Int64
}

func newCountingServer(t *testing.T, handler http.HandlerFunc) *countingServer {
	t.Helper()
	cs := &countingServer{}
	cs.ts = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.requests.Add(1)
		handler(w, r)
	}))
	cs.ts.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			cs.conns.Add(1)
		}
	}
	cs.ts.StartTLS()
	t.Cleanup(cs.ts.Close)
	return cs
}

func (cs *countingServer) client(t *testing.T) *Client {
	t.Helper()
	c, err := New(Config{
		Host:               strings.TrimPrefix(cs.ts.URL, "https://"),
		Node:               "node1",
		TokenID:            "user@pve!token=11111111-2222-3333-4444-555555555555",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func okJSON(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{}})
}

// The per-host cap must hold across EVERY client pointed at that host — two
// profiles (or a rebuilt provider beside its predecessor) share one pveproxy,
// so they must share one budget. 40 requests are launched at once from two
// clients; the server must never see more than maxConcurrentPerHost of them
// in flight.
func TestConcurrencyCapHoldsAcrossClientsForOneHost(t *testing.T) {
	var inFlight, peak atomic.Int64
	cs := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond) // hold the slot long enough to overlap
		inFlight.Add(-1)
		okJSON(w, r)
	})
	a, b := cs.client(t), cs.client(t)

	var wg sync.WaitGroup
	for i := range 40 {
		c := a
		if i%2 == 1 {
			c = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.GetConfig(context.Background(), 100); err != nil {
				t.Errorf("GetConfig: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := peak.Load(); got > maxConcurrentPerHost {
		t.Fatalf("peak concurrent requests at the host = %d, want <= %d", got, maxConcurrentPerHost)
	}
	if got := cs.requests.Load(); got != 40 {
		t.Fatalf("server saw %d requests, want all 40 to complete", got)
	}
	// Connections are reused across the burst: never more than the cap.
	if got := cs.conns.Load(); got > maxConcurrentPerHost {
		t.Errorf("server accepted %d connections for 40 requests, want <= %d (connections must be reused)", got, maxConcurrentPerHost)
	}
}

// Sequential requests must ride one kept-alive connection, error responses
// included: a client that does not drain an error body throws its connection
// away and redials, and each redial costs pveproxy a descriptor and a TLS
// handshake.
func TestConnectionsAreReusedIncludingErrorResponses(t *testing.T) {
	withFastBackoff(t)
	var n atomic.Int64
	cs := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1)%2 == 0 {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"data": nil, "message": "QEMU guest agent is not running"})
			return
		}
		okJSON(w, r)
	})
	c := cs.client(t)
	for range 20 {
		_, _ = c.GetConfig(context.Background(), 100)
	}
	if got := cs.conns.Load(); got != 1 {
		t.Fatalf("20 sequential requests opened %d connections, want 1", got)
	}
}

// A gateway 5xx is the host not answering. After one, further requests must be
// refused LOCALLY — they never reach the network — with an error that says so
// and how long to wait; once the window passes, a request goes through again,
// and a success closes the gate fully.
func TestGatewayErrorBacksOffWithoutTouchingTheHost(t *testing.T) {
	withFastBackoff(t)
	var failing atomic.Bool
	failing.Store(true)
	cs := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		okJSON(w, r)
	})
	c := cs.client(t)

	_, err := c.GetConfig(context.Background(), 100)
	if !IsUnavailable(err) {
		t.Fatalf("a 502 must classify as unavailable, got %v", err)
	}
	if got := cs.requests.Load(); got != 1 {
		t.Fatalf("server saw %d requests, want 1", got)
	}

	// A tight loop of retries during the window: none may reach the host.
	for range 50 {
		_, err = c.GetConfig(context.Background(), 100)
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("a request during backoff must be refused with ErrUnavailable, got %v", err)
		}
		if RetryAfter(err) <= 0 {
			t.Fatalf("a refusal must carry a positive RetryAfter, got %v", RetryAfter(err))
		}
	}
	if got := cs.requests.Load(); got != 1 {
		t.Fatalf("server saw %d requests during the backoff window, want still 1", got)
	}

	failing.Store(false)
	time.Sleep(backoffMax + 10*time.Millisecond)
	if _, err := c.GetConfig(context.Background(), 100); err != nil {
		t.Fatalf("after the window a request must reach the host: %v", err)
	}
	if _, err := c.GetConfig(context.Background(), 100); err != nil {
		t.Fatalf("a success must reset the gate: %v", err)
	}
}

// Consecutive failures lengthen the window exponentially (within jitter) up to
// the cap, so a long outage is probed at most every backoffMax.
func TestBackoffScheduleIsExponentialJitteredAndCapped(t *testing.T) {
	origBase, origMax := backoffBase, backoffMax
	backoffBase, backoffMax = time.Second, 30*time.Second
	t.Cleanup(func() { backoffBase, backoffMax = origBase, origMax })

	want := []time.Duration{1, 2, 4, 8, 16, 30, 30, 30}
	for i, full := range want {
		full *= time.Second
		for range 200 {
			got := backoffFor(i + 1)
			if got < full/2 || got > full {
				t.Fatalf("backoffFor(%d) = %v, want within [%v, %v]", i+1, got, full/2, full)
			}
		}
	}
}

// PVE answers ordinary application errors with 500 — a guest agent that is not
// running is the common one. The host is healthy; tripping the gate on it would
// lock every OTHER request out because one VM has no agent.
func TestApplication500DoesNotTripTheGate(t *testing.T) {
	cs := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"data": nil, "message": "QEMU guest agent is not running"})
	})
	c := cs.client(t)
	for range 5 {
		_, err := c.GetConfig(context.Background(), 100)
		if IsUnavailable(err) {
			t.Fatalf("an application 500 must not classify as unavailable: %v", err)
		}
	}
	if got := cs.requests.Load(); got != 5 {
		t.Fatalf("server saw %d of 5 requests; a 500 must not trip the gate", got)
	}
}

// A caller cancelling its own request says nothing about the host's health.
func TestCallerCancellationDoesNotTripTheGate(t *testing.T) {
	release := make(chan struct{})
	cs := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		okJSON(w, r)
	})
	t.Cleanup(func() { close(release) })
	c := cs.client(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err := c.GetConfig(ctx, 100); err == nil {
		t.Fatal("expected the cancelled request to fail")
	}
	if err := c.gate.refusal(); err != nil {
		t.Fatalf("a caller cancellation must not close the gate: %v", err)
	}
}

// A request waiting for a slot honours its own deadline, and one that was
// queued when the host failed is refused once a slot frees — it never goes on
// to hit a host that has just stopped answering.
func TestQueuedRequestsHonourDeadlinesAndAGateThatClosedMeanwhile(t *testing.T) {
	withFastBackoff(t)
	g := &hostGate{host: "h", sem: make(chan struct{}, maxConcurrentPerHost)}
	for range maxConcurrentPerHost {
		if err := g.acquire(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := g.acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a request that cannot get a slot before its deadline must fail with it, got %v", err)
	}

	queued := make(chan error, 1)
	go func() { queued <- g.acquire(context.Background()) }()
	time.Sleep(10 * time.Millisecond) // let it queue behind the full semaphore
	g.record(false, errors.New("502"))
	g.release()
	if err := <-queued; !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a request queued across the host failing must be refused, got %v", err)
	}
	if got := len(g.sem); got != maxConcurrentPerHost-1 {
		t.Fatalf("a refused request must give its slot back: %d slots held, want %d", got, maxConcurrentPerHost-1)
	}
}

// WaitTask must ride out an API outage rather than abandon a task that is
// still running on the node: a 502 from the proxy in front of pveproxy is not
// the task failing.
func TestWaitTaskRidesOutGatewayErrors(t *testing.T) {
	withFastPolling(t)
	var calls atomic.Int32
	c := waitClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"status": "stopped", "exitstatus": "OK"},
		})
	}, nil)
	if err := c.WaitTask(context.Background(), "UPID:node1:00001234:1A2B3C4D:5E6F7A8B:qmstart:100:user@pve!token:"); err != nil {
		t.Fatalf("WaitTask: %v", err)
	}
}

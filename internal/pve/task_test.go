package pve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// withFastPolling shrinks WaitTask's poll interval for the duration of a test
// so a test that needs a few loop iterations does not burn real wall-clock
// seconds, restoring the real values on cleanup.
func withFastPolling(t *testing.T) {
	t.Helper()
	origInterval, origMax := waitTaskPollInterval, waitTaskMaxPollInterval
	waitTaskPollInterval = time.Millisecond
	waitTaskMaxPollInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		waitTaskPollInterval, waitTaskMaxPollInterval = origInterval, origMax
	})
}

func TestParseUPID(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    UPID
		wantErr bool
	}{
		{
			name: "8-hex pstart",
			raw:  "UPID:node1:00001234:1A2B3C4D:5E6F7A8B:qmcreate:100:user@pve!token:",
			want: UPID{
				Raw:  "UPID:node1:00001234:1A2B3C4D:5E6F7A8B:qmcreate:100:user@pve!token:",
				Node: "node1",
				Type: "qmcreate",
				ID:   "100",
				User: "user@pve!token",
			},
		},
		{
			// pstart is 9 hex digits on hosts with uptime over 497 days; the
			// element count and every OTHER field's position must still parse.
			name: "9-hex pstart on a long-uptime host",
			raw:  "UPID:node1:00001234:1A2B3C4DE:5E6F7A8B:qmdestroy:100:user@pve!token:",
			want: UPID{
				Raw:  "UPID:node1:00001234:1A2B3C4DE:5E6F7A8B:qmdestroy:100:user@pve!token:",
				Node: "node1",
				Type: "qmdestroy",
				ID:   "100",
				User: "user@pve!token",
			},
		},
		{
			name: "empty id",
			raw:  "UPID:node1:00001234:1A2B3C4D:5E6F7A8B:vzdump::user@pve!token:",
			want: UPID{
				Raw:  "UPID:node1:00001234:1A2B3C4D:5E6F7A8B:vzdump::user@pve!token:",
				Node: "node1",
				Type: "vzdump",
				ID:   "",
				User: "user@pve!token",
			},
		},
		{
			name:    "missing trailing colon",
			raw:     "UPID:node1:00001234:1A2B3C4D:5E6F7A8B:qmcreate:100:user@pve!token",
			wantErr: true,
		},
		{
			name:    "wrong prefix",
			raw:     "NOTUPID:node1:00001234:1A2B3C4D:5E6F7A8B:qmcreate:100:user@pve!token:",
			wantErr: true,
		},
		{
			name:    "not a upid at all",
			raw:     "garbage",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseUPID(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseUPID(%q) = %+v, nil; want error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseUPID(%q) unexpected error: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("ParseUPID(%q) = %+v; want %+v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestTaskSucceeded(t *testing.T) {
	tests := []struct {
		exitStatus string
		want       bool
	}{
		{"OK", true},
		{"WARNINGS: 3", true},
		{"WARNINGS: 0", true},
		{"WARNINGS: 42", true},
		{"", false},
		{"job errors", false},
		{"WARNINGS:3", false},   // no space: does not match the exact grammar
		{"WARNINGS: -1", false}, // not \d+
	}

	for _, tt := range tests {
		if got := taskSucceeded(tt.exitStatus); got != tt.want {
			t.Errorf("taskSucceeded(%q) = %v; want %v", tt.exitStatus, got, tt.want)
		}
	}
}

// waitClient builds a Client for WaitTask tests, pointed at an
// httptest.NewTLSServer running statusHandler for the /status endpoint and
// logHandler (which may be nil) for the /log endpoint.
func waitClient(t *testing.T, statusHandler, logHandler http.HandlerFunc) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/nodes/node1/tasks/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/log") {
			if logHandler != nil {
				logHandler(w, r)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		statusHandler(w, r)
	})
	ts := httptest.NewTLSServer(mux)
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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(body)
	_, _ = w.Write(b)
}

func TestWaitTaskOKIsSuccess(t *testing.T) {
	c := waitClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"status": "stopped", "exitstatus": "OK"},
		})
	}, nil)

	if err := c.WaitTask(context.Background(), "UPID:node1:00001234:1A2B3C4D:5E6F7A8B:qmcreate:100:user@pve!token:"); err != nil {
		t.Fatalf("WaitTask: %v", err)
	}
}

func TestWaitTaskWarningsIsSuccess(t *testing.T) {
	c := waitClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"status": "stopped", "exitstatus": "WARNINGS: 3"},
		})
	}, nil)

	if err := c.WaitTask(context.Background(), "UPID:node1:00001234:1A2B3C4D:5E6F7A8B:qmcreate:100:user@pve!token:"); err != nil {
		t.Fatalf("WaitTask: %v", err)
	}
}

func TestWaitTaskArbitraryExitStatusIsFailureCarryingText(t *testing.T) {
	c := waitClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"status": "stopped", "exitstatus": "unable to create VM: config error"},
		})
	}, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("limit"); got != "1000" {
			t.Errorf("log request limit = %q; want 1000 (default 50 truncates before TASK ERROR)", got)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": []map[string]any{
				{"n": 1, "t": "starting task"},
				{"n": 2, "t": "TASK ERROR: unable to create VM: config error"},
			},
		})
	})

	err := c.WaitTask(context.Background(), "UPID:node1:00001234:1A2B3C4D:5E6F7A8B:qmcreate:100:user@pve!token:")
	if err == nil {
		t.Fatal("WaitTask: expected an error")
	}
	if !strings.Contains(err.Error(), "unable to create VM: config error") {
		t.Errorf("err.Error() = %q; want it to contain the exitstatus text", err.Error())
	}
	if !strings.Contains(err.Error(), "TASK ERROR: unable to create VM: config error") {
		t.Errorf("err.Error() = %q; want it to contain the folded-in log tail", err.Error())
	}
}

func TestWaitTaskTransient400WithUPIDKeyRetriesThenSucceeds(t *testing.T) {
	withFastPolling(t)

	var calls atomic.Int32
	c := waitClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"data":    nil,
				"message": "parameter verification failed",
				"errors":  map[string]string{"upid": "no such task"},
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"status": "stopped", "exitstatus": "OK"},
		})
	}, nil)

	if err := c.WaitTask(context.Background(), "UPID:node1:00001234:1A2B3C4D:5E6F7A8B:qmcreate:100:user@pve!token:"); err != nil {
		t.Fatalf("WaitTask: %v", err)
	}
	if got := calls.Load(); got < 2 {
		t.Errorf("status endpoint called %d times; want at least 2 (one transient 400, one success)", got)
	}
}

func TestWaitTaskGenuinelyMalformed400IsNotRetried(t *testing.T) {
	withFastPolling(t)

	var calls atomic.Int32
	c := waitClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"data":    nil,
			"message": "parameter verification failed",
			"errors":  map[string]string{"somethingelse": "bad value"},
		})
	}, nil)

	err := c.WaitTask(context.Background(), "UPID:node1:00001234:1A2B3C4D:5E6F7A8B:qmcreate:100:user@pve!token:")
	if err == nil {
		t.Fatal("WaitTask: expected an error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("status endpoint called %d times; want exactly 1 (a 400 without the upid key must not be retried)", got)
	}
}

func TestWaitTaskProxyArtifactsAreTransient(t *testing.T) {
	withFastPolling(t)

	for _, code := range []int{596, 599} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var calls atomic.Int32
			c := waitClient(t, func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.WriteHeader(code)
					_, _ = w.Write([]byte(""))
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"data": map[string]any{"status": "stopped", "exitstatus": "OK"},
				})
			}, nil)

			if err := c.WaitTask(context.Background(), "UPID:node1:00001234:1A2B3C4D:5E6F7A8B:qmcreate:100:user@pve!token:"); err != nil {
				t.Fatalf("WaitTask: %v", err)
			}
			if got := calls.Load(); got < 2 {
				t.Errorf("status endpoint called %d times; want at least 2", got)
			}
		})
	}
}

func TestWaitTaskPermissionErrorAbortsImmediately(t *testing.T) {
	withFastPolling(t)

	var calls atomic.Int32
	c := waitClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"data":null}`))
	}, nil)

	err := c.WaitTask(context.Background(), "UPID:node1:00001234:1A2B3C4D:5E6F7A8B:qmcreate:100:user@pve!token:")
	if err == nil {
		t.Fatal("WaitTask: expected an error")
	}
	if !IsPermission(err) {
		t.Errorf("IsPermission(err) = false; want true")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("status endpoint called %d times; want exactly 1 (a permission error must never be retried)", got)
	}
}

func TestWaitTaskHonoursContextCancellation(t *testing.T) {
	withFastPolling(t)

	c := waitClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"status": "running"},
		})
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := c.WaitTask(ctx, "UPID:node1:00001234:1A2B3C4D:5E6F7A8B:qmcreate:100:user@pve!token:")
	if err == nil {
		t.Fatal("WaitTask: expected an error from context cancellation")
	}
}

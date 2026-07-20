package pve

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func agentTestClient(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *Client {
	t.Helper()
	ts := httptest.NewTLSServer(http.HandlerFunc(handler))
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

// --- Method (GET vs POST) per agent command, and the double-nested result envelope ---

func TestAgentPingUsesPOST(t *testing.T) {
	var gotMethod, gotPath string
	c := agentTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"result":{}}}`))
	})

	if err := c.AgentPing(context.Background(), 100); err != nil {
		t.Fatalf("AgentPing: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q; want POST", gotMethod)
	}
	if gotPath != "/api2/json/nodes/node1/qemu/100/agent/ping" {
		t.Errorf("path = %q", gotPath)
	}
}

func TestAgentFsfreezeStatusUsesPOST(t *testing.T) {
	var gotMethod string
	c := agentTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"result":"thawed"}}`))
	})

	got, err := c.AgentFsfreezeStatus(context.Background(), 100)
	if err != nil {
		t.Fatalf("AgentFsfreezeStatus: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q; want POST", gotMethod)
	}
	if got != "thawed" {
		t.Errorf("result = %q; want thawed (double-nested data.result must be unwrapped)", got)
	}
}

func TestAgentNetworkGetInterfacesUsesGET(t *testing.T) {
	var gotMethod string
	c := agentTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"result":[
			{"name":"eth0","hardware-address":"aa:bb:cc:dd:ee:ff","ip-addresses":[
				{"ip-address":"10.0.0.5","ip-address-type":"ipv4","prefix":24}
			]}
		]}}`))
	})

	ifaces, err := c.AgentNetworkGetInterfaces(context.Background(), 100)
	if err != nil {
		t.Fatalf("AgentNetworkGetInterfaces: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q; want GET", gotMethod)
	}
	if len(ifaces) != 1 || ifaces[0].HardwareAddress != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("ifaces = %+v", ifaces)
	}
	if len(ifaces[0].IPAddresses) != 1 || ifaces[0].IPAddresses[0].IPAddress != "10.0.0.5" {
		t.Fatalf("ip addresses = %+v; hyphenated keys must map correctly", ifaces[0].IPAddresses)
	}
}

func TestAgentGetFsinfoUsesGET(t *testing.T) {
	var gotMethod string
	c := agentTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"result":[
			{"name":"sda1","mountpoint":"/","type":"ext4","used-bytes":1000,"total-bytes":2000}
		]}}`))
	})

	fs, err := c.AgentGetFsinfo(context.Background(), 100)
	if err != nil {
		t.Fatalf("AgentGetFsinfo: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q; want GET", gotMethod)
	}
	if len(fs) != 1 || fs[0].Mountpoint != "/" || fs[0].TotalBytes != 2000 {
		t.Fatalf("fs = %+v", fs)
	}
}

func TestAgentExecStatusUsesGET(t *testing.T) {
	var gotMethod string
	var gotPID string
	c := agentTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPID = r.URL.Query().Get("pid")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"result":{"exited":true,"exitcode":0,"out-data":"hi"}}}`))
	})

	st, err := c.AgentExecStatus(context.Background(), 100, 4242)
	if err != nil {
		t.Fatalf("AgentExecStatus: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q; want GET", gotMethod)
	}
	if gotPID != "4242" {
		t.Errorf("pid query = %q; want 4242", gotPID)
	}
	if !st.Exited || st.ExitCode != 0 || st.OutData != "hi" {
		t.Fatalf("status = %+v", st)
	}
}

func TestAgentExecUsesPOSTAndSendsArrayCommand(t *testing.T) {
	var gotMethod string
	var gotCommand []string
	c := agentTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		gotCommand = r.PostForm["command"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"result":{"pid":555}}}`))
	})

	pid, err := c.AgentExec(context.Background(), 100, []string{"cloud-init", "status", "--wait"}, "")
	if err != nil {
		t.Fatalf("AgentExec: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q; want POST", gotMethod)
	}
	if pid != 555 {
		t.Errorf("pid = %d; want 555", pid)
	}
	if len(gotCommand) != 3 || gotCommand[1] != "status" {
		t.Fatalf("command = %v", gotCommand)
	}
}

// --- AgentUnavailableReason classification ---

func TestAgentUnavailableReason(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil error", nil, ""},
		{"not configured", &APIError{Status: 500, Message: "No QEMU guest agent configured"}, "not-configured"},
		{"vm stopped", &APIError{Status: 500, Message: "VM 100 is not running"}, "vm-stopped"},
		{"agent down", &APIError{Status: 500, Message: "guest agent is not running"}, "agent-down"},
		{"timeout", &APIError{Status: 500, Message: "got timeout waiting for guest agent"}, "timeout"},
		{"unrecognized", &APIError{Status: 500, Message: "something else entirely"}, ""},
		{"wrapped", fmt404Wrap(&APIError{Status: 500, Message: "No QEMU guest agent configured"}), "not-configured"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AgentUnavailableReason(tt.err); got != tt.want {
				t.Errorf("AgentUnavailableReason(%v) = %q; want %q", tt.err, got, tt.want)
			}
		})
	}
}

// fmt404Wrap wraps err the way a caller might (e.g. with additional context),
// to confirm AgentUnavailableReason's text classification still works on
// err.Error() through a wrap.
func fmt404Wrap(err error) error {
	return errors.New("checking guest agent: " + err.Error())
}

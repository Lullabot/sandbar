package ui

// connlog_test.go pins the connection-lifecycle lines in the session message
// log: a REMOTE member announces connecting / connected / reconnecting /
// reconnected as its state transitions, a deliberate disable announces the
// disconnect for any profile type, and the LOCAL member's automatic lifecycle
// stays silent (it is the machine sand runs on, not a connection — and the
// zero-config board must stay bit-identical to the pre-profiles TUI).

import (
	"errors"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// messageLog flattens the model's session log for substring assertions.
func messageLog(m model) string {
	var b strings.Builder
	for _, msg := range m.messages {
		b.WriteString(msg.text)
		b.WriteString("\n")
	}
	return b.String()
}

func TestRemoteConnectionLifecycleLogsToMessages(t *testing.T) {
	isolateHostState(t)
	m := New(twoMemberFleet(&providerfake.Provider{}, &providerfake.Provider{})).(model)
	m = resized(m, 120, 40)

	// Startup: the remote member's connection attempt is announced; the local
	// member's is not.
	if log := messageLog(m); !strings.Contains(log, "connecting to build-host") {
		t.Fatalf("startup did not announce the remote connection attempt:\n%s", log)
	} else if strings.Contains(log, "connecting to local") {
		t.Fatalf("startup announced the LOCAL member — it must stay silent:\n%s", log)
	}

	// First successful list => "connected".
	nm, _ := m.Update(vmsLoadedMsg{scope: remoteScope, vms: []vm.VM{}})
	m = nm.(model)
	if log := messageLog(m); !strings.Contains(log, "connected to build-host") {
		t.Fatalf("first successful list did not log connected:\n%s", log)
	}

	// A steady-state refresh must NOT repeat it.
	before := len(m.messages)
	nm, _ = m.Update(vmsLoadedMsg{scope: remoteScope, vms: []vm.VM{}})
	m = nm.(model)
	for _, msg := range m.messages[before:] {
		if strings.Contains(msg.text, "connected to build-host") {
			t.Fatalf("steady-state refresh re-logged the connected line: %q", msg.text)
		}
	}

	// An interruption (connected -> errored) => "reconnecting".
	nm, _ = m.Update(vmsLoadedMsg{scope: remoteScope, err: errors.New("broken pipe")})
	m = nm.(model)
	if log := messageLog(m); !strings.Contains(log, "reconnecting to build-host") {
		t.Fatalf("interruption did not log reconnecting:\n%s", log)
	}

	// A further failure while ALREADY errored must not repeat "reconnecting".
	before = len(m.messages)
	nm, _ = m.Update(vmsLoadedMsg{scope: remoteScope, err: errors.New("still broken")})
	m = nm.(model)
	for _, msg := range m.messages[before:] {
		if strings.Contains(msg.text, "reconnecting to build-host") {
			t.Fatalf("repeated failure re-logged reconnecting: %q", msg.text)
		}
	}

	// Recovery (errored -> connected) => "reconnected".
	nm, _ = m.Update(vmsLoadedMsg{scope: remoteScope, vms: []vm.VM{}})
	m = nm.(model)
	if log := messageLog(m); !strings.Contains(log, "reconnected to build-host") {
		t.Fatalf("recovery did not log reconnected:\n%s", log)
	}
}

func TestLocalLifecycleStaysSilentButDisableLogs(t *testing.T) {
	isolateHostState(t)
	m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)
	m = resized(m, 120, 40)

	// The zero-config local fleet logs no connection lifecycle at all —
	// startup, first list, steady state.
	nm, _ := m.Update(vmsLoadedMsg{scope: registry.LocalScope, vms: []vm.VM{}})
	m = nm.(model)
	for _, msg := range m.messages {
		if strings.Contains(strings.ToLower(msg.text), "connect") {
			t.Fatalf("local member logged connection lifecycle: %q", msg.text)
		}
	}

	// But a DELIBERATE disable of any profile announces the disconnect. New()
	// loaded (and seeded) the profile store from the isolated XDG env, so the
	// permanent Local profile is present under its fixed id.
	m.disableProfile(profiles.LocalProfileID)
	if m.profileMsg != "" {
		t.Fatalf("disabling the idle local profile was refused: %q", m.profileMsg)
	}
	if log := messageLog(m); !strings.Contains(log, "disconnected from") {
		t.Fatalf("disable did not log a disconnect:\n%s", log)
	}
}

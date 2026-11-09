package ui

import (
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// A backend advisory (a known-affected Lima release) is read only on the
// refresh that ran Preflight — never on the steady 5s refresh, which must not
// grow a probe — and it reaches the Messages log as ONE warning however many
// times the member reconnects.
func TestBackendAdvisoryIsLoggedOnceFromPreflight(t *testing.T) {
	calls := 0
	p := &providerfake.Provider{
		ListFunc: func() ([]vm.VM, error) { return nil, nil },
		AdvisoriesFunc: func() []string {
			calls++
			return []string{"Lima 2.3.0: `limactl start` may fail intermittently"}
		},
	}

	if msg := refreshCmd(registry.LocalScope, p, nil, false)().(vmsLoadedMsg); len(msg.advisories) != 0 || calls != 0 {
		t.Fatalf("a refresh without Preflight asked for advisories (%d calls, %v)", calls, msg.advisories)
	}
	msg := refreshCmd(registry.LocalScope, p, nil, true)().(vmsLoadedMsg)
	if len(msg.advisories) != 1 {
		t.Fatalf("preflight refresh advisories = %v, want one", msg.advisories)
	}

	m := newTestModel(t)
	for i := 0; i < 3; i++ { // reconnects re-run Preflight and resend it
		next, _ := m.Update(msg)
		m = next.(model)
	}
	if n := countMessages(m, "limactl start` may fail"); n != 1 {
		t.Fatalf("advisory logged %d times, want once:\n%v", n, m.messages)
	}
	for _, e := range m.messages {
		if strings.Contains(e.text, "Lima 2.3.0") && !e.warn {
			t.Fatalf("advisory was logged as a plain message, want a warning: %q", e.text)
		}
	}
}

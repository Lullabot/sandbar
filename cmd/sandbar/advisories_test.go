package main

import (
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/providerfake"
)

// warnAdvisories prints each advisory Preflight learned as a stderr warning, and
// nothing for a provider that has none.
func TestWarnAdvisories(t *testing.T) {
	var buf strings.Builder
	warnAdvisories(&buf, &providerfake.Provider{AdvisoriesFunc: func() []string {
		return []string{"Lima 2.3.0: limactl start may fail intermittently"}
	}})
	if got, want := buf.String(), "warning: Lima 2.3.0: limactl start may fail intermittently\n"; got != want {
		t.Fatalf("warnAdvisories wrote %q, want %q", got, want)
	}
	buf.Reset()
	warnAdvisories(&buf, &providerfake.Provider{})
	if buf.Len() != 0 {
		t.Fatalf("no advisories must print nothing, got %q", buf.String())
	}
}

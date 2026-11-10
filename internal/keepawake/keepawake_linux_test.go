//go:build linux

package keepawake

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestLiveLogindInhibitor takes a real logind inhibitor and checks
// systemd-inhibit sees it come and go. It changes the host's power state for
// its duration, needs a system bus, and is meaningless in CI containers, so it
// runs only when asked: SANDBAR_KEEPAWAKE_LIVE=1 go test ./internal/keepawake/
func TestLiveLogindInhibitor(t *testing.T) {
	if os.Getenv("SANDBAR_KEEPAWAKE_LIVE") == "" {
		t.Skip("set SANDBAR_KEEPAWAKE_LIVE=1 to take a real logind inhibitor")
	}
	const reason = "sandbar keepawake live test"
	list := func() string {
		out, err := exec.Command("systemd-inhibit", "--list", "--no-pager").CombinedOutput()
		if err != nil {
			t.Fatalf("systemd-inhibit --list: %v\n%s", err, out)
		}
		return string(out)
	}

	rel, err := Hold(reason)
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	held := list()
	t.Logf("while held:\n%s", held)
	if !strings.Contains(held, reason) {
		rel()
		t.Fatalf("inhibitor %q not listed while held", reason)
	}

	rel()
	after := list()
	t.Logf("after release:\n%s", after)
	if strings.Contains(after, reason) {
		t.Fatalf("inhibitor %q still listed after release", reason)
	}
}

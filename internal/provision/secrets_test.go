package provision

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
)

// TestApplySecrets_SecretNeverOnArgv is the load-bearing security property: the
// rendered secret text must be streamed over stdin only. It must never appear in
// any element of the command's argv, where a host `ps` would leak it to every
// user on the machine.
func TestApplySecrets_SecretNeverOnArgv(t *testing.T) {
	const secret = "SENTINEL_SECRET_VALUE"
	f := &fakeRunner{}
	cli := lima.New(f)

	if err := ApplySecrets(context.Background(), cli, "claude", "andrew", map[string]string{"TOK": secret}, io.Discard); err != nil {
		t.Fatalf("ApplySecrets: %v", err)
	}

	// Exactly one shell call, streaming exactly one stdin.
	if len(f.calls) != 1 {
		t.Fatalf("want exactly 1 limactl call, got %d: %v", len(f.calls), f.calls)
	}
	if len(f.streams) != 1 {
		t.Fatalf("want exactly 1 streamed stdin, got %d", len(f.streams))
	}

	// The secret MUST be on stdin (the file body streamed into the guest).
	if !strings.Contains(f.streams[0], secret) {
		t.Fatalf("secret must appear in the streamed stdin; stdin=%q", f.streams[0])
	}
	// The rendered, single-quote-wrapped export line is what lands on stdin.
	if want := "export TOK='" + secret + "'\n"; !strings.Contains(f.streams[0], want) {
		t.Fatalf("stdin missing rendered export line %q; stdin=%q", want, f.streams[0])
	}

	// The secret MUST NOT appear on argv — not in any single element, and not in
	// the joined argv (the exact form the acceptance criterion names).
	argv := f.calls[0]
	for _, a := range argv {
		if strings.Contains(a, secret) {
			t.Fatalf("secret leaked onto an argv element %q; full argv=%v", a, argv)
		}
	}
	if strings.Contains(strings.Join(argv, " "), secret) {
		t.Fatalf("secret leaked into the joined argv: %v", argv)
	}

	// Sanity on the transport: it is a guest shell that writes the file from stdin
	// as the target user, with the 0600 install-before-write hygiene.
	joined := strings.Join(argv, " ")
	if !hasTok(argv, "shell") || !hasTok(argv, "claude") {
		t.Fatalf("expected a `shell claude ...` call, got %v", argv)
	}
	if !hasTok(argv, "andrew") {
		t.Fatalf("expected the target user on argv, got %v", argv)
	}
	if !strings.Contains(joined, "install -m 600 /dev/null") {
		t.Fatalf("apply script must create the file 0600 before writing; argv=%v", argv)
	}
	if !strings.Contains(joined, "cat > ") {
		t.Fatalf("apply script must stream the body via cat; argv=%v", argv)
	}
	if !strings.Contains(joined, "secrets.env") || !strings.Contains(joined, ".config/sandbar") {
		t.Fatalf("apply script must target ~/.config/sandbar/secrets.env; argv=%v", argv)
	}
}

// TestApplySecrets_SudoDoesNotUseLoginShell guards a bug found only against a
// live guest: `sudo -i` runs the target user's LOGIN shell, which re-parses the
// command, so the multi-line script arrived mangled ("set: -c: invalid option")
// and the write silently failed. `sudo -H -u` sets HOME without that extra shell
// layer. A fake runner cannot catch this by executing the script, so pin the
// flags instead — for both the apply and the clear path.
func TestApplySecrets_SudoDoesNotUseLoginShell(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pairs map[string]string
	}{
		{"apply", map[string]string{"TOK": "v"}},
		{"clear", map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{}
			if err := ApplySecrets(context.Background(), lima.New(f), "claude", "andrew", tc.pairs, io.Discard); err != nil {
				t.Fatalf("ApplySecrets: %v", err)
			}
			argv := f.calls[0]
			for _, bad := range []string{"-i", "-iu"} {
				if hasTok(argv, bad) {
					t.Fatalf("sudo %s runs a login shell that re-parses the script and mangles it; argv=%v", bad, argv)
				}
			}
			if !hasTok(argv, "-H") || !hasTok(argv, "-u") {
				t.Fatalf("expected `sudo -H -u <user>` so HOME is the target user's home; argv=%v", argv)
			}
		})
	}
}

// TestApplySecrets_EmptyRemovesGuestFile: with no pairs, ApplySecrets removes the
// guest file (rm -f) instead of writing an empty one, and streams no stdin.
func TestApplySecrets_EmptyRemovesGuestFile(t *testing.T) {
	f := &fakeRunner{}
	cli := lima.New(f)

	if err := ApplySecrets(context.Background(), cli, "claude", "andrew", map[string]string{}, io.Discard); err != nil {
		t.Fatalf("ApplySecrets: %v", err)
	}

	if len(f.calls) != 1 {
		t.Fatalf("want exactly 1 limactl call, got %d: %v", len(f.calls), f.calls)
	}
	argv := f.calls[0]
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "rm -f") || !strings.Contains(joined, "secrets.env") {
		t.Fatalf("empty apply must run the rm -f clear script for secrets.env; argv=%v", argv)
	}
	// It must NOT run the write path.
	if strings.Contains(joined, "cat > ") || strings.Contains(joined, "install -m 600") {
		t.Fatalf("empty apply must not run the write script; argv=%v", argv)
	}
	// The clear path streams no stdin (fakeRunner only records non-nil stdin).
	if len(f.streams) != 0 {
		t.Fatalf("clear path must not stream any stdin, got %d: %v", len(f.streams), f.streams)
	}
}

// TestApplySecrets_NilPairsRemovesGuestFile: a nil map is treated the same as an
// empty one — the guest file is removed, nothing is streamed.
func TestApplySecrets_NilPairsRemovesGuestFile(t *testing.T) {
	f := &fakeRunner{}
	cli := lima.New(f)

	if err := ApplySecrets(context.Background(), cli, "claude", "andrew", nil, io.Discard); err != nil {
		t.Fatalf("ApplySecrets(nil): %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("want 1 call, got %d: %v", len(f.calls), f.calls)
	}
	if !strings.Contains(strings.Join(f.calls[0], " "), "rm -f") {
		t.Fatalf("nil pairs must run the clear script; argv=%v", f.calls[0])
	}
	if len(f.streams) != 0 {
		t.Fatalf("nil pairs must not stream stdin, got %v", f.streams)
	}
}

package lima

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestMarkerScanScriptRoundTrip runs the REAL remote script (markerScanScript,
// the emitting half of ReadInstanceMarkers) through a real /bin/sh and feeds
// its bytes to the REAL parser (parseMarkerStream, the decoding half), then
// requires the result to match what localFiles.ReadInstanceMarkers returns for
// the same tree.
//
// This test exists because the two halves were previously tested only in
// isolation, each against a hand-written model of the wire format rather than
// against each other: markerFrame (sshhost_test.go) built name/length/payload
// records the way the parser wanted them, and the real-ssh e2e tests only ever
// exercised the SINGLE-marker read (ProvenanceOf, which is a plain `cat`),
// never the batched one. So a script that emitted an EXTRA newline between the
// length line and the payload satisfied every test while producing, on a real
// host, a stream the parser misframed by one byte — corrupting the first
// marker and then failing the whole batch. A failed batch degrades silently to
// the legacy per-controller registry (internal/ui/commands.go), which is
// exactly the divergence plan 17 set out to remove: each controller saw only
// the VMs it had created itself.
//
// Comparing against localFiles is the point: local and remote are two
// implementations of one HostFiles contract, so the batched read must produce
// identical results whichever transport answers it.
func TestMarkerScanScriptRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh available")
	}
	home := t.TempDir()

	// Payloads chosen to catch the failure modes the framing exists for: a
	// realistic marker, one with an embedded newline (the reason the format is
	// length-framed and not line-delimited), and an empty file (a zero length
	// must not desynchronize the stream).
	markers := map[string]string{
		"created-from-silo":   `{"schema":2,"base":"sandbar-base","config":{"Name":"created-from-silo"},"created_at":"2026-07-20T13:09:36Z"}`,
		"created-from-acutus": "{\"schema\":2,\n\"provisioning\":true}\n",
		"empty-marker":        "",
	}
	for name, body := range markers {
		dir := filepath.Join(home, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, MarkerFilename), []byte(body), 0o600); err != nil {
			t.Fatalf("write marker %s: %v", name, err)
		}
	}
	// A directory with no marker and a stray top-level file: the script must
	// skip both rather than emit a record or abort the scan.
	if err := os.MkdirAll(filepath.Join(home, "unmanaged"), 0o700); err != nil {
		t.Fatalf("mkdir unmanaged: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "stray.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write stray: %v", err)
	}

	cmd := exec.Command("sh", "-c", markerScanScript, "sand", home, MarkerFilename)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("marker scan script: %v (stderr: %s)", err, stderr.String())
	}

	got, err := parseMarkerStream(stdout.Bytes())
	if err != nil {
		t.Fatalf("parseMarkerStream on REAL script output: %v\nraw stream:\n%q", err, stdout.String())
	}

	want, err := LocalFiles().ReadInstanceMarkers(t.Context(), home, MarkerFilename)
	if err != nil {
		t.Fatalf("localFiles.ReadInstanceMarkers: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("script+parser returned %d markers, localFiles returned %d\ngot:  %q\nwant: %q", len(got), len(want), got, want)
	}
	for name, wantBody := range want {
		gotBody, ok := got[name]
		if !ok {
			t.Fatalf("script+parser dropped marker %q that localFiles returned", name)
		}
		if !bytes.Equal(gotBody, wantBody) {
			t.Fatalf("marker %q mismatch between transports:\n got: %q\nwant: %q", name, gotBody, wantBody)
		}
	}
}

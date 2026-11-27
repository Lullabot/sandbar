package main

import (
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

func writeTokenFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile is subject to the umask; set the mode exactly.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func setTokenStdin(t *testing.T, s string) {
	t.Helper()
	old := cloneTokenStdin
	cloneTokenStdin = strings.NewReader(s)
	t.Cleanup(func() { cloneTokenStdin = old })
}

func TestReadCloneTokenFile(t *testing.T) {
	for _, tc := range []struct{ name, content, want string }{
		{"plain", "ghp_abc", "ghp_abc"},
		{"one trailing newline", "ghp_abc\n", "ghp_abc"},
		{"crlf", "ghp_abc\r\n", "ghp_abc"},
		{"only one newline trimmed", "ghp_abc\n\n", "ghp_abc\n"},
		{"inner and leading space kept", " a b\n", " a b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readCloneTokenFile(writeTokenFile(t, tc.content, 0o600), nil)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestReadCloneTokenFileRefusals(t *testing.T) {
	const secret = "ghp_must_not_leak"
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o660} {
		_, err := readCloneTokenFile(writeTokenFile(t, secret, mode), nil)
		if err == nil || !strings.Contains(err.Error(), "must not be readable by group or other") {
			t.Fatalf("mode %04o: err = %v, want a permission refusal", mode, err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatal("error leaked the token")
		}
	}
	if _, err := readCloneTokenFile(filepath.Join(t.TempDir(), "missing"), nil); err == nil {
		t.Fatal("missing file: want error")
	}
	if _, err := readCloneTokenFile(t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("directory: err = %v, want not-a-regular-file", err)
	}
	for _, empty := range []string{"", "\n"} {
		if _, err := readCloneTokenFile(writeTokenFile(t, empty, 0o600), nil); err == nil || !strings.Contains(err.Error(), "empty") {
			t.Fatalf("empty %q: err = %v", empty, err)
		}
	}
}

func TestReadCloneTokenFromStdin(t *testing.T) {
	got, err := readCloneTokenFile("-", strings.NewReader("ghp_stdin\n"))
	if err != nil || got != "ghp_stdin" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := readCloneTokenFile("-", strings.NewReader("")); err == nil {
		t.Fatal("empty stdin: want error")
	}
	if _, err := readCloneTokenFile("-", strings.NewReader(strings.Repeat("x", maxCloneTokenBytes+1))); err == nil {
		t.Fatal("oversized stdin: want error")
	}
}

func TestResolveCloneTokenFlagCombinations(t *testing.T) {
	if _, err := resolveCloneToken("a", "/some/file", true, true); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("both flags: err = %v", err)
	}
	if got, err := resolveCloneToken("literal", "", true, false); err != nil || got != "literal" {
		t.Fatalf("--clone-token alone: %q, %v", got, err)
	}
	if got, err := resolveCloneToken("", "", false, false); err != nil || got != "" {
		t.Fatalf("neither flag: %q, %v", got, err)
	}
	if _, err := resolveCloneToken("", "", false, true); err == nil {
		t.Fatal("empty --clone-token-file path: want error")
	}
}

func runCreateWithToken(t *testing.T, extra ...string) error {
	t.Helper()
	isolateCreateState(t)
	p := &providerfake.Provider{HostFilesFunc: lima.LocalFiles}
	args := createAgentArgs("tokvm", append([]string{"--clone-url", "https://github.com/acme/web"}, extra...)...)
	return runCreateWithBinding(args, bindCreateFake(p))
}

// The token must land in the same place --clone-token puts it: the config the
// provider's Create receives.
func TestCreateCloneTokenFileReachesProvider(t *testing.T) {
	isolateCreateState(t)
	var got vm.CreateConfig
	p := &providerfake.Provider{
		HostFilesFunc: lima.LocalFiles,
		CreateFunc: func(_ context.Context, cfg vm.CreateConfig, _ provision.CreateOptions, _ io.Writer) error {
			got = cfg
			return nil
		},
	}
	url := []string{"--clone-url", "https://github.com/acme/web"}
	path := writeTokenFile(t, "ghp_from_file\n", 0o600)
	if err := runCreateWithBinding(createAgentArgs("tokvm", append(url, "--clone-token-file", path)...), bindCreateFake(p)); err != nil {
		t.Fatal(err)
	}
	if got.CloneToken != "ghp_from_file" {
		t.Fatalf("file: provider saw CloneToken %q", got.CloneToken)
	}

	got = vm.CreateConfig{}
	setTokenStdin(t, "ghp_from_stdin\n")
	if err := runCreateWithBinding(createAgentArgs("tokvm2", append(url, "--clone-token-file", "-")...), bindCreateFake(p)); err != nil {
		t.Fatal(err)
	}
	if got.CloneToken != "ghp_from_stdin" {
		t.Fatalf("stdin: provider saw CloneToken %q", got.CloneToken)
	}

	got = vm.CreateConfig{}
	if err := runCreateWithBinding(createAgentArgs("tokvm3", append(url, "--clone-token", "ghp_literal")...), bindCreateFake(p)); err != nil {
		t.Fatal(err)
	}
	if got.CloneToken != "ghp_literal" {
		t.Fatalf("--clone-token still works: provider saw %q", got.CloneToken)
	}
}

func TestCreateCloneTokenFileRefusals(t *testing.T) {
	good := writeTokenFile(t, "ghp_x", 0o600)
	if err := runCreateWithToken(t, "--clone-token", "a", "--clone-token-file", good); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("both flags: err = %v", err)
	}
	loose := writeTokenFile(t, "ghp_x", 0o644)
	if err := runCreateWithToken(t, "--clone-token-file", loose); err == nil || !strings.Contains(err.Error(), "group or other") {
		t.Fatalf("0644 file: err = %v", err)
	}
}

func TestResetCloneTokenFile(t *testing.T) {
	// Both flags and a loose file are refused by the real entrypoint, before
	// any backend is contacted.
	isolateCreateState(t)
	good := writeTokenFile(t, "ghp_x", 0o600)
	if err := runReset([]string{"web", "--clone-token", "a", "--clone-token-file", good}); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("both flags: err = %v", err)
	}
	loose := writeTokenFile(t, "ghp_x", 0o644)
	if err := runReset([]string{"web", "--clone-token-file", loose}); err == nil || !strings.Contains(err.Error(), "group or other") {
		t.Fatalf("0644 file: err = %v", err)
	}

	// The resolved token reaches the config a reset builds, exactly as
	// --clone-token's value does.
	var o resetOptions
	fs := newResetFlagSet(&o)
	if err := fs.Parse(reorderFlags(fs, []string{"web", "--clone-token-file", "-"})); err != nil {
		t.Fatal(err)
	}
	setTokenStdin(t, "ghp_reset\n")
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	tok, err := resolveCloneToken(o.values.cloneToken, o.cloneTokenFile, explicit["clone-token"], explicit["clone-token-file"])
	if err != nil {
		t.Fatal(err)
	}
	o.values.cloneToken = tok
	cfg, err := resetConfigFor(seededRegistry(t), "web", registry.LocalScope, "work-base", explicit, o.values, "hostuser")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CloneToken != "ghp_reset" {
		t.Fatalf("reset config CloneToken = %q", cfg.CloneToken)
	}
}

package provision

import (
	"context"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
)

// localArchiveGuest executes the production tar/chown argv against temporary
// guest homes, dropping only sudo so this test needs no VM or privilege.
type localArchiveGuest struct{}

func (localArchiveGuest) Shell(ctx context.Context, _ string, in io.Reader, out io.Writer, args ...string) error {
	if args[0] == "sudo" {
		args = args[1:]
	}
	if args[0] == "sh" && len(args) > 4 && args[3] == "chown" {
		args[4] = strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	}
	c := exec.CommandContext(ctx, args[0], args[1:]...)
	c.Stdin = in
	c.Stdout = out
	c.Stderr = os.Stderr
	return c.Run()
}
func (g localArchiveGuest) ShellStreamOut(ctx context.Context, n string, in io.Reader, out io.Writer, args ...string) error {
	return g.Shell(ctx, n, in, out, args...)
}
func (localArchiveGuest) ShellOut(ctx context.Context, _ string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, args[0], args[1:]...).Output()
}

func TestAgentArchiveRoundTripWithMissingPaths(t *testing.T) {
	source, dest := t.TempDir(), t.TempDir()
	files := []string{".claude/settings.json", ".claude.json", ".codex/auth.json", ".config/opencode/opencode.json", ".local/share/opencode/auth.json", ".local/state/opencode/state.json", ".pi/agent/auth.json"}
	for _, p := range files {
		full := filepath.Join(source, p)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("secret:"+p), 0600); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(t.TempDir(), "agents.tar")
	paths := append([]string{}, AgentStatePaths...)
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if err := StageOut(context.Background(), localArchiveGuest{}, "vm", source, u.Username, paths, archive, "agent data", io.Discard); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("archive mode %o", info.Mode().Perm())
	}
	if err := StageIn(context.Background(), localArchiveGuest{}, "vm", dest, u.Username, paths, archive, "agent data", io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, p := range files {
		b, err := os.ReadFile(filepath.Join(dest, p))
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != "secret:"+p {
			t.Fatalf("changed bytes %s", p)
		}
		info, _ := os.Stat(filepath.Join(dest, p))
		if info.Mode().Perm() != 0600 {
			t.Fatalf("changed mode %s", p)
		}
	}
}

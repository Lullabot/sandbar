//go:build limae2e

// This file extends the limae2e suite with the full repo-checked-in
// provisioning-profile path (strikethroo plan 18, task 05): `sand create
// --clone-url <fixture>` clones a checked-in fixture repository, the
// `project` role's finalize-phase clone lands it in the guest, and the new
// `repo-profile` role (roles/repo-profile/) discovers its committed
// `.sandbar/profile.yml` and applies every declared group. It also covers the
// toolset-reconciliation and malformed-manifest checks from the plan's Self
// Validation steps 2 and 6, since both reuse this file's fixture-serving
// machinery.
//
// FIXTURE SERVING
// ================
// The fixture (testdata/fixtures/repoprofile/{valid,malformed}/) is committed
// to the sandbar repo as a plain directory tree, not a git repository — it is
// turned into one at test time (buildFixtureRepo) and served with `git
// daemon` (startGitDaemon) over
// git://host.lima.internal:<port>/<org>/<repo>.
//
// The clone URL host is deliberately host.lima.internal, NOT the literal
// "localhost" the plan's prose uses as its example: git daemon runs on the
// HOST (this test binary's own machine), and the guest resolves its own
// "localhost" to ITSELF, not the host — a git://localhost/... URL would
// simply refuse the connection inside the guest. host.lima.internal is
// Lima's own built-in DNS name for the host machine (the QEMU
// usermode/slirp gateway address, confirmed live: `limactl shell <vm> --
// getent hosts host.lima.internal` resolves it, and a host-bound TCP
// service is reachable from the guest through it with zero extra Lima
// config — no networks:/portForwards: overlay needed). It satisfies the
// same intent the plan's "prefer localhost" guidance is actually after:
// this is not a real, public hostname, so the URL never touches the
// project role's GitHub-token-injection branch (gated strictly on
// `_clone_host == "github.com"`, roles/project/tasks/main.yml), and the
// scheme://host/org/repo shape the project role's regex-based URL parsing
// expects is preserved exactly (a raw file:// path would break its
// host/org derivation).
//
// Run (needs limactl + nested virt/KVM; downloads the Debian 13 image once):
//
//	LIMA_E2E=1 go test -tags limae2e -timeout 30m -run E2E ./cmd/sand/
//
// This runs in the `lima-e2e` CI job (.github/workflows/test.yml) as part of
// the SAME `-run E2E ./cmd/sand/` invocation the existing create/recreate
// tests already use — no new CI job or step is needed.
package main

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
)

// repoProfileFixtureRoot is the checked-in fixture tree's root, relative to
// this package's directory. It holds two variants: "valid" (exercises every
// manifest group) and "malformed" (an unknown top-level key, for the
// malformed-manifest check).
const repoProfileFixtureRoot = "testdata/fixtures/repoprofile"

// freePort asks the OS for an ephemeral TCP port and returns it after
// releasing the probe listener. There is an inherent (and in practice
// vanishingly small) TOCTOU race between the release and git daemon's own
// bind — the same trade-off most "find a free port for a test server" helpers
// make, since git daemon itself has no "pick any port and tell me which"
// mode.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// startGitDaemon starts `git daemon` serving every repository under baseDir
// (--export-all, so no per-repo git-daemon-export-ok marker file is needed)
// on a freshly chosen port, registers its teardown via t.Cleanup, waits for
// the port to actually accept connections, and returns the port.
func startGitDaemon(t *testing.T, baseDir string) int {
	t.Helper()

	port := freePort(t)
	cmd := exec.Command("git", "daemon",
		"--reuseaddr",
		"--export-all",
		"--base-path="+baseDir,
		"--listen=0.0.0.0",
		fmt.Sprintf("--port=%d", port),
		baseDir,
	)
	// A REAL *os.File for Stdout/Stderr, not a bytes.Buffer. git daemon forks
	// a fresh child process to serve each connection, and that child
	// inherits these descriptors. With a Go-managed pipe (what exec.Cmd sets
	// up for an io.Writer like a bytes.Buffer), Cmd.Wait() blocks until
	// EVERY process holding the pipe's write end has closed it — including
	// a per-connection child that outlives (or is merely slow to reap after)
	// the daemon itself — which hung this exact cleanup in a real run
	// against this test (goroutine dump: Cmd.Wait() stuck in
	// awaitGoroutines). A plain *os.File hands the descriptor straight to
	// the kernel: exec.Cmd does not build a pipe for it, so Wait() only
	// waits on THIS process's own exit status, never on descriptor closure.
	logPath := filepath.Join(t.TempDir(), "git-daemon.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create git daemon log file: %v", err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Setpgid + a process-group kill (in the Cleanup below) reaches any
	// per-connection child git daemon has already forked, not just the
	// daemon itself — belt-and-braces alongside the *os.File fix above, and
	// what actually stops an orphaned child from lingering at all.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start git daemon: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		_ = cmd.Wait()
		_ = logFile.Close()
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			data, _ := os.ReadFile(logPath)
			t.Fatalf("git daemon on port %d did not start listening within 5s:\n%s", port, data)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return port
}

// runGit runs a git command, failing the test with combined stdout/stderr on
// a non-zero exit. dir == "" runs in the test binary's own working directory
// (used for commands, like `git init --bare <path>` or `git push <path>
// ...`, whose target is an explicit argument rather than the cwd).
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s (dir=%q): %v\n%s", strings.Join(args, " "), dir, err, out.String())
	}
}

// copyTree recursively copies the fixture directory tree at src into dst,
// preserving structure (mode bits are normalized rather than preserved —
// nothing in the fixture needs anything other than the ordinary 0644/0755
// this writes).
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// buildFixtureRepo turns the checked-in fixture directory
// testdata/fixtures/repoprofile/<variant> into a bare git repository at
// <barePath>/<org>/<repo>, with one commit on `main`, ready to be served by
// git daemon (startGitDaemon).
func buildFixtureRepo(t *testing.T, variant, barePath, org, repo string) {
	t.Helper()

	srcDir := filepath.Join(repoProfileFixtureRoot, variant)
	if _, err := os.Stat(srcDir); err != nil {
		t.Fatalf("fixture variant %q not found at %s: %v", variant, srcDir, err)
	}

	workDir := t.TempDir()
	if err := copyTree(srcDir, workDir); err != nil {
		t.Fatalf("copy fixture %q into a work dir: %v", variant, err)
	}

	runGit(t, workDir, "init", "-q", "-b", "main")
	runGit(t, workDir, "config", "user.email", "fixture@sand.example.com")
	runGit(t, workDir, "config", "user.name", "Sand Fixture Bot")
	runGit(t, workDir, "add", "-A")
	runGit(t, workDir, "commit", "-q", "-m", "fixture: repo-profile e2e ("+variant+")")

	bareDir := filepath.Join(barePath, org, repo)
	if err := os.MkdirAll(bareDir, 0o755); err != nil {
		t.Fatalf("mkdir bare repo dir %s: %v", bareDir, err)
	}
	runGit(t, "", "init", "--bare", "-q", "-b", "main", bareDir)
	runGit(t, workDir, "push", "-q", bareDir, "main:main")
}

// requireGit skips the test when git is not on PATH — the fixture-serving
// machinery this file adds needs it independently of Lima/limactl.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
}

// TestE2ERepoProfileFullPath drives `sand create --clone-url <served
// fixture>` against the shared cmd/sand e2e base and asserts every declared
// manifest group actually applied inside the guest clone (plan Self
// Validation step 1), AND that the declared `go` toolset tool was reconciled
// per-clone WITHOUT touching the shared base's own toolset stamp (Self
// Validation step 2).
//
// Reusing cmdE2EBaseName for both checks in one VM, rather than building a
// second base with --with-go=false, is deliberate: ensureCmdE2EBase's shared
// base already has EVERY tool flag off (see its doc comment), so it already
// satisfies the reconciliation check's precondition — go absent from the
// base — for free. lima-e2e's job timeout accounts for one extra VM here,
// not two.
func TestE2ERepoProfileFullPath(t *testing.T) {
	if os.Getenv("LIMA_E2E") == "" {
		t.Skip("set LIMA_E2E=1 (and -tags limae2e) to run the real-Lima e2e tests")
	}
	requireGit(t)

	cli, prov, baseCfg := ensureCmdE2EBase(t)

	// Precondition for the reconciliation assertion below: the shared base
	// must not already carry go, or "the clone has go and the base doesn't"
	// would prove nothing.
	if set, ok := provision.BaseToolset(lima.LocalFiles(), cmdE2EBaseName); ok && set["go"] {
		t.Fatalf("shared e2e base %q unexpectedly already carries go in its toolset stamp (%v) — the toolset-reconciliation assertion below would not be meaningful", cmdE2EBaseName, set)
	}

	reposRoot := t.TempDir()
	const org, repo = "sand-e2e", "repoprofile"
	buildFixtureRepo(t, "valid", reposRoot, org, repo)
	port := startGitDaemon(t, reposRoot)
	cloneURL := fmt.Sprintf("git://host.lima.internal:%d/%s/%s", port, org, repo)

	const name = "sand-cmde2e-repoprofile"
	_ = cli.Delete(name, true)
	t.Cleanup(func() { _ = cli.Delete(name, true) })

	cfg := baseCfg
	cfg.Name, cfg.GitName, cfg.GitEmail = name, "Sand CmdE2E RepoProfile", "sand-cmde2e-repoprofile@example.com"
	cfg.CloneURL = cloneURL

	reg := registry.NewEmpty()
	var buildLog bytes.Buffer
	if err := doHeadlessCreate(context.Background(), reg, prov, cfg, registry.LocalScope, false, false, &buildLog); err != nil {
		t.Fatalf("doHeadlessCreate with --clone-url %s: %v\n%s", cloneURL, err, buildLog.String())
	}

	ctx := context.Background()

	// Resolve where the project role actually cloned the fixture, mirroring
	// roles/project/tasks/main.yml's own scheme://host/org/repo derivation
	// (provision.CheckoutRelDir is the Go-side mirror of that same regex
	// logic) plus the guest's real $HOME for the create's user.
	checkoutRel, ok := provision.CheckoutRelDir(cloneURL)
	if !ok {
		t.Fatalf("CheckoutRelDir(%q) = _, false — cannot locate the clone destination", cloneURL)
	}
	homeOut, err := cli.ShellOut(ctx, name, "sh", "-c", "echo $HOME")
	if err != nil {
		t.Fatalf("resolve guest $HOME: %v", err)
	}
	checkoutDir := strings.TrimSpace(string(homeOut)) + "/" + checkoutRel

	// --- packages: the fixture's declared apt package is installed. ---
	if _, err := cli.ShellOut(ctx, name, "sh", "-c", "dpkg -l cowsay | grep -E '^ii'"); err != nil {
		t.Fatalf("declared package cowsay is not installed in the clone: %v", err)
	}

	// --- services: the fixture's declared systemd service is enabled. ---
	out, err := cli.ShellOut(ctx, name, "systemctl", "is-enabled", "sand-fixture-marker.service")
	if err != nil || strings.TrimSpace(string(out)) != "enabled" {
		t.Fatalf("declared service sand-fixture-marker.service is not enabled: err=%v out=%q", err, strings.TrimSpace(string(out)))
	}

	// --- roles: the fixture's custom role's effect is present. ---
	if _, err := cli.ShellOut(ctx, name, "test", "-f", "/etc/sand-fixture-role-marker"); err != nil {
		t.Fatalf("custom role marker /etc/sand-fixture-role-marker is missing: %v", err)
	}

	// --- toolset: the declared shipped tool is present in the clone (this is
	// ALSO half of the toolset-reconciliation check: the other half, that the
	// shared base's own stamp did NOT gain go, is asserted below). ---
	if _, err := cli.ShellOut(ctx, name, "go", "version"); err != nil {
		t.Fatalf("declared toolset tool go is not present in the clone despite the manifest's toolset declaration: %v", err)
	}

	// --- seed: the seed tasks file's marker exists in the project tree. ---
	if _, err := cli.ShellOut(ctx, name, "test", "-f", checkoutDir+"/seed-marker.txt"); err != nil {
		t.Fatalf("seed marker missing at %s/seed-marker.txt: %v", checkoutDir, err)
	}

	// --- toolset reconciliation, second half: the SHARED base's own toolset
	// stamp must still exclude go — proving the per-clone install above did
	// not churn the base every other clone/test shares. ---
	set, ok := provision.BaseToolset(lima.LocalFiles(), cmdE2EBaseName)
	if !ok {
		t.Fatalf("shared base %q has no recorded toolset stamp after the clone", cmdE2EBaseName)
	}
	if set["go"] {
		t.Fatalf("shared base %q's toolset stamp gained go after a per-clone reconciliation — the base was churned instead of the clone: %v", cmdE2EBaseName, set)
	}
}

// TestE2ERepoProfileMalformedManifestAborts is the plan's Self Validation
// step 6: a served fixture whose manifest has an unknown top-level key must
// abort finalize with the validator's own message (roles/repo-profile's
// "Repo provisioning profile is invalid" fail task, quoting
// scripts/validate_profile.py's stderr) — never a silent skip, and never a
// generic Ansible failure with no indication of WHICH key is wrong. This
// proves the guest-side invocation wiring (Task 3), not just the validator's
// own logic (already unit-tested in Task 1).
func TestE2ERepoProfileMalformedManifestAborts(t *testing.T) {
	if os.Getenv("LIMA_E2E") == "" {
		t.Skip("set LIMA_E2E=1 (and -tags limae2e) to run the real-Lima e2e tests")
	}
	requireGit(t)

	cli, prov, baseCfg := ensureCmdE2EBase(t)

	reposRoot := t.TempDir()
	const org, repo = "sand-e2e", "repoprofile-malformed"
	buildFixtureRepo(t, "malformed", reposRoot, org, repo)
	port := startGitDaemon(t, reposRoot)
	cloneURL := fmt.Sprintf("git://host.lima.internal:%d/%s/%s", port, org, repo)

	const name = "sand-cmde2e-repoprofile-bad"
	_ = cli.Delete(name, true)
	t.Cleanup(func() { _ = cli.Delete(name, true) })

	cfg := baseCfg
	cfg.Name, cfg.GitName, cfg.GitEmail = name, "Sand CmdE2E RepoProfile Bad", "sand-cmde2e-repoprofile-bad@example.com"
	cfg.CloneURL = cloneURL

	reg := registry.NewEmpty()
	var log bytes.Buffer
	err := doHeadlessCreate(context.Background(), reg, prov, cfg, registry.LocalScope, false, false, &log)
	if err == nil {
		t.Fatalf("doHeadlessCreate against a malformed .sandbar/profile.yml should have failed, but it succeeded")
	}

	combined := err.Error() + "\n" + log.String()
	if !strings.Contains(combined, "Repo provisioning profile is invalid") {
		t.Fatalf("expected the repo-profile stage's own abort message (\"Repo provisioning profile is invalid\"), got:\n%s", combined)
	}
	if !strings.Contains(combined, "unknown top-level key") {
		t.Fatalf("expected the validator's specific error naming the unknown key, got:\n%s", combined)
	}
}

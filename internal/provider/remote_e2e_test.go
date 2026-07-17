//go:build limae2e

// remote_e2e_test.go is the remote-Lima-over-SSH counterpart to
// cmd/sand/create_e2e_test.go and internal/lima/copy_e2e_test.go: it drives the
// REAL remote provider (remote.go) against a REAL SSH target end to end —
// create, the recorded-managed/remote-scope claim, the attach argv, a
// copy-and-read-back, list, stop, delete — and confirms a
// LOCAL `limactl list` and the REMOTE provider's List() never show each other's
// instances. It is gated behind the `limae2e` build tag exactly like every other
// test in that family (AGENTS.md's hard rule: no test may require a real
// limactl/ssh target without the tag; plain `go test ./...` never runs this
// file).
//
// On top of the tag, this test has an opt-in gate of its own: LIMA_REMOTE_E2E=1
// AND a reachable SSH target, because — unlike the other limae2e tests, which
// only need a local KVM — this one needs a SECOND, separately reachable Lima
// host, and passwordless SSH to it is not something a checkout can assume is
// configured. With no target configured it SKIPS CLEANLY, the same way the
// limae2e-tagged tests skip without KVM when LIMA_E2E is unset. The target is
// configured through this test's OWN env vars (LIMA_REMOTE_E2E_HOST/USER/
// PORT/IDENTITY/LIMA_HOME) plus LIMA_REMOTE_E2E=1 — this test builds a
// provider.TargetConfig directly. `sand` itself no longer has an env-var
// selection surface at all — it was retired in favor of internal/profiles's
// persisted connection profiles — so this test's env vars
// are private to this suite and were never something pointing `sand` itself
// anywhere.
//
// Run (the target host needs limactl + KVM; a loopback simulation — pointing
// LIMA_REMOTE_E2E_HOST at "localhost" — only exercises the local/remote
// isolation assertion meaningfully when LIMA_REMOTE_E2E_USER names a
// DIFFERENT local account than the one running the test, since that is what
// gives the "remote" side its own $HOME and therefore its own default
// ~/.lima, genuinely separate from the caller's):
//
//	LIMA_REMOTE_E2E=1 LIMA_REMOTE_E2E_HOST=localhost LIMA_REMOTE_E2E_USER=sand-remote-e2e \
//	  go test -tags limae2e -timeout 30m -run TestE2ERemoteLima ./internal/provider/
package provider_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// This test's own, private env-var surface for naming a live SSH target —
// `sand` itself has no env-var selection surface at all any more (it was
// retired in favor of internal/profiles's persisted connection profiles),
// so this suite's configuration cannot be confused with the
// product's.
const (
	remoteE2EHostEnv     = "LIMA_REMOTE_E2E_HOST"
	remoteE2EUserEnv     = "LIMA_REMOTE_E2E_USER"
	remoteE2EPortEnv     = "LIMA_REMOTE_E2E_PORT"
	remoteE2EIdentityEnv = "LIMA_REMOTE_E2E_IDENTITY"
	remoteE2ELimaHomeEnv = "LIMA_REMOTE_E2E_LIMA_HOME"
)

// remoteE2ETargetConfig builds the provider.TargetConfig this test drives
// NewRemoteLima with, from this suite's own env vars (see above) — the same
// secret-free shape (internal/provider/select.go) a RemoteSSH connection
// profile is converted into for real use, so this test exercises the real
// provider construction path even though it is not going through
// internal/profiles or BuildFleet itself.
func remoteE2ETargetConfig(t *testing.T) provider.TargetConfig {
	t.Helper()
	cfg := provider.TargetConfig{
		Provider:       provider.RemoteLimaProviderID,
		Host:           os.Getenv(remoteE2EHostEnv),
		User:           os.Getenv(remoteE2EUserEnv),
		IdentityPath:   os.Getenv(remoteE2EIdentityEnv),
		RemoteLimaHome: os.Getenv(remoteE2ELimaHomeEnv),
	}
	if portStr := os.Getenv(remoteE2EPortEnv); portStr != "" {
		port, err := strconv.Atoi(portStr)
		if err != nil {
			t.Fatalf("%s=%q is not a valid port: %v", remoteE2EPortEnv, portStr, err)
		}
		cfg.Port = port
	}
	return cfg
}

// sshTarget mirrors SSHHost.target(): user@host, or bare host with no user set.
func sshTarget(cfg provider.TargetConfig) string {
	if cfg.User != "" {
		return cfg.User + "@" + cfg.Host
	}
	return cfg.Host
}

// skipUnlessRemoteE2EConfigured is the clean-skip path this test MUST take on a
// box with no passwordless SSH set up: it checks the opt-in gate and a
// configured host first (cheapest, least surprising reasons to skip), then a
// bounded, non-interactive reachability probe — a TCP dial, then
// `ssh -o BatchMode=yes limactl --version` — so a target that is configured but
// not actually reachable (or whose auth needs a password/host-key prompt this
// test has no tty to answer) is reported as a clean skip rather than a
// multi-minute hang or a wall of ssh auth noise.
func skipUnlessRemoteE2EConfigured(t *testing.T) provider.TargetConfig {
	t.Helper()
	if os.Getenv("LIMA_REMOTE_E2E") == "" {
		t.Skip("set LIMA_REMOTE_E2E=1 (plus LIMA_REMOTE_E2E_HOST, and -tags limae2e) to run the remote-Lima e2e test")
	}
	cfg := remoteE2ETargetConfig(t)
	if cfg.Host == "" {
		t.Skipf("set %s (and LIMA_REMOTE_E2E=1) to point the remote e2e test at an SSH target", remoteE2EHostEnv)
	}

	port := cfg.Port
	if port <= 0 {
		port = 22
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(port)), 3*time.Second)
	if err != nil {
		t.Skipf("remote target %s:%d not reachable: %v (skipping cleanly — configure SSH to run this live)", cfg.Host, port, err)
	}
	_ = conn.Close()

	sshArgs := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "StrictHostKeyChecking=accept-new"}
	if cfg.Port > 0 && cfg.Port != 22 {
		sshArgs = append(sshArgs, "-p", strconv.Itoa(cfg.Port))
	}
	if cfg.IdentityPath != "" {
		sshArgs = append(sshArgs, "-i", cfg.IdentityPath)
	}
	sshArgs = append(sshArgs, sshTarget(cfg), "limactl", "--version")
	if out, err := exec.Command("ssh", sshArgs...).CombinedOutput(); err != nil {
		t.Skipf("no passwordless SSH to the configured remote target (%s): %v\n%s", sshTarget(cfg), err, out)
	}

	return cfg
}

// TestE2ERemoteLima is one cohesive integration test: create over
// SSH, the managed/remote-scope bookkeeping, the exec-ready ssh-wrapped attach
// argv (with a best-effort live corroboration that the guest `main` tmux
// session survives detach, when a host tmux binary is available to drive it),
// a copy up and read-back, list/stop/delete, and the local/remote list
// isolation the whole point of a Scope is to guarantee.
func TestE2ERemoteLima(t *testing.T) {
	cfg := skipUnlessRemoteE2EConfigured(t)

	// NewRemoteLima's host-access handle lives on the Provisioner it constructs
	// (Provisioner.HostFiles), not a process-global, so there is nothing here to
	// leak into another test sharing the process and nothing to restore.
	remote, err := provider.NewRemoteLima(cfg)
	if err != nil {
		t.Fatalf("NewRemoteLima: %v", err)
	}

	const name = "sand-remote-e2e"
	const baseName = "sand-remote-e2e-base"

	// Clean slate + unconditional teardown, exactly like every other e2e test in
	// this repo — a prior interrupted run must not confuse this one.
	_ = remote.Delete(name, true)
	_ = remote.Delete(baseName, true)
	t.Cleanup(func() { _ = remote.Delete(name, true) })
	t.Cleanup(func() { _ = remote.Delete(baseName, true) })

	vmCfg := vm.CreateConfig{
		Name:     name,
		BaseName: baseName,
		User:     vm.HostUser(),
		GitName:  "Sand Remote E2E",
		GitEmail: "sand-remote-e2e@example.com",
		CPUs:     2,
		Memory:   "2GiB",
		Disk:     vm.BaseDiskFloor,
		Domain:   "lan",
		Locale:   "en_US.UTF-8",
		// Every optional tool flag left at its zero value (false): this test
		// exercises the SSH transport/topology, never the base's installed
		// tooling — see cmd/sand/create_e2e_test.go's ensureCmdE2EBase for the
		// same reasoning.
	}

	ctx := context.Background()
	var createLog bytes.Buffer
	if err := remote.Create(ctx, vmCfg, provision.CreateOptions{}, &createLog); err != nil {
		t.Fatalf("remote Create: %v\n%s", err, createLog.String())
	}

	// --- recorded managed, with the REMOTE scope ---------------------------
	scope := cfg.Scope()
	if scope.Provider != provider.RemoteLimaProviderID || scope.RemoteTarget == "" {
		t.Fatalf("TargetConfig.Scope() = %+v, want a populated remote scope", scope)
	}
	reg := registry.NewEmpty()
	if err := reg.AddScoped(vmCfg, scope); err != nil {
		t.Fatalf("AddScoped: %v", err)
	}
	// IsManagedInScope, not the bare IsManaged: since the registry was re-keyed
	// by (scope, name), the unscoped conveniences are LOCAL-scope shorthands, and
	// a remote-scoped entry is deliberately invisible to them — the same
	// isolation the LocalScope assertion below pins from the other side.
	if !reg.IsManagedInScope(name, scope) {
		t.Fatalf("%s not recorded managed under its remote scope after AddScoped", name)
	}
	if base, managed := reg.BaseInScope(name, scope); !managed || base != baseName {
		t.Fatalf("BaseInScope(%s, remote scope) = (%q, %v), want (%q, true)", name, base, managed, baseName)
	}
	if _, managed := reg.BaseInScope(name, registry.LocalScope); managed {
		t.Fatalf("a remote-scoped entry must NOT be found under the LOCAL scope — Scope's whole purpose is to keep the two from crossing")
	}

	// --- remote List() sees it -----------------------------------------------
	remoteVMs, err := remote.List()
	if err != nil {
		t.Fatalf("remote List: %v", err)
	}
	if !containsVM(remoteVMs, name) {
		t.Fatalf("%s missing from the remote provider's List() after Create: %+v", name, remoteVMs)
	}

	// --- local and remote lists never show each other's instances -----------
	// Asserted BEFORE delete, while the remote-created VM still exists: a real
	// local `limactl list` (this box's own default Lima home, wholly untouched
	// by the remote provider's ssh hop) must not see it, and the remote list
	// must carry no name also present in the local list.
	localCli := lima.New(lima.NewExecRunner())
	localVMs, err := localCli.List()
	if err != nil {
		t.Fatalf("local limactl list: %v", err)
	}
	if containsVM(localVMs, name) {
		t.Fatalf("%s (created via the REMOTE provider) leaked into the LOCAL limactl list: %+v", name, localVMs)
	}
	for _, lv := range localVMs {
		if containsVM(remoteVMs, lv.Name) {
			t.Fatalf("local instance %q also appears in the REMOTE provider's List() — local and remote Lima homes are not isolated", lv.Name)
		}
	}

	// --- attach argv: ssh -t ... limactl shell ... bash -c <guestAttachExpr>,
	// and exec-ready -------------------------------------------------------
	v, err := remote.Get(name)
	if err != nil {
		t.Fatalf("remote Get: %v", err)
	}
	argv := remote.AttachArgv(v)
	if len(argv) < 3 || argv[0] != "ssh" || argv[1] != "-t" {
		t.Fatalf("remote AttachArgv = %v, want it to start `ssh -t ...`", argv)
	}
	if !strings.Contains(argv[len(argv)-1], "tmux") {
		t.Fatalf("remote AttachArgv's last element should be the guest tmux expression, got %q", argv[len(argv)-1])
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		t.Fatalf("attach argv's own binary (%q) must be resolvable for tea.ExecProcess to exec it: %v", argv[0], err)
	}

	// Best-effort live corroboration: drive the real attach through a private
	// host tmux PTY (mirrors internal/ui/shell_e2e_test.go's e2eHostTmux) and
	// confirm the guest `main` session survives detach. This is optional
	// corroboration on top of the argv assertions above (the task's own
	// wording: "if feasible against the live target") — skipped, not failed,
	// when no host tmux binary is available.
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Logf("no host tmux binary available — skipping the live attach/detach corroboration: %v", err)
	} else {
		assertGuestMainSurvivesDetach(t, remote, name, argv)
	}

	// --- copy a sentinel up, read it back ------------------------------------
	const guestDir = "/tmp/sand-remote-e2e"
	const sentinel = "sand-remote-e2e sentinel payload"
	if err := remote.Shell(ctx, name, nil, io.Discard, "mkdir", "-p", guestDir); err != nil {
		t.Fatalf("mkdir guest dir: %v", err)
	}
	hostSrc := writeSentinelFile(t, sentinel)
	if err := remote.Copy(ctx, io.Discard, false, hostSrc, remote.GuestPath(name, guestDir)); err != nil {
		t.Fatalf("copy sentinel to guest: %v", err)
	}
	back, err := remote.ShellOut(ctx, name, "cat", guestDir+"/sentinel.txt")
	if err != nil {
		t.Fatalf("read sentinel back from guest: %v", err)
	}
	if got := strings.TrimSpace(string(back)); got != sentinel {
		t.Fatalf("sentinel round-trip = %q, want %q — the copy did not place the file where Copy's directory contract says it should", got, sentinel)
	}

	// --- list / stop / delete -------------------------------------------------
	if err := remote.Stop(name); err != nil {
		t.Fatalf("remote Stop: %v", err)
	}
	if status, err := remote.Status(name); err != nil || status != "Stopped" {
		t.Fatalf("remote Status after Stop = (%q, %v), want (Stopped, nil)", status, err)
	}
	if err := remote.Delete(name, true); err != nil {
		t.Fatalf("remote Delete: %v", err)
	}
	remoteVMs, err = remote.List()
	if err != nil {
		t.Fatalf("remote List after Delete: %v", err)
	}
	if containsVM(remoteVMs, name) {
		t.Fatalf("%s still present in the remote provider's List() after Delete: %+v", name, remoteVMs)
	}
}

func containsVM(vms []vm.VM, name string) bool {
	for _, v := range vms {
		if v.Name == name {
			return true
		}
	}
	return false
}

func writeSentinelFile(t *testing.T, content string) string {
	t.Helper()
	path := t.TempDir() + "/sentinel.txt"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write sentinel file: %v", err)
	}
	return path
}

// remoteShellOut runs argv against name over the remote provider and returns
// its trimmed stdout, failing the test on error — the remote counterpart to
// internal/provision's own guestOut helper (which is pinned to *lima.Client
// and so cannot drive a provider.Provider).
func remoteShellOut(t *testing.T, remote provider.Provider, name string, argv ...string) string {
	t.Helper()
	out, err := remote.ShellOut(context.Background(), name, argv...)
	if err != nil {
		t.Fatalf("shell %s %v: %v\n%s", name, argv, err, string(out))
	}
	return strings.TrimSpace(string(out))
}

// TestE2ERemoteLimaTemplateRoundTrip extends this suite's SSH-target coverage
// to plan 17's golden-VM-template feature: snapshot a REMOTE VM into a
// template, create a second REMOTE VM straight from it, confirm the guest
// marker and a fresh per-VM identity carried over, delete the template, and —
// the whole point of running this against the remote provider rather than
// just internal/provision's local TestTemplateRoundTrip — confirm the
// template's disk existed ONLY on the remote host, never locally, mirroring
// the local/remote list isolation TestE2ERemoteLima already checks for
// ordinary VMs (registry.Scope's whole reason to exist).
//
// Kept to a single round-trip for this scope (creates are expensive; the
// ordinary create/list/attach/copy lifecycle is already covered by
// TestE2ERemoteLima above) — this test's own scenario is the template
// mechanics alone.
func TestE2ERemoteLimaTemplateRoundTrip(t *testing.T) {
	cfg := skipUnlessRemoteE2EConfigured(t)

	remote, err := provider.NewRemoteLima(cfg)
	if err != nil {
		t.Fatalf("NewRemoteLima: %v", err)
	}

	const (
		baseName   = "sand-remote-e2e-tmpl-base"
		sourceName = "sand-remote-e2e-tmpl-source"
		cloneName  = "sand-remote-e2e-tmpl-clone"
	)
	templateInstance := vm.TemplateInstanceName("golden-e2e-remote")

	cleanup := func() {
		_ = remote.Delete(cloneName, true)
		_ = remote.Delete(templateInstance, true)
		_ = remote.Delete(sourceName, true)
		_ = remote.Delete(baseName, true)
	}
	cleanup()
	t.Cleanup(cleanup)

	sourceCfg := vm.CreateConfig{
		Name:     sourceName,
		BaseName: baseName,
		User:     vm.HostUser(),
		GitName:  "Sand Remote E2E Tmpl Source",
		GitEmail: "sand-remote-e2e-tmpl-source@example.com",
		CPUs:     2,
		Memory:   "2GiB",
		Disk:     vm.BaseDiskFloor,
		Domain:   "lan",
		Locale:   "en_US.UTF-8",
		// Every optional tool-set flag left at its zero value: this test
		// exercises the template mechanics over SSH, never the base's
		// installed tooling.
	}

	ctx := context.Background()
	var createLog bytes.Buffer
	if err := remote.Create(ctx, sourceCfg, provision.CreateOptions{}, &createLog); err != nil {
		t.Fatalf("remote Create source: %v\n%s", err, createLog.String())
	}

	// Seed a guest marker — a file and a directory — exactly like the local
	// round-trip (internal/provision/template_e2e_test.go).
	if err := remote.Shell(ctx, sourceName, nil, io.Discard, "sh", "-c",
		"set -e; echo golden > ~/marker.txt; mkdir -p ~/markerdir; echo golden-dir > ~/markerdir/marker2.txt"); err != nil {
		t.Fatalf("seed marker: %v", err)
	}
	sourceHostname := remoteShellOut(t, remote, sourceName, "hostname")
	sourceGitName := remoteShellOut(t, remote, sourceName, "git", "config", "--get", "user.name")
	if sourceHostname != sourceName {
		t.Fatalf("source hostname = %q, want %q (EffectiveHostname defaults to the VM name)", sourceHostname, sourceName)
	}

	// --- snapshot: source ends back in the power state it started in
	// (Running), and the template instance is a stopped clone --------------
	if status, err := remote.Status(sourceName); err != nil || status != "Running" {
		t.Fatalf("source status before snapshot = (%q, %v), want (Running, nil)", status, err)
	}

	var snapLog bytes.Buffer
	if _, err := remote.SnapshotTemplate(ctx, sourceName, templateInstance, &snapLog); err != nil {
		t.Fatalf("SnapshotTemplate: %v\n%s", err, snapLog.String())
	}

	if status, err := remote.Status(sourceName); err != nil || status != "Running" {
		t.Fatalf("source status after snapshot = (%q, %v), want (Running, nil) — snapshot must restore the prior power state", status, err)
	}
	if status, err := remote.Status(templateInstance); err != nil || status != "Stopped" {
		t.Fatalf("template instance status = (%q, %v), want (Stopped, nil)", status, err)
	}

	// --- the template disk lived ONLY on the remote host -------------------
	remoteDir := filepath.Join(remote.HostFiles().LimaHome(), templateInstance)
	if _, err := remote.HostFiles().Stat(remoteDir); err != nil {
		t.Fatalf("template instance dir %q missing on the REMOTE host after snapshot: %v", remoteDir, err)
	}
	localDir := filepath.Join(lima.LocalFiles().LimaHome(), templateInstance)
	if _, err := lima.LocalFiles().Stat(localDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("template instance dir %q found on the LOCAL host (err=%v) — a remote template must never leak onto the local host", localDir, err)
	}

	// --- create VM2 straight from the template ------------------------------
	cloneCfg := sourceCfg
	cloneCfg.Name, cloneCfg.BaseName = cloneName, templateInstance
	cloneCfg.GitName, cloneCfg.GitEmail = "Sand Remote E2E Tmpl Clone", "sand-remote-e2e-tmpl-clone@example.com"

	var cloneLog bytes.Buffer
	if err := remote.Create(ctx, cloneCfg, provision.CreateOptions{TemplateSource: templateInstance}, &cloneLog); err != nil {
		t.Fatalf("remote Create from template: %v\n%s", err, cloneLog.String())
	}

	if got := remoteShellOut(t, remote, cloneName, "sh", "-c", "cat ~/marker.txt"); got != "golden" {
		t.Fatalf("clone marker.txt = %q, want %q", got, "golden")
	}
	if got := remoteShellOut(t, remote, cloneName, "sh", "-c", "cat ~/markerdir/marker2.txt"); got != "golden-dir" {
		t.Fatalf("clone markerdir/marker2.txt = %q, want %q", got, "golden-dir")
	}

	cloneHostname := remoteShellOut(t, remote, cloneName, "hostname")
	if cloneHostname == sourceHostname {
		t.Fatalf("clone hostname %q must differ from source hostname %q", cloneHostname, sourceHostname)
	}
	if cloneHostname != cloneName {
		t.Fatalf("clone hostname = %q, want %q (its own EffectiveHostname)", cloneHostname, cloneName)
	}
	cloneGitName := remoteShellOut(t, remote, cloneName, "git", "config", "--get", "user.name")
	if cloneGitName == sourceGitName {
		t.Fatalf("clone git user.name %q must differ from source's %q", cloneGitName, sourceGitName)
	}
	if cloneGitName != cloneCfg.GitName {
		t.Fatalf("clone git user.name = %q, want %q", cloneGitName, cloneCfg.GitName)
	}

	// --- delete the template, confirm the instance (and its disk) is gone,
	// on the remote host -----------------------------------------------------
	var delLog bytes.Buffer
	if err := remote.DeleteTemplate(ctx, templateInstance, &delLog); err != nil {
		t.Fatalf("DeleteTemplate: %v\n%s", err, delLog.String())
	}
	if _, err := remote.Get(templateInstance); !errors.Is(err, lima.ErrNoSuchInstance) {
		t.Fatalf("remote Get(%q) after DeleteTemplate = %v, want lima.ErrNoSuchInstance", templateInstance, err)
	}
	if _, err := remote.HostFiles().Stat(remoteDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("template instance dir %q still present on the remote host after DeleteTemplate (err=%v)", remoteDir, err)
	}
}

// assertGuestMainSurvivesDetach drives the exact remote attach argv through a
// private, throwaway host tmux server — a distinct -S socket inside t.TempDir(),
// never the developer's default socket and never torn down with kill-server
// (only ever by session name) — as the PTY source `ssh -t` and the nested guest
// tmux client both require. It waits for the guest's tmux status bar to appear,
// kills the client session (the same effect as closing that terminal), and then
// asks the guest directly (over a PLAIN, non-interactive remote.ShellOut, not a
// second nested attach) whether `main` is still there — the headline claim this
// whole feature exists for (see internal/lima/attach.go's guestAttachExpr).
func assertGuestMainSurvivesDetach(t *testing.T, remote provider.Provider, vmName string, argv []string) {
	t.Helper()
	sock := t.TempDir() + "/sand-remote-e2e.sock"
	run := func(args ...string) []byte {
		t.Helper()
		full := append([]string{"-S", sock}, args...)
		out, err := exec.Command("tmux", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v\n%s", args, err, out)
		}
		return out
	}

	const session = "sand-remote-e2e-client"
	newArgs := append([]string{"new-session", "-d", "-s", session, "-x", "200", "-y", "50"}, argv...)
	run(newArgs...)

	deadline := time.Now().Add(45 * time.Second)
	for {
		pane, err := exec.Command("tmux", "-S", sock, "capture-pane", "-p", "-t", session).CombinedOutput()
		if err == nil && (strings.Contains(string(pane), "[main") || strings.Contains(string(pane), "[sand-")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the guest tmux status bar over the remote attach; last pane:\n%s", pane)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Detach: kill the host-side client session, the same effect as closing the
	// terminal — never a kill-server, and this is a private throwaway socket
	// that touches nothing outside this test.
	_ = exec.Command("tmux", "-S", sock, "kill-session", "-t", session).Run()
	time.Sleep(2 * time.Second)

	out, err := remote.ShellOut(context.Background(), vmName, "tmux", "list-sessions")
	if err != nil {
		t.Fatalf("check guest tmux sessions after detach: %v", err)
	}
	if !strings.Contains(string(out), "main") {
		t.Fatalf("guest tmux session %q should still exist after detach, got tmux list-sessions: %q", "main", out)
	}
}

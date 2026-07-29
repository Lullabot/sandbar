//go:build proxmoxe2e

// proxmox_e2e_test.go is the Proxmox counterpart to remote_e2e_test.go: it
// drives the REAL Proxmox provider (proxmox.go) against a REAL Proxmox VE host
// end to end — preflight, create, list, a plain non-interactive ShellOut,
// host-resource sampling, reset, delete — and then proves the plan's central
// security claim from the other side: a pool-scoped token CANNOT touch a VM
// outside its pool.
//
// It is gated behind the `proxmoxe2e` build tag exactly like the limae2e family
// (AGENTS.md's hard rule: no test may require a real external target without a
// tag; plain `go test ./...` never compiles this file). On top of the tag it has
// an opt-in gate of its own — PROXMOX_E2E=1 plus a configured, reachable host —
// because CI has no Proxmox host and a checkout cannot assume one. With nothing
// configured it SKIPS CLEANLY.
//
// It builds a provider.TargetConfig directly from this suite's OWN env vars.
// `sand` itself has no env-var selection surface — connection profiles replaced
// it — so these vars are private to this suite and can never be confused with
// the product's configuration.
//
// Required env (all must be set, or the suite skips):
//
//	PROXMOX_E2E=1
//	PROXMOX_E2E_HOST         host[:port] the API answers on (e.g. pve1.example.com or pve1:8006)
//	PROXMOX_E2E_NODE         the PVE node name (e.g. pve1)
//	PROXMOX_E2E_POOL         the dedicated pool sandbar's VMs live in (e.g. sandbar-test)
//	PROXMOX_E2E_STORAGE      storage backing VM disks + cloud-init (must support images), e.g. local-lvm
//	PROXMOX_E2E_BRIDGE       the Linux bridge net0 attaches to (e.g. vmbr0)
//	PROXMOX_E2E_TOKEN_FILE   path to a 0600 file holding user@realm!tokenid=uuid
//	PROXMOX_E2E_SSH_USER     the cloud-init guest login user (the ciuser sand provisions)
//	PROXMOX_E2E_SSH_IDENTITY path to the private key that reaches the guest
//
// A leading ~ in the two path vars is expanded here, exactly as the product
// expands identity_path and token_file from a profile — the suite reads them
// straight out of the environment, where no shell has done it for us.
//
// Optional:
//
//	PROXMOX_E2E_IMAGE            cloud-image URL the base template is built from
//	                             (qcow2/raw/…; NOT .img), fed to TargetConfig.BaseImage.
//	                             LEAVE IT UNSET to use sand's own default golden
//	                             image: sand needs qemu-guest-agent running on
//	                             first boot to learn a VM's IP, and a stock cloud
//	                             image does not ship it, so most overrides here
//	                             will hang the lifecycle test rather than teach
//	                             you anything.
//	PROXMOX_E2E_INSECURE=1       skip TLS verification (self-signed PVE cert)
//	PROXMOX_E2E_FOREIGN_VMID     a VMID OUTSIDE the pool, for the isolation test.
//	                             The isolation test skips if unset, and NEVER
//	                             creates or deletes this VM — the operator owns it.
//
// Run:
//
//	PROXMOX_E2E=1 PROXMOX_E2E_HOST=pve1.example.com PROXMOX_E2E_NODE=pve1 \
//	  PROXMOX_E2E_POOL=sandbar-test PROXMOX_E2E_STORAGE=local-lvm PROXMOX_E2E_BRIDGE=vmbr0 \
//	  PROXMOX_E2E_TOKEN_FILE=~/.config/sandbar/pve-test.token \
//	  PROXMOX_E2E_SSH_USER=debian PROXMOX_E2E_SSH_IDENTITY=~/.ssh/id_ed25519 \
//	  PROXMOX_E2E_FOREIGN_VMID=100 \
//	  go test -tags proxmoxe2e -timeout 45m -run TestE2EProxmox -v ./internal/provider/
package provider_test

import (
	"bytes"
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/pve"
	"github.com/lullabot/sandbar/internal/vm"
)

const (
	proxmoxE2EEnabled     = "PROXMOX_E2E"
	proxmoxE2EHost        = "PROXMOX_E2E_HOST"
	proxmoxE2ENode        = "PROXMOX_E2E_NODE"
	proxmoxE2EPool        = "PROXMOX_E2E_POOL"
	proxmoxE2EStorage     = "PROXMOX_E2E_STORAGE"
	proxmoxE2EImageStore  = "PROXMOX_E2E_IMAGE_STORAGE" // optional; defaults to "local"
	proxmoxE2EBridge      = "PROXMOX_E2E_BRIDGE"
	proxmoxE2ETokenFile   = "PROXMOX_E2E_TOKEN_FILE"
	proxmoxE2ESSHUser     = "PROXMOX_E2E_SSH_USER"
	proxmoxE2ESSHIdentity = "PROXMOX_E2E_SSH_IDENTITY"
	proxmoxE2EImage       = "PROXMOX_E2E_IMAGE"
	proxmoxE2EInsecure    = "PROXMOX_E2E_INSECURE"
	proxmoxE2EForeignVMID = "PROXMOX_E2E_FOREIGN_VMID"
)

// proxmoxE2ETargetConfig builds the TargetConfig this suite drives NewProxmox
// with — the same secret-free shape (select.go) a proxmox connection profile is
// converted into for real use, so the real construction path (including the
// token-file load) is what is under test.
func proxmoxE2ETargetConfig(t *testing.T) provider.TargetConfig {
	t.Helper()
	host, port := os.Getenv(proxmoxE2EHost), 0
	if h, p, ok := strings.Cut(host, ":"); ok {
		host = h
		port, _ = strconv.Atoi(p)
	}
	return provider.TargetConfig{
		Provider:     provider.ProxmoxProviderID,
		Host:         host,
		Port:         port,
		Node:         os.Getenv(proxmoxE2ENode),
		Pool:         os.Getenv(proxmoxE2EPool),
		Storage:      os.Getenv(proxmoxE2EStorage),
		ImageStorage: os.Getenv(proxmoxE2EImageStore), // "" -> NewProxmox defaults to "local"
		Bridge:       os.Getenv(proxmoxE2EBridge),
		TokenFile:    proxmoxE2EPath(t, proxmoxE2ETokenFile),
		User:         os.Getenv(proxmoxE2ESSHUser),
		IdentityPath: proxmoxE2EPath(t, proxmoxE2ESSHIdentity),
		BaseImage:    os.Getenv(proxmoxE2EImage), // "" -> NewProxmox uses the default golden image
		Insecure:     os.Getenv(proxmoxE2EInsecure) != "",
	}
}

// proxmoxE2EPath reads a path-valued env var and expands a leading ~, which no
// shell has done for us: these vars are usually set from a saved env file or a
// CI secret, and every consumer here (os.ReadFile on the token, ssh -i on the
// identity) hands the value to a syscall rather than to a shell. NewProxmox
// expands both again on the way in — harmless, since expansion is idempotent —
// but doing it at the boundary is what lets the RAW client below share one
// already-resolved path with the provider instead of re-deriving it.
func proxmoxE2EPath(t *testing.T, env string) string {
	t.Helper()
	expanded, err := profiles.ExpandHome(os.Getenv(env))
	if err != nil {
		t.Fatalf("%s: %v", env, err)
	}
	return expanded
}

// skipUnlessProxmoxE2EConfigured takes the clean-skip path on a box with no
// Proxmox host: it checks the opt-in gate and every required var first (cheapest,
// least surprising reasons to skip), then a bounded TCP reachability probe of the
// API port, so a target that is configured but not reachable is a clean skip
// rather than a multi-minute hang.
func skipUnlessProxmoxE2EConfigured(t *testing.T) provider.TargetConfig {
	t.Helper()
	if os.Getenv(proxmoxE2EEnabled) == "" {
		t.Skipf("set %s=1 (plus the PROXMOX_E2E_* vars, and -tags proxmoxe2e) to run the Proxmox e2e test", proxmoxE2EEnabled)
	}
	for _, k := range []string{
		proxmoxE2EHost, proxmoxE2ENode, proxmoxE2EPool, proxmoxE2EStorage,
		proxmoxE2EBridge, proxmoxE2ETokenFile, proxmoxE2ESSHUser,
		proxmoxE2ESSHIdentity,
	} {
		if os.Getenv(k) == "" {
			t.Skipf("set %s (and %s=1) to run the Proxmox e2e test", k, proxmoxE2EEnabled)
		}
	}
	cfg := proxmoxE2ETargetConfig(t)
	port := cfg.Port
	if port <= 0 {
		port = 8006
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(port)), 3*time.Second)
	if err != nil {
		t.Skipf("Proxmox API %s:%d not reachable: %v (skipping cleanly — configure a host to run this live)", cfg.Host, port, err)
	}
	_ = conn.Close()
	return cfg
}

// proxmoxContainsVM reports whether vms holds one named name. It is defined here
// rather than reused from remote_e2e_test.go's containsVM because that file
// carries the `limae2e` tag, not this one — and a distinct name keeps both
// compilable even in the unusual case of `-tags "limae2e proxmoxe2e"`.
func proxmoxContainsVM(vms []vm.VM, name string) bool {
	for _, v := range vms {
		if v.Name == name {
			return true
		}
	}
	return false
}

// proxmoxE2EClient builds a raw pve.Client scoped exactly as the provider under
// test is: same token file, same node, same TLS posture. Tests that need to act
// on the hypervisor from OUTSIDE the provider — to prove the token is refused,
// or to cut a VM's power out from under a live session — drive this directly.
func proxmoxE2EClient(t *testing.T, cfg provider.TargetConfig) *pve.Client {
	t.Helper()
	// cfg.TokenFile, not the raw env var: proxmoxE2ETargetConfig has already
	// expanded a leading ~, and os.ReadFile would take "~/…" literally.
	token, err := os.ReadFile(cfg.TokenFile)
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	// pve.Config.Host carries the port as "host:port" (a bare host gets :8006);
	// reconstruct the pair the profile split apart in proxmoxE2ETargetConfig.
	host := cfg.Host
	if cfg.Port > 0 {
		host = net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	}
	client, err := pve.New(pve.Config{
		Host:               host,
		Node:               cfg.Node,
		TokenID:            strings.TrimSpace(string(token)),
		InsecureSkipVerify: cfg.Insecure,
	})
	if err != nil {
		t.Fatalf("pve.New: %v", err)
	}
	return client
}

// proxmoxE2EVMID resolves a pool member's name to its VMID through the same
// pool listing the provider uses, so a test can address it on the raw client.
func proxmoxE2EVMID(t *testing.T, client *pve.Client, pool, name string) int {
	t.Helper()
	vms, err := client.ListVMs(context.Background(), pool)
	if err != nil {
		t.Fatalf("listing pool %q: %v", pool, err)
	}
	for _, r := range vms {
		if r.Name == name {
			return r.VMID
		}
	}
	t.Fatalf("%s is not in pool %q: %+v", name, pool, vms)
	return 0
}

// TestE2EProxmoxLifecycle is one cohesive integration test: preflight, create
// (which builds the base template on the first run), list, a plain
// non-interactive ShellOut, host-resource sampling, reset, and delete.
func TestE2EProxmoxLifecycle(t *testing.T) {
	cfg := skipUnlessProxmoxE2EConfigured(t)

	prov, err := provider.NewProxmox(cfg)
	if err != nil {
		t.Fatalf("NewProxmox: %v", err)
	}
	if err := prov.Preflight(); err != nil {
		t.Fatalf("Preflight (is the token scoped to the pool, and the storage images-capable?): %v", err)
	}

	// A name unique enough that a leftover from an interrupted run cannot collide.
	name := "sand-pve-e2e-" + strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)

	// Unconditional teardown registered immediately, so a mid-test failure still
	// removes the VM — matching every other e2e test in this repo.
	t.Cleanup(func() { _ = prov.Delete(name, true) })

	vmCfg := vm.CreateConfig{
		Name: name,
		// Not optional, and not defaulted for us: this is a bare literal, not
		// vm.DefaultCreateConfig(), so an unset BaseName is the empty string —
		// which the provider happily uses to CREATE the base VM and only trips
		// over one step later, when start("") resolves to no such instance.
		BaseName: vm.DefaultCreateConfig().BaseName,
		User:     os.Getenv(proxmoxE2ESSHUser),
		GitName:  "Sand PVE E2E",
		GitEmail: "sand-pve-e2e@example.com",
		CPUs:     2,
		Memory:   "2GiB",
		Disk:     vm.BaseDiskFloor,
		Domain:   "lan",
		Locale:   "en_US.UTF-8",
		// Tool flags left at their zero value: this test exercises the Proxmox
		// transport and lifecycle, not the base's installed tooling.
	}

	ctx := context.Background()
	var createLog bytes.Buffer
	if err := prov.Create(ctx, vmCfg, provision.CreateOptions{}, &createLog); err != nil {
		t.Fatalf("Create: %v\n%s", err, createLog.String())
	}

	// --- List() sees it ------------------------------------------------------
	vms, err := prov.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !proxmoxContainsVM(vms, name) {
		t.Fatalf("%s missing from List() after Create: %+v", name, vms)
	}

	// --- a plain ShellOut round-trips ---------------------------------------
	back, err := prov.ShellOut(ctx, name, "echo", "sentinel-42")
	if err != nil {
		t.Fatalf("ShellOut: %v", err)
	}
	if got := strings.TrimSpace(string(back)); got != "sentinel-42" {
		t.Fatalf("ShellOut echo = %q, want %q", got, "sentinel-42")
	}

	// --- host resources come from the API -----------------------------------
	hr := prov.HostResources()
	if hr.CPUs <= 0 || hr.MemBytes <= 0 {
		t.Fatalf("HostResources() = %+v, want non-zero CPUs and memory sampled from the node", hr)
	}
	// Disk is only asserted non-zero when the storage reported a size — an
	// inactive storage legitimately leaves it 0 ("unknown"), which is the whole
	// point of the no-false-warning contract, so a 0 here is not a failure.

	// --- reset recreates it in place ----------------------------------------
	var resetLog bytes.Buffer
	if err := prov.Reset(ctx, vmCfg, provision.ResetOptions{}, &resetLog); err != nil {
		t.Fatalf("Reset: %v\n%s", err, resetLog.String())
	}
	if _, err := prov.Get(name); err != nil {
		t.Fatalf("Get after Reset: %v", err)
	}

	// --- delete removes it ---------------------------------------------------
	if err := prov.Delete(name, true); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if vms, err := prov.List(); err != nil {
		t.Fatalf("List after Delete: %v", err)
	} else if proxmoxContainsVM(vms, name) {
		t.Fatalf("%s still present after Delete: %+v", name, vms)
	}
}

// TestE2EProxmoxPoolIsolation proves the plan's central security claim: a
// pool-scoped token is structurally UNABLE to touch a VM outside its pool.
//
// It is deliberately adversarial rather than confirmatory. It operates on a VM
// it did NOT create and must NOT clean up — the operator supplies its VMID via
// PROXMOX_E2E_FOREIGN_VMID — and every assertion checks for a PERMISSION error
// specifically. A test that merely asserted "an error occurred" would pass just
// as happily if the VMID did not exist, proving nothing at all.
func TestE2EProxmoxPoolIsolation(t *testing.T) {
	cfg := skipUnlessProxmoxE2EConfigured(t)

	foreign := os.Getenv(proxmoxE2EForeignVMID)
	if foreign == "" {
		t.Skipf("set %s to a VMID OUTSIDE the pool to run the isolation proof", proxmoxE2EForeignVMID)
	}
	foreignVMID, err := strconv.Atoi(foreign)
	if err != nil {
		t.Fatalf("%s=%q is not a VMID: %v", proxmoxE2EForeignVMID, foreign, err)
	}

	// A raw client scoped exactly as the provider is, so the isolation assertions
	// hit the same token against the same node — the provider's own Get/Stop/
	// Delete resolve names through a pool listing, and a foreign VM is not in it,
	// so this drives the client at the VMID directly, which is the sharper test:
	// it proves the TOKEN is refused, not merely that the name is unlisted.
	client := proxmoxE2EClient(t, cfg)

	ctx := context.Background()

	// Read: the token must not even be able to SEE the foreign VM's status.
	if _, err := client.GetStatus(ctx, foreignVMID); err == nil {
		t.Fatalf("the pool-scoped token READ a VM outside its pool (VMID %d) — isolation is broken", foreignVMID)
	} else if !pve.IsPermission(err) {
		t.Fatalf("GetStatus(foreign) failed with %v; want a PERMISSION error. A non-permission error here does not prove isolation (the VM may simply not exist).", err)
	}

	// Power: the token must not be able to stop it.
	if _, err := client.StopVM(ctx, foreignVMID); err == nil {
		t.Fatalf("the pool-scoped token STOPPED a VM outside its pool (VMID %d) — isolation is broken", foreignVMID)
	} else if !pve.IsPermission(err) {
		t.Fatalf("StopVM(foreign) failed with %v; want a PERMISSION error", err)
	}

	// Delete: the token must not be able to destroy it.
	if _, err := client.DeleteVM(ctx, foreignVMID, false); err == nil {
		t.Fatalf("the pool-scoped token DELETED a VM outside its pool (VMID %d) — isolation is CATASTROPHICALLY broken", foreignVMID)
	} else if !pve.IsPermission(err) {
		t.Fatalf("DeleteVM(foreign) failed with %v; want a PERMISSION error", err)
	}

	// The "still exists, unchanged" half of the proof is verified out of band in
	// the docs' setup-verification step: this suite's token, by design, cannot
	// read the foreign VM's state to confirm it, and borrowing an admin token
	// into an automated test would defeat the very isolation being proven. The
	// three permission refusals above are what a token CAN establish on its own.
}

// TestE2EProxmoxSessionDiesWhenTheGuestVanishes proves the keepalive fix does
// what it claims on a real guest, which no unit test can reach.
//
// TestSSHKeepalivesAlwaysThreaded (internal/lima) pins ServerAliveInterval and
// ServerAliveCountMax onto every ssh argv this codebase builds. That is an argv
// assertion: it proves the options are REQUESTED, never that OpenSSH honours
// them against a peer that has actually stopped answering. The bug being
// guarded is a hang, and a hang is only observable end to end.
//
// The cut is a HARD stop (PVE's status/stop, not status/shutdown), because the
// point is a guest that vanishes WITHOUT closing its TCP connections. A clean
// shutdown sends FIN, ssh notices immediately, and the test would pass with the
// keepalives stripped out — proving nothing.
//
// Two deliberate choices keep this honest rather than merely green:
//
//   - The silent command runs on context.Background(), NOT a context with a
//     deadline. A deadline would cancel the call itself, so the test would pass
//     on a build where the keepalives do nothing. The ONLY thing that may end
//     that call is ssh giving up. The ceiling below is enforced outside it, and
//     tripping it is a FAILURE, not a cancellation.
//   - The bound is deliberately loose. The fix targets ~120s (15s x 8), and the
//     bug it replaced was an unbounded wait against the kernel's two-hour idle
//     timer. Asserting a tight window around 120s on a shared hypervisor would
//     buy nothing and flake; a five-minute ceiling is still 24x tighter than the
//     behaviour being guarded against, and cannot flake under ordinary load.
func TestE2EProxmoxSessionDiesWhenTheGuestVanishes(t *testing.T) {
	const (
		// Long enough that the session is unambiguously established and idle
		// before the power is cut — the failure being reproduced is an IDLE
		// session reaped mid-task, not a connection that never came up.
		idleBeforeCut = 20 * time.Second
		// See the doc comment: loose on purpose.
		mustDieWithin = 5 * time.Minute
		// Comfortably longer than the ceiling, so the command's own completion
		// can never be what ends the wait.
		silentFor = "600"
	)

	cfg := skipUnlessProxmoxE2EConfigured(t)

	prov, err := provider.NewProxmox(cfg)
	if err != nil {
		t.Fatalf("NewProxmox: %v", err)
	}
	if err := prov.Preflight(); err != nil {
		t.Fatalf("Preflight: %v", err)
	}

	name := "sand-pve-cut-" + strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)
	t.Cleanup(func() { _ = prov.Delete(name, true) })

	vmCfg := vm.CreateConfig{
		Name:     name,
		BaseName: vm.DefaultCreateConfig().BaseName,
		User:     os.Getenv(proxmoxE2ESSHUser),
		GitName:  "Sand PVE E2E",
		GitEmail: "sand-pve-e2e@example.com",
		CPUs:     2,
		Memory:   "2GiB",
		Disk:     vm.BaseDiskFloor,
		Domain:   "lan",
		Locale:   "en_US.UTF-8",
	}

	ctx := context.Background()
	var createLog bytes.Buffer
	if err := prov.Create(ctx, vmCfg, provision.CreateOptions{}, &createLog); err != nil {
		t.Fatalf("Create: %v\n%s", err, createLog.String())
	}

	// Establish that the transport works BEFORE breaking it, so a failure below
	// cannot be quietly explained by a guest that was never reachable.
	if _, err := prov.ShellOut(ctx, name, "true"); err != nil {
		t.Fatalf("the guest was not reachable before the cut, so this test proves nothing: %v", err)
	}

	client := proxmoxE2EClient(t, cfg)
	vmid := proxmoxE2EVMID(t, client, cfg.Pool, name)

	type outcome struct {
		err     error
		elapsed time.Duration
	}
	done := make(chan outcome, 1)
	started := time.Now()
	go func() {
		_, err := prov.ShellOut(context.Background(), name, "sleep", silentFor)
		done <- outcome{err: err, elapsed: time.Since(started)}
	}()

	// Let it go quiet on the channel, and make sure it really is still running.
	select {
	case got := <-done:
		t.Fatalf("`sleep %s` returned after %s, before the guest was cut — the session ended for some reason other than the one under test: %v",
			silentFor, got.elapsed, got.err)
	case <-time.After(idleBeforeCut):
	}

	// Pull the power. No FIN, no RST: from the client's side the peer simply
	// stops answering, which is the condition the keepalives exist to detect.
	if _, err := client.StopVM(context.Background(), vmid); err != nil {
		t.Fatalf("hard-stopping %s (VMID %d): %v", name, vmid, err)
	}
	cut := time.Now()

	select {
	case got := <-done:
		if got.err == nil {
			t.Fatalf("`sleep %s` returned SUCCESSFULLY %s after the guest was hard-stopped — the session cannot have survived, so this result is not trustworthy",
				silentFor, time.Since(cut))
		}
		// Any error would end the wait; only a TRANSPORT error means the
		// keepalives did it. Without this the test would pass on an unrelated
		// failure and quietly stop guarding anything.
		if !strings.Contains(got.err.Error(), "the ssh transport failed") {
			t.Fatalf("the session ended %s after the cut, but not as a transport failure — got %v; want transportError's annotation (ssh exit %d)",
				time.Since(cut), got.err, 255)
		}
		t.Logf("the session was declared dead %s after the guest vanished (%s total)", time.Since(cut).Round(time.Second), got.elapsed.Round(time.Second))
	case <-time.After(mustDieWithin):
		t.Fatalf("the ssh session was STILL blocked %s after the guest was hard-stopped: the keepalives are not ending a session whose peer stopped answering, which is the exact hang they were added to bound",
			mustDieWithin)
	}
}

// TestE2EProxmoxTwoClonesHoldDistinctLeases is the end-to-end proof of the
// DHCP-collision fix, against the real hypervisor, DHCP server, and guests.
//
// The failure it guards: a base template that kept the /etc/machine-id its
// build's boot committed gave every clone the same systemd-networkd DHCP
// client identity, the DHCP server handed them all ONE lease, and the clones
// fought over the address at the ARP layer — the board's guest probes flapped
// with "lost the guest connection; retrying in 5s" forever, and any ssh could
// silently land in the wrong VM. Reproduced verbatim on a real pool (two
// clones, distinct MACs, both reporting one IPv4) before the template build
// learned to reset the identity (generalizeScript).
//
// Three assertions, in causal order:
//
//  1. The clones' machine-ids DIFFER (and are real 32-hex ids, not empty —
//     proving the truncated file was regenerated, not left blank).
//  2. Their IPv4 leases differ — the identity fix's observable purpose.
//  3. Long-lived streams into BOTH guests, held OPEN CONCURRENTLY, survive a
//     window comfortably longer than the flap's period. This is the board's
//     heartbeat shape (one ShellStreamOut per VM), and it is the assertion
//     that fails first if the guests are fighting over an address.
//
// Creating two full VMs is the most expensive test in the suite; it exists
// because nothing short of two real clones behind a real DHCP server can prove
// any of the three.
func TestE2EProxmoxTwoClonesHoldDistinctLeases(t *testing.T) {
	cfg := skipUnlessProxmoxE2EConfigured(t)

	prov, err := provider.NewProxmox(cfg)
	if err != nil {
		t.Fatalf("NewProxmox: %v", err)
	}
	if err := prov.Preflight(); err != nil {
		t.Fatalf("Preflight: %v", err)
	}

	suffix := strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)
	names := []string{"sand-pve-lease-a-" + suffix, "sand-pve-lease-b-" + suffix}
	for _, name := range names {
		t.Cleanup(func() { _ = prov.Delete(name, true) })
	}

	ctx := context.Background()
	for _, name := range names {
		vmCfg := vm.CreateConfig{
			Name:     name,
			BaseName: vm.DefaultCreateConfig().BaseName,
			User:     os.Getenv(proxmoxE2ESSHUser),
			GitName:  "Sand PVE E2E",
			GitEmail: "sand-pve-e2e@example.com",
			CPUs:     2,
			Memory:   "2GiB",
			Disk:     vm.BaseDiskFloor,
			Domain:   "lan",
			Locale:   "en_US.UTF-8",
		}
		var createLog bytes.Buffer
		if err := prov.Create(ctx, vmCfg, provision.CreateOptions{}, &createLog); err != nil {
			t.Fatalf("Create %s: %v\n%s", name, err, createLog.String())
		}
	}

	// --- 1. distinct, well-formed machine identities -------------------------
	ids := make([]string, len(names))
	for i, name := range names {
		out, err := prov.ShellOut(ctx, name, "cat", "/etc/machine-id")
		if err != nil {
			t.Fatalf("reading %s's machine-id: %v", name, err)
		}
		ids[i] = strings.TrimSpace(string(out))
		if len(ids[i]) != 32 {
			t.Fatalf("%s's machine-id is %q; want a regenerated 32-hex id — an empty one means the truncated file was never re-initialized", name, ids[i])
		}
	}
	if ids[0] == ids[1] {
		t.Fatalf("both clones carry machine-id %s — the template still bakes an identity in, and their DHCP leases will collide", ids[0])
	}

	// --- 2. distinct leases ---------------------------------------------------
	addrs := make([]string, len(names))
	for i, name := range names {
		out, err := prov.ShellOut(ctx, name, "hostname", "-I")
		if err != nil {
			t.Fatalf("reading %s's addresses: %v", name, err)
		}
		fields := strings.Fields(string(out))
		if len(fields) == 0 {
			t.Fatalf("%s reports no address at all", name)
		}
		addrs[i] = fields[0]
	}
	if addrs[0] == addrs[1] {
		t.Fatalf("both clones hold the lease %s — the DHCP server still sees one client identity", addrs[0])
	}
	t.Logf("distinct identities (%s…, %s…) and distinct leases (%s, %s)", ids[0][:8], ids[1][:8], addrs[0], addrs[1])

	// --- 3. concurrent long-lived streams survive -----------------------------
	// One streaming shell per VM, open at the same time, each ticking for over
	// a minute — the exact shape of the board's per-VM heartbeat, and several
	// times the observed flap period (a probe died within seconds of its rival
	// VM's probe opening). ShellStreamOut must return nil: an address fight
	// surfaces as an ssh transport failure (exit 255) mid-stream.
	const ticks, tickEvery = 24, "3"
	type result struct {
		name  string
		lines int
		err   error
	}
	results := make(chan result, len(names))
	for _, name := range names {
		go func(name string) {
			var out bytes.Buffer
			err := prov.ShellStreamOut(ctx, name, nil, &out,
				"sh", "-c", "for i in $(seq 1 "+strconv.Itoa(ticks)+"); do echo tick-$i; sleep "+tickEvery+"; done")
			results <- result{name: name, lines: strings.Count(out.String(), "tick-"), err: err}
		}(name)
	}
	for range names {
		got := <-results
		if got.err != nil {
			t.Errorf("the long-lived stream into %s died while its sibling's stream was open — the address flap's signature: %v", got.name, got.err)
			continue
		}
		if got.lines != ticks {
			t.Errorf("%s's stream delivered %d/%d ticks; a dropped or hijacked connection loses output", got.name, got.lines, ticks)
		}
	}
}

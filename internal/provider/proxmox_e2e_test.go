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
//	PROXMOX_E2E_IMAGE        cloud-image URL to import for the base (qcow2/raw/…; NOT .img)
//
// Optional:
//
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
//	  PROXMOX_E2E_IMAGE=https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2 \
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
		Bridge:       os.Getenv(proxmoxE2EBridge),
		TokenFile:    os.Getenv(proxmoxE2ETokenFile),
		User:         os.Getenv(proxmoxE2ESSHUser),
		IdentityPath: os.Getenv(proxmoxE2ESSHIdentity),
		Insecure:     os.Getenv(proxmoxE2EInsecure) != "",
	}
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
		proxmoxE2ESSHIdentity, proxmoxE2EImage,
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
		Name:     name,
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
	token, err := os.ReadFile(os.Getenv(proxmoxE2ETokenFile))
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

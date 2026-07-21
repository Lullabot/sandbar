package provider

// proxmoxprovision.go is the Proxmox backend's whole provisioning lifecycle:
// building the sandbar base as a PVE TEMPLATE, cloning VMs from it, and the
// Create / Recreate / Reset methods the Provider seam requires. It is the one
// place this backend diverges hardest from Lima — there is no `limactl clone`,
// no Lima overlay, no host mount for the playbook — so the mechanism is rebuilt
// on PVE's own primitives (internal/pve) plus SSH to the guest (proxmox.go's
// transport), while REUSING the parts of internal/provision that are backend-
// agnostic: BuildExtraVars (the toolset selection), LocatePlaybook, the base
// version stamp, and the base advisory lock.
//
// Several behaviours here are not stylistic choices but correctness invariants
// verified against Proxmox's own source; each is called out at its site:
//
//   - `pool` is passed on BOTH the base create and the clone. Pool membership is
//     what makes every later permission check succeed under a pool-scoped token;
//     omitting it silently breaks the whole isolation model (see pve.CreateVMOptions).
//   - Clones from one template are serialized. Parallel clones contend on the
//     template's SERVER-SIDE flock (10s timeout, no token-reachable unlock), so
//     the only fix is to never contend — a reliable in-process mutex plus the
//     cross-process advisory lock (see ensureBaseAndClone).
//   - A cloud-init config write is always followed by a regenerate (or a boot):
//     a config write alone does not rebuild the cloud-init image.
//   - Only storage-backed volids are used for import; absolute filesystem paths
//     are hard-gated to root@pam and fail even for a root@pam API TOKEN.
//   - WaitTask's verdict is trusted. It already classifies "WARNINGS: n" as
//     success; a second "is it really running" status poll after it is exactly
//     where the duplicate-VM bug creeps back in, so there is none.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	sandbar "github.com/lullabot/sandbar"
	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/pve"
	"github.com/lullabot/sandbar/internal/vm"
)

// baseImageURL / baseImageFile are the DEFAULT cloud image the base template is
// built from, used when a profile sets no base_image (see NewProxmox, which
// copies these into the provider's own fields). They are VARS, not consts, so a
// test can point them at a rejected extension to exercise the early guard
// without a real download.
//
// The default is the project's own golden image: upstream Debian genericcloud
// with qemu-guest-agent baked in, published by .github/workflows/base-image.yml.
// sand needs the agent running on first boot to learn a VM's IP, and stock cloud
// images don't ship it — so the default carries it rather than making every user
// build their own. Bumping to a newer monthly build means updating all three of
// URL, file, and defaultBaseImageSHA256 together (the workflow publishes the
// matching .sha256 asset).
var (
	baseImageURL  = "https://github.com/Lullabot/sandbar/releases/download/base-image-2026.07.21/sandbar-base-debian-13-amd64.qcow2"
	baseImageFile = "sandbar-base-debian-13-amd64.qcow2"
)

// defaultBaseImageSHA256 pins the default image so PVE's download-url verifies it
// server-side — the default image is downloaded and BOOTED, so its integrity is
// worth checking. Empty for a custom base_image (a user's own URL carries no
// checksum here). Keep in lockstep with baseImageURL above.
const defaultBaseImageSHA256 = "57500f861b5a2e5a12a9d90a3046aae09b49d4a49bbfa7b1a9b48ff62b4b4659"

// acceptedImportExts is the extension set PVE's download-url endpoint accepts for
// content=import. `.img` is deliberately ABSENT: PVE rejects it outright, so a
// configured .img image must fail early here with a message naming this set,
// rather than as an opaque task failure minutes into a build.
var acceptedImportExts = []string{"ova", "ovf", "qcow2", "raw", "vmdk"}

// locatePlaybookFn is indirected through a package var so a test can point the
// playbook at a hermetic fixture dir instead of the real repo/embedded fileset —
// the same seam pattern internal/provision uses for its own filesystem touches.
var locatePlaybookFn = provision.LocatePlaybook

// readPublicKey returns the SSH PUBLIC key cloud-init installs for the guest
// login user, read from "<identityPath>.pub" (identityPath is the PRIVATE key the
// ssh transport authenticates with). Indirected through a var so a test need not
// materialise a real key pair on disk.
var readPublicKey = func(identityPath string) (string, error) {
	if identityPath == "" {
		return "", errors.New("no identity_path configured, so cloud-init has no public key to install for the guest login user")
	}
	data, err := os.ReadFile(identityPath + ".pub")
	if err != nil {
		return "", fmt.Errorf("reading %s.pub: %w", identityPath, err)
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", fmt.Errorf("%s.pub is empty", identityPath)
	}
	return key, nil
}

// cloneSerial holds one in-process mutex per (endpoint, base template), keyed so
// two provider instances aimed at the same pool still serialize — which matches
// the reality they are contending over (one template's server-side flock). It is
// the RELIABLE half of clone serialization: unlike the advisory file lock it
// cannot fail open, so an in-process pair of clones is guaranteed never to
// overlap even on a host where the lock file cannot be written.
var cloneSerial sync.Map // key string -> *sync.Mutex

// --- lifecycle entry points -----------------------------------------------------
//
// proxmox.go's Create/Recreate/Reset delegate straight to these, keeping that
// file's edit to three one-line bodies.

// createInstance is Create: refuse an existing target FIRST (the interface
// contract, and it avoids a partial clone that would then need cleaning up), then
// build/clone/finalize.
func (p *proxmoxProvider) createInstance(ctx context.Context, cfg vm.CreateConfig, opts provision.CreateOptions, out io.Writer) error {
	if _, err := p.Get(cfg.Name); err == nil {
		return fmt.Errorf("proxmox: vm %q already exists in pool %q — delete it first, or choose a different name", cfg.Name, p.pool)
	} else if !errors.Is(err, lima.ErrNoSuchInstance) {
		// A permission error (or any non-absence failure) must not be read as
		// "safe to create": that is how a duplicate gets built on a VM the token
		// simply could not see.
		return err
	}
	return p.provisionClone(ctx, cfg, opts, out)
}

// recreateInstance is Recreate: force-delete the target, then clone — skipping
// createInstance's exists-guard, since we just removed it and re-querying could
// spuriously refuse the recreate before the delete has settled. opts carries the
// base intent, so `create --recreate --rebuild` still asks for a base rebuild.
func (p *proxmoxProvider) recreateInstance(ctx context.Context, cfg vm.CreateConfig, opts provision.CreateOptions, out io.Writer) error {
	if err := p.Delete(cfg.Name, true); err != nil && !errors.Is(err, lima.ErrNoSuchInstance) {
		return fmt.Errorf("proxmox: deleting %s for recreate: %w", cfg.Name, err)
	}
	return p.provisionClone(ctx, cfg, opts, out)
}

// provisionClone is the create body shared by Create and Recreate: the serialized
// base-and-clone critical section, then the unserialized finalize. A finalize
// failure deletes the partial VM (purge=1) so a failed create never leaves a
// half-built VM occupying a VMID on the cluster.
func (p *proxmoxProvider) provisionClone(ctx context.Context, cfg vm.CreateConfig, opts provision.CreateOptions, out io.Writer) error {
	cloneVMID, err := p.ensureBaseAndClone(ctx, cfg, opts, out)
	if err != nil {
		return err
	}
	if err := p.finalizeClone(ctx, cloneVMID, cfg, out); err != nil {
		p.cleanupVM(ctx, cloneVMID, cfg.Name, out)
		return fmt.Errorf("proxmox: creating %s (removed the partial VM): %w", cfg.Name, err)
	}
	progress(out, "%s is ready\n", cfg.Name)
	return nil
}

// ensureBaseAndClone is the base template's WHOLE critical section — prepare it,
// then clone from it — held under BOTH the in-process clone mutex and the
// cross-process advisory lock, mirroring the Lima provisioner's
// prepareBaseAndClone. The clone MUST be inside the lock: parallel clones from
// one template contend on its server-side flock (a 10s timeout that no client
// timeout can extend and no token identity can release), so the design never
// lets two clones of one template run at once. The expensive finalize that
// follows runs OUTSIDE the lock, so concurrent creates still overlap on the part
// concurrency was for.
func (p *proxmoxProvider) ensureBaseAndClone(ctx context.Context, cfg vm.CreateConfig, opts provision.CreateOptions, out io.Writer) (int, error) {
	unlock := p.lockCloneSerial(cfg.BaseName)
	defer unlock()

	// Cross-process serialization on top of the in-process mutex: two `sand`
	// processes on the same machine must not build or clone the same template at
	// once either. Best-effort by contract (see provision.LockBase) — the mutex
	// above is the guarantee, this is the cross-process add-on.
	release, err := provision.LockBase(ctx, p.files, cfg.BaseName, out)
	if err != nil {
		return 0, err // only a cancelled context reaches here
	}
	defer release()

	templateVMID, err := p.ensureBaseTemplate(ctx, cfg, opts, out)
	if err != nil {
		return 0, err
	}
	return p.cloneFromTemplate(ctx, templateVMID, cfg, out)
}

// lockCloneSerial takes the per-(endpoint, base) in-process mutex and returns its
// unlock. Keyed by the endpoint identity plus the base name so two providers for
// the same pool serialize against each other, since they clone from the same
// server-side template.
func (p *proxmoxProvider) lockCloneSerial(baseName string) func() {
	key := strings.Join([]string{p.host, p.node, p.pool, baseName}, "\x00")
	muAny, _ := cloneSerial.LoadOrStore(key, &sync.Mutex{})
	mu := muAny.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// --- base template --------------------------------------------------------------

// ensureBaseTemplate makes sure a current base TEMPLATE exists in the pool and
// returns its VMID. It has three outcomes: reuse the existing template (present
// and current), rebuild it from scratch (absent, or --rebuild asked, or the
// playbook it was stamped with is stale), or build it for the first time.
//
// A stale template is REBUILT rather than converged in place: a PVE template
// cannot be started, so there is no equivalent of Lima's in-place re-apply. The
// staleness signal is the same content-hash stamp the other providers use (via
// provision.ReadBaseVersion), so a base built by any provider is judged
// identically.
func (p *proxmoxProvider) ensureBaseTemplate(ctx context.Context, cfg vm.CreateConfig, opts provision.CreateOptions, out io.Writer) (int, error) {
	vmid, exists, err := p.lookupTemplate(ctx, cfg.BaseName)
	if err != nil {
		return 0, err
	}

	if exists {
		rebuild := opts.Rebuild
		switch {
		case opts.Rebuild:
			progress(out, "Rebuilding base template %s from scratch (requested)\n", cfg.BaseName)
		default:
			if stale, why := p.baseStale(cfg); stale {
				progress(out, "Base template %s is stale (%s); rebuilding it from scratch\n", cfg.BaseName, why)
				rebuild = true
			}
		}
		if rebuild {
			if err := p.destroyVM(ctx, vmid, out); err != nil {
				return 0, fmt.Errorf("proxmox: deleting base template %s for rebuild: %w", cfg.BaseName, err)
			}
			p.forget(cfg.BaseName)
			exists = false
		}
	}

	if exists {
		progress(out, "Reusing base template %s (VMID %d)\n", cfg.BaseName, vmid)
		return vmid, nil
	}
	return p.buildBaseTemplate(ctx, cfg, out)
}

// lookupTemplate resolves a base name to its VMID from a fresh pool listing,
// reporting exists=false (not an error) when the pool holds no such VM.
func (p *proxmoxProvider) lookupTemplate(ctx context.Context, name string) (vmid int, exists bool, err error) {
	vmid, err = p.lookupVMID(ctx, name)
	if err != nil {
		if errors.Is(err, lima.ErrNoSuchInstance) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return vmid, true, nil
}

// baseStale reports whether the existing template was built from a different
// playbook version than the one that would be used now, and a short reason. A
// missing/unreadable stamp counts as stale (build it once with a stamp); a
// version-lookup failure counts as NOT stale (reuse rather than churn), matching
// the Lima flow's posture.
func (p *proxmoxProvider) baseStale(cfg vm.CreateConfig) (bool, string) {
	dir, err := locatePlaybookFn()
	if err != nil {
		return false, ""
	}
	want, err := provision.PlaybookVersion(os.DirFS(dir), cfg.ToolsetKey())
	if err != nil {
		return false, ""
	}
	have := provision.ReadBaseVersion(p.files, cfg.BaseName)
	switch {
	case have == want:
		return false, ""
	case have == "":
		return true, "it has no recorded playbook version"
	default:
		return true, "the playbook fileset or tool-set has changed since it was built"
	}
}

// buildBaseTemplate creates the base VM from the cloud image, provisions it over
// SSH, stops it, and converts it to a template. Any failure after the VM is
// created deletes the partial base (purge=1) so a retry starts from a clean slate
// — mirroring the Lima flow's cleanup of a half-built base.
func (p *proxmoxProvider) buildBaseTemplate(ctx context.Context, cfg vm.CreateConfig, out io.Writer) (int, error) {
	progress(out, "Building base template %s — downloads the cloud image and boots once; the first run takes several minutes\n", cfg.BaseName)

	volid, err := p.ensureCloudImage(ctx, out)
	if err != nil {
		return 0, err
	}

	pubkey, err := readPublicKey(p.identityPath)
	if err != nil {
		return 0, fmt.Errorf("proxmox: the base VM needs an SSH public key for cloud-init: %w", err)
	}

	progress(out, "Creating base VM %s importing %s\n", cfg.BaseName, volid)
	vmid, upid, err := p.client.CreateVMWithNextID(ctx, pve.CreateVMOptions{
		Name:    cfg.BaseName,
		Cores:   cfg.CPUs,
		Memory:  memMiB(cfg.Memory),
		Storage: p.storage,
		Bridge:  p.bridge,
		// Pool membership on the base create is non-negotiable: it is what makes
		// every later permission check on this VM (and, once it is a template, on
		// clones of it) succeed under a pool-scoped token.
		Pool:    p.pool,
		CIUser:  p.ciUser,
		SSHKeys: []string{pubkey},
		// A storage-backed import volid, never an absolute filesystem path:
		// absolute paths are hard-gated to root@pam and fail even for a root@pam
		// API token.
		ImportFrom: volid,
	})
	if err != nil {
		return 0, fmt.Errorf("proxmox: creating base VM %s: %w", cfg.BaseName, err)
	}
	if err := p.client.WaitTask(ctx, upid.Raw); err != nil {
		p.cleanupVM(ctx, vmid, cfg.BaseName, out)
		return 0, fmt.Errorf("proxmox: creating base VM %s: %w", cfg.BaseName, err)
	}
	p.setVMID(cfg.BaseName, vmid)

	if err := p.provisionBase(ctx, vmid, cfg, out); err != nil {
		p.cleanupVM(ctx, vmid, cfg.BaseName, out)
		return 0, err
	}
	return vmid, nil
}

// provisionBase carries the created base VM through resize → boot → playbook →
// stop → templatize → stamp. Every step writes a progress line so the multi-
// minute build shows movement.
func (p *proxmoxProvider) provisionBase(ctx context.Context, vmid int, cfg vm.CreateConfig, out io.Writer) error {
	// Grow the imported disk to the base floor. Clones grow FROM this floor (a
	// qcow2 cannot shrink live), so the base is deliberately built small.
	if err := p.resizeDisk(ctx, vmid, "scsi0", vm.BaseDiskFloor, out); err != nil {
		return err
	}
	// Boot and wait until the guest is actually reachable (agent up, net0
	// addressed) — start() caches the resolved address for the SSH steps below.
	if err := p.start(ctx, cfg.BaseName, out); err != nil {
		return err
	}
	// Install just enough to run the playbook (ansible-core, rsync, and the apt
	// prerequisites), then run the heavy base phase over SSH.
	if err := p.installBaseDeps(ctx, cfg.BaseName, out); err != nil {
		return err
	}
	if err := p.runPlaybookPhase(ctx, cfg.BaseName, cfg, "base", cfg.BaseName, out); err != nil {
		return err
	}
	// A template must be built from an idle disk, so stop the VM first.
	if err := p.stop(ctx, cfg.BaseName, out); err != nil {
		return err
	}
	progress(out, "Converting %s to a template\n", cfg.BaseName)
	tUPID, err := p.client.ConvertToTemplate(ctx, vmid)
	if err != nil {
		return fmt.Errorf("proxmox: converting %s to a template: %w", cfg.BaseName, err)
	}
	if err := p.client.WaitTask(ctx, tUPID.Raw); err != nil {
		return fmt.Errorf("proxmox: converting %s to a template: %w", cfg.BaseName, err)
	}
	// Stamp the playbook version this base was built from so a later create can
	// detect drift and rebuild. Best-effort: an unstamped base simply reads as
	// stale and is rebuilt next time.
	p.stampBaseVersion(cfg, out)
	return nil
}

// ensureCloudImage returns the storage-backed import volid for the cloud image,
// downloading it once if absent. It fails EARLY when the configured image has an
// extension PVE's download-url rejects (notably .img), naming the accepted set,
// rather than letting the download task fail opaquely minutes later.
func (p *proxmoxProvider) ensureCloudImage(ctx context.Context, out io.Writer) (string, error) {
	if !acceptedImportExt(p.baseImageFile) {
		return "", fmt.Errorf("proxmox: cloud image %q has an extension PVE's download-url rejects; convert it to one of %s first", p.baseImageFile, strings.Join(acceptedImportExts, "|"))
	}
	// The image is downloaded onto the file-based imageStorage (content=import),
	// NOT the VM-disk storage — a block disk storage (zfspool, lvm-thin) rejects
	// content=import. buildBaseTemplate then imports the disk onto p.storage FROM
	// this volid (import-from allows a cross-storage source). See the imageStorage
	// field's doc and Preflight's import-content check.
	volid := fmt.Sprintf("%s:import/%s", p.imageStorage, p.baseImageFile)

	items, err := p.client.StorageContent(ctx, p.imageStorage)
	if err != nil {
		return "", fmt.Errorf("proxmox: listing image storage %q content: %w", p.imageStorage, err)
	}
	for _, it := range items {
		if it.VolID == volid {
			progress(out, "Cloud image already present (%s)\n", volid)
			return volid, nil
		}
	}

	progress(out, "Downloading cloud image %s into %s\n", p.baseImageURL, p.imageStorage)
	opts := pve.DownloadURLOptions{
		Content:  "import",
		Filename: p.baseImageFile,
		URL:      p.baseImageURL,
	}
	// Pin the checksum for the default image so PVE verifies the download
	// server-side (empty for a custom base_image — see NewProxmox).
	if p.baseImageSHA256 != "" {
		opts.Checksum = p.baseImageSHA256
		opts.ChecksumAlgorithm = "sha256"
	}
	dlUPID, err := p.client.DownloadURL(ctx, p.imageStorage, opts)
	if err != nil {
		return "", fmt.Errorf("proxmox: downloading cloud image into %q: %w", p.imageStorage, err)
	}
	if err := p.client.WaitTask(ctx, dlUPID.Raw); err != nil {
		return "", fmt.Errorf("proxmox: downloading cloud image into %q: %w", p.imageStorage, err)
	}
	return volid, nil
}

// stampBaseVersion records the playbook version and time the base was built from,
// through the shared provision machinery so staleness detection is identical to
// the other providers. Best-effort, exactly like the Lima flow.
func (p *proxmoxProvider) stampBaseVersion(cfg vm.CreateConfig, out io.Writer) {
	dir, err := locatePlaybookFn()
	if err != nil {
		progress(out, "Note: could not locate the playbook to stamp the base version (%v); it will rebuild on the next create\n", err)
		return
	}
	version, err := provision.PlaybookVersion(os.DirFS(dir), cfg.ToolsetKey())
	if err != nil {
		progress(out, "Note: could not compute the playbook version (%v); the base will rebuild on the next create\n", err)
		return
	}
	if err := provision.WriteBaseVersion(p.files, cfg.BaseName, version, time.Now()); err != nil {
		progress(out, "Note: could not record the base template's playbook version (%v); it will rebuild on the next create\n", err)
	}
}

// --- clone ----------------------------------------------------------------------

// cloneFromTemplate full-clones a new VM from the template into the target pool
// and storage, waits for the clone task, and records the new VMID. A failure
// deletes whatever partial VM the clone left behind (purge=1).
func (p *proxmoxProvider) cloneFromTemplate(ctx context.Context, templateVMID int, cfg vm.CreateConfig, out io.Writer) (int, error) {
	// CloneVMWithNextID allocates the id and retries a fresh one on the "already
	// exists" collision — the clone-path equivalent of the base create's
	// CreateVMWithNextID. It must live in the client, not here, because the wrong
	// recovery (deleting the id we tried to use) would purge the VM the colliding
	// creator just put in our pool. On a NON-collision synchronous error nothing
	// was created, so there is nothing to clean up either.
	progress(out, "Cloning %s from base template %s (VMID %d)\n", cfg.Name, cfg.BaseName, templateVMID)
	newid, cUPID, err := p.client.CloneVMWithNextID(ctx, templateVMID, pve.CloneVMOptions{
		Name: cfg.Name,
		// Pool on the clone is as non-negotiable as on the base create: without
		// it the clone is not a pool member and every later token-scoped
		// permission check against it fails.
		Pool: p.pool,
		// A full (independent) clone onto the configured storage, so the VM does
		// not depend on the template's disk staying put.
		Full:    true,
		Storage: p.storage,
	})
	if err != nil {
		return 0, fmt.Errorf("proxmox: cloning %s: %w", cfg.Name, err)
	}
	p.setVMID(cfg.Name, newid)
	if err := p.client.WaitTask(ctx, cUPID.Raw); err != nil {
		// The clone TASK started under newid and then failed, so the partial VM
		// under newid is genuinely OURS to purge (unlike a synchronous collision,
		// which never reaches here).
		p.cleanupVM(ctx, newid, cfg.Name, out)
		return 0, fmt.Errorf("proxmox: cloning %s: %w", cfg.Name, err)
	}
	return newid, nil
}

// finalizeClone brings a freshly cloned VM up to a usable state: apply its
// cloud-init identity (and regenerate the drive), grow its disk to the requested
// size, boot it, and run the finalize playbook. It runs OUTSIDE the base lock, so
// concurrent creates overlap here.
func (p *proxmoxProvider) finalizeClone(ctx context.Context, vmid int, cfg vm.CreateConfig, out io.Writer) error {
	if err := p.applyCloudInitIdentity(ctx, vmid, out); err != nil {
		return err
	}
	if err := p.resizeDisk(ctx, vmid, "scsi0", cfg.Disk, out); err != nil {
		return err
	}
	if err := p.start(ctx, cfg.Name, out); err != nil {
		return err
	}
	return p.runPlaybookPhase(ctx, cfg.Name, cfg, "finalize", cfg.EffectiveHostname(), out)
}

// applyCloudInitIdentity (re)asserts the guest login user and DHCP networking on
// the VM's cloud-init config, then REGENERATES the cloud-init image. The
// regenerate is the load-bearing second call: a config write ALONE does not
// rebuild the cloud-init drive (only a start, this call, or hotplug do, and
// cloudinit is not in the default hotplug set), so without it the write would
// never take effect. The SSH key itself is inherited from the template (baked in
// at base-create time, where its double-encoding is handled), so it is not
// rewritten here.
func (p *proxmoxProvider) applyCloudInitIdentity(ctx context.Context, vmid int, out io.Writer) error {
	progress(out, "Applying cloud-init identity to VMID %d\n", vmid)
	form := url.Values{
		"ciuser":    {p.ciUser},
		"ipconfig0": {"ip=dhcp"},
	}
	if err := p.client.SetConfigSync(ctx, vmid, form); err != nil {
		return fmt.Errorf("proxmox: writing cloud-init config for VMID %d: %w", vmid, err)
	}
	if err := p.client.RegenerateCloudInit(ctx, vmid); err != nil {
		return fmt.Errorf("proxmox: regenerating cloud-init for VMID %d: %w", vmid, err)
	}
	return nil
}

// --- guest provisioning over SSH ------------------------------------------------

// baseDepsScript installs just enough in the base guest to run the playbook over
// SSH — the SSH counterpart of Lima's `mode: dependency` overlay script. It is
// the bootstrap, NOT the playbook (which is run separately below so its output
// streams), and it reruns idempotently: the guard checks for every tool it
// installs so a partial earlier run does not leave a later boot missing one.
// --no-install-recommends is load-bearing (Debian's ansible-core Recommends the
// 200MB `ansible` bundle), which is why python3-passlib — needed by the user
// role's password_hash filter — is named explicitly.
const baseDepsScript = `set -eux -o pipefail
if command -v ansible-playbook >/dev/null 2>&1 \
   && command -v rsync >/dev/null 2>&1 \
   && command -v curl >/dev/null 2>&1 \
   && command -v gpg >/dev/null 2>&1 \
   && python3 -c 'import passlib' >/dev/null 2>&1; then
  exit 0
fi
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ansible-core rsync curl gnupg ca-certificates python3-passlib
`

// stagePlaybookScript receives the playbook tarball on stdin and unpacks it to
// /root/playbook (replacing any prior copy so a stale file cannot survive), the
// directory runPlaybookScript then runs ansible-playbook from.
const stagePlaybookScript = `set -eu -o pipefail
rm -rf /root/playbook
mkdir -p /root/playbook
tar -xzf - -C /root/playbook
`

// runPlaybookScript reads the phase extra-vars off stdin into tmpfs (never argv,
// so a finalize token never appears in a process listing or on the persistent
// disk) with a 0600 mode set BEFORE any bytes land and an EXIT-trap removal, then
// runs the playbook with --connection=local. It mirrors the in-guest hygiene of
// internal/provision's own script; the playbook and the extra-vars are reused
// verbatim (LocatePlaybook + BuildExtraVars), only the invocation harness is
// local to this backend.
const runPlaybookScript = `set -eu -o pipefail
vars=/dev/shm/sand-vars.yml
trap 'rm -f "$vars"' EXIT
install -m 600 /dev/null "$vars"
cat > "$vars"
cd /root/playbook
ansible-playbook -i localhost, --connection=local site.yml --extra-vars @"$vars"
`

// installBaseDeps runs the bootstrap dependency install in the base guest.
func (p *proxmoxProvider) installBaseDeps(ctx context.Context, name string, out io.Writer) error {
	progress(out, "Installing provisioning dependencies in %s\n", name)
	if err := p.Shell(ctx, name, nil, out, "sudo", "bash", "-c", baseDepsScript); err != nil {
		return fmt.Errorf("proxmox: installing provisioning dependencies in %s: %w", name, err)
	}
	return nil
}

// runPlaybookPhase stages the playbook onto the guest and runs one Ansible phase
// (base or finalize) against it, streaming output to out. The extra-vars — which
// carry the tool-set selection and, for finalize, the git identity and any
// project-clone token — come from provision.BuildExtraVars, reused unchanged so
// the toolset flags are never reimplemented here.
func (p *proxmoxProvider) runPlaybookPhase(ctx context.Context, name string, cfg vm.CreateConfig, phase, hostname string, out io.Writer) error {
	dir, err := locatePlaybookFn()
	if err != nil {
		return fmt.Errorf("proxmox: locating the playbook: %w", err)
	}
	tarball, err := buildPlaybookTar(dir)
	if err != nil {
		return fmt.Errorf("proxmox: packaging the playbook: %w", err)
	}
	progress(out, "Staging the playbook into %s\n", name)
	if err := p.Shell(ctx, name, tarball, out, "sudo", "bash", "-c", stagePlaybookScript); err != nil {
		return fmt.Errorf("proxmox: staging the playbook into %s: %w", name, err)
	}

	// aptUpgrade is false: the cold base build and the finalize phase never ask
	// for an apt upgrade (only the Lima flow's 30-day in-place refresh does, and
	// there is no in-place refresh for a template).
	vars, err := provision.BuildExtraVars(cfg, phase, hostname, false)
	if err != nil {
		return fmt.Errorf("proxmox: building extra-vars (%s phase): %w", phase, err)
	}
	progress(out, "Provisioning %s (%s phase)\n", name, phase)
	if err := p.Shell(ctx, name, bytes.NewReader(vars), out, "sudo", "bash", "-c", runPlaybookScript); err != nil {
		return fmt.Errorf("proxmox: provisioning %s (%s phase): %w", name, phase, err)
	}
	return nil
}

// buildPlaybookTar packages the playbook fileset from dir into a gzip tar for
// streaming to the guest. The fileset is the CANONICAL one — the top-level
// entries of the embedded FS, i.e. the go:embed directive's own list — so a
// checkout dir contributes only site.yml/ansible.cfg/inventory/roles/group_vars
// and never its .git, Go sources, or agent-tooling symlinks. Non-regular files
// (symlinks) are skipped, so a checkout carrying them cannot break the archive.
func buildPlaybookTar(dir string) (io.Reader, error) {
	roots, err := fs.ReadDir(sandbar.PlaybookFS, ".")
	if err != nil {
		return nil, fmt.Errorf("reading the embedded playbook fileset: %w", err)
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, root := range roots {
		srcRoot := filepath.Join(dir, root.Name())
		if _, err := os.Stat(srcRoot); err != nil {
			continue // this fileset member is absent in dir; skip it
		}
		err := filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if d.IsDir() {
				return tw.WriteHeader(&tar.Header{Name: rel + "/", Mode: 0o755, Typeflag: tar.TypeDir})
			}
			if !info.Mode().IsRegular() {
				return nil // skip symlinks/specials rather than fail on them
			}
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			hdr.Name = rel
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

// --- reset ----------------------------------------------------------------------

// resetInstance recreates a managed VM from a (possibly edited) config, optionally
// preserving the Claude login and/or the per-org project tree across the
// destroy/recreate. The preservation mechanism is guest-side file copying over
// SSH (provision.StageOut/StageIn), which is backend-agnostic and reused
// unchanged — there is no Proxmox-specific mechanism.
//
// Ordering is load-bearing and mirrors the Lima Reset: the Claude restore runs
// BEFORE finalize so the playbook layers settings on top of the restored
// credentials; the project restore runs AFTER finalize, and the finalize pass
// omits the project clone (CloneURL cleared) so the role does not clobber the
// restored tree. Once stage-out has begun, no later error removes the staging
// dir — its path is surfaced in the error so the user can recover the data.
func (p *proxmoxProvider) resetInstance(ctx context.Context, cfg vm.CreateConfig, opts provision.ResetOptions, out io.Writer) error {
	user := cfg.User
	if user == "" {
		user = p.ciUser
	}
	home := "/home/" + user

	var stageDir, orgRel string
	var haveOrg bool

	// 1. Stage out the selected state while the source VM is still alive.
	if opts.PreserveClaude || opts.PreserveProject {
		if _, err := p.Get(cfg.Name); err != nil {
			// Nothing to preserve from a VM that is not there; fall through to a
			// clean recreate rather than fail.
			if !errors.Is(err, lima.ErrNoSuchInstance) {
				return err
			}
		} else {
			if st, _ := p.Status(cfg.Name); st != "Running" {
				progress(out, "Starting %s to stage its data\n", cfg.Name)
				if err := p.start(ctx, cfg.Name, out); err != nil {
					return fmt.Errorf("proxmox: starting %s for staging: %w", cfg.Name, err)
				}
			}
			var err error
			if stageDir, err = os.MkdirTemp("", "sand-reset-*"); err != nil {
				return fmt.Errorf("proxmox: creating a staging directory: %w", err)
			}
			if err := os.Chmod(stageDir, 0o700); err != nil {
				_ = os.RemoveAll(stageDir)
				return fmt.Errorf("proxmox: locking down the staging directory: %w", err)
			}
			if opts.PreserveClaude {
				if err := provision.StageOut(ctx, p, cfg.Name, home, []string{".claude", ".claude.json"}, filepath.Join(stageDir, "claude.tgz")); err != nil {
					return fmt.Errorf("reset failed after staging; your data is preserved at %s: %w", stageDir, err)
				}
			}
			if opts.PreserveProject {
				if orgRel, haveOrg = provision.OrgRelDir(cfg.CloneURL); haveOrg {
					if err := provision.StageOut(ctx, p, cfg.Name, home, []string{orgRel}, filepath.Join(stageDir, "project.tgz")); err != nil {
						return fmt.Errorf("reset failed after staging; your data is preserved at %s: %w", stageDir, err)
					}
				}
			}
		}
	}

	staged := stageDir != ""
	wrap := func(err error) error {
		if err == nil || !staged {
			return err
		}
		return fmt.Errorf("reset failed after staging; your data is preserved at %s: %w", stageDir, err)
	}

	// 2. Delete the existing VM (force), then re-clone from the base.
	if err := p.Delete(cfg.Name, true); err != nil && !errors.Is(err, lima.ErrNoSuchInstance) {
		return wrap(fmt.Errorf("proxmox: deleting %s: %w", cfg.Name, err))
	}

	// A reset never asks for a base rebuild (zero CreateOptions); it takes the
	// template as the lock finds it.
	cloneVMID, err := p.ensureBaseAndClone(ctx, cfg, provision.CreateOptions{}, out)
	if err != nil {
		return wrap(err)
	}

	// 3. Bring the clone up to the point just before finalize, cleaning up the
	// partial VM on any failure here (as provisionClone does for a plain create).
	if err := p.applyCloudInitIdentity(ctx, cloneVMID, out); err != nil {
		p.cleanupVM(ctx, cloneVMID, cfg.Name, out)
		return wrap(err)
	}
	if err := p.resizeDisk(ctx, cloneVMID, "scsi0", cfg.Disk, out); err != nil {
		p.cleanupVM(ctx, cloneVMID, cfg.Name, out)
		return wrap(err)
	}
	if err := p.start(ctx, cfg.Name, out); err != nil {
		p.cleanupVM(ctx, cloneVMID, cfg.Name, out)
		return wrap(err)
	}

	// 4. Restore Claude BEFORE finalize so the playbook layers settings on top.
	if opts.PreserveClaude && staged {
		if err := provision.StageIn(ctx, p, cfg.Name, home, user, []string{".claude", ".claude.json"}, filepath.Join(stageDir, "claude.tgz")); err != nil {
			return wrap(fmt.Errorf("proxmox: restoring Claude into %s: %w", cfg.Name, err))
		}
	}

	// 5. Finalize, omitting the project clone only when a tree was actually staged.
	finCfg := cfg
	if opts.PreserveProject && haveOrg {
		finCfg.CloneURL = ""
	}
	if err := p.runPlaybookPhase(ctx, cfg.Name, finCfg, "finalize", cfg.EffectiveHostname(), out); err != nil {
		return wrap(err)
	}

	// 6. Restore the project tree AFTER finalize, then re-approve its .env.
	if opts.PreserveProject && haveOrg && staged {
		if err := provision.StageIn(ctx, p, cfg.Name, home, user, []string{orgRel}, filepath.Join(stageDir, "project.tgz")); err != nil {
			return wrap(fmt.Errorf("proxmox: restoring the project into %s: %w", cfg.Name, err))
		}
		if err := p.Shell(ctx, cfg.Name, nil, out, "sudo", "-iu", user, "direnv", "allow", home+"/"+orgRel); err != nil {
			return wrap(fmt.Errorf("proxmox: approving the restored .env in %s: %w", cfg.Name, err))
		}
	}

	// 7. Full success: drop the staging dir.
	if staged {
		_ = os.RemoveAll(stageDir)
	}
	progress(out, "%s is ready\n", cfg.Name)
	return nil
}

// --- shared helpers -------------------------------------------------------------

// resizeDisk grows a VM disk to size (a Lima-style size string like "100GiB"),
// waiting on the resulting task. An empty size is a no-op, and PVE treats a
// resize to the current size as a silent success, so this is safely idempotent.
func (p *proxmoxProvider) resizeDisk(ctx context.Context, vmid int, disk, size string, out io.Writer) error {
	if strings.TrimSpace(size) == "" {
		return nil
	}
	bytesN, err := parseSizeToBytes(size)
	if err != nil {
		return fmt.Errorf("proxmox: invalid disk size %q: %w", size, err)
	}
	progress(out, "Resizing %s to %s\n", disk, size)
	upid, err := p.client.ResizeDisk(ctx, vmid, disk, bytesN)
	if err != nil {
		return fmt.Errorf("proxmox: resizing %s of VMID %d: %w", disk, vmid, err)
	}
	// ResizeDisk returns an empty UPID for a no-op (already the target size);
	// only a real task needs waiting on.
	if upid.Raw != "" {
		if err := p.client.WaitTask(ctx, upid.Raw); err != nil {
			return fmt.Errorf("proxmox: resizing %s of VMID %d: %w", disk, vmid, err)
		}
	}
	return nil
}

// destroyVM stops (if needed) and deletes a VM by id, waiting on each task. Used
// for the deliberate rebuild of a stale/`--rebuild` base — a first-class delete
// whose failure aborts the operation, unlike cleanupVM's best-effort tidy.
func (p *proxmoxProvider) destroyVM(ctx context.Context, vmid int, out io.Writer) error {
	st, err := p.client.GetStatus(ctx, vmid)
	if err != nil {
		return err
	}
	if st.Status != pveStopped {
		upid, err := p.client.StopVM(ctx, vmid)
		if err != nil {
			return err
		}
		if err := p.client.WaitTask(ctx, upid.Raw); err != nil {
			return err
		}
	}
	upid, err := p.client.DeleteVM(ctx, vmid, true)
	if err != nil {
		return err
	}
	return p.client.WaitTask(ctx, upid.Raw)
}

// cleanupVM best-effort removes a VM this run created but did not finish creating
// — mirroring internal/provision/cleanup.go's role, adapted to PVE: a partial
// clone occupies a VMID that PVE will hand to someone else, so it must not be
// left behind. It runs on an already-failing path (or a cancelled context), so it
// reports what it did and never replaces the error that brought us here.
func (p *proxmoxProvider) cleanupVM(ctx context.Context, vmid int, name string, out io.Writer) {
	progress(out, "Cleaning up the partial VM %s (VMID %d)\n", name, vmid)
	// Detach from the caller's cancellation (the most common path here is a
	// context the user just cancelled) but keep it bounded.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), powerTimeout)
	defer cancel()

	// A running VM cannot be deleted, so hard-stop first. Best-effort: an already
	// stopped VM makes this error, which is fine to ignore.
	if upid, err := p.client.StopVM(cctx, vmid); err == nil {
		_ = p.client.WaitTask(cctx, upid.Raw)
	}
	if upid, err := p.client.DeleteVM(cctx, vmid, true); err != nil {
		progress(out, "Note: could not delete VMID %d (%v); remove it by hand with `qm destroy %d --purge`\n", vmid, err, vmid)
	} else if err := p.client.WaitTask(cctx, upid.Raw); err != nil {
		progress(out, "Note: could not confirm deletion of VMID %d (%v); remove it by hand with `qm destroy %d --purge`\n", vmid, err, vmid)
	}
	p.forget(name)
	_ = os.RemoveAll(p.instanceDir(name))
}

// acceptedImportExt reports whether file's extension is one PVE's download-url
// accepts for content=import. The check is on the trailing extension only, lower-
// cased, so "Image.QCOW2" passes and "disk.img" does not.
func acceptedImportExt(file string) bool {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(file), "."))
	for _, ok := range acceptedImportExts {
		if ext == ok {
			return true
		}
	}
	return false
}

// memMiB converts a Lima-style memory string ("8GiB") to whole MiB for
// pve.CreateVMOptions, returning 0 (which the create form then omits) for an
// empty or unparseable value.
func memMiB(size string) int {
	b, err := parseSizeToBytes(size)
	if err != nil {
		return 0
	}
	return int(b / (1 << 20))
}

// parseSizeToBytes parses a size string with an optional binary unit suffix
// (KiB/MiB/GiB/TiB, or the shorthand K/M/G/T and KB/MB/GB/TB — all base 1024, to
// match Lima's GiB sizing) into bytes. A bare number is bytes. It exists because
// cfg carries sizes as Lima strings while pve wants integers.
func parseSizeToBytes(size string) (int64, error) {
	s := strings.TrimSpace(size)
	if s == "" {
		return 0, errors.New("empty size")
	}
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	num, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number followed by an optional unit", size)
	}
	var mult float64
	switch strings.ToLower(strings.TrimSpace(s[i:])) {
	case "", "b":
		mult = 1
	case "k", "kb", "kib":
		mult = 1 << 10
	case "m", "mb", "mib":
		mult = 1 << 20
	case "g", "gb", "gib":
		mult = 1 << 30
	case "t", "tb", "tib":
		mult = 1 << 40
	default:
		return 0, fmt.Errorf("unknown size unit in %q", size)
	}
	return int64(num * mult), nil
}

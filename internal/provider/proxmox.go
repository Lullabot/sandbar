package provider

// proxmox.go is the third backend behind the Provider seam, and the first one
// that is not Lima at all. Where the remote provider swaps only WHERE limactl
// runs, this one replaces the whole mechanism: PVE's REST API for the lifecycle,
// direct SSH to the VM for the guest transport, and PVE's own metadata for
// everything Lima kept in an instance directory. Three consequences shape the
// file and are worth reading before changing anything in it:
//
//   - PVE addresses VMs by a numeric VMID; sand addresses them by NAME. There is
//     no lookup-by-name endpoint, so this provider keeps a name->VMID index
//     (populated by List, the call the board already makes every refresh) and
//     verifies it on use. Verification is not defensive tidiness: PVE hands out
//     the LOWEST free VMID, so a deleted VM's id is promptly reused by an
//     unrelated one, and a trusted stale entry would let `delete web` destroy a
//     stranger's VM.
//   - The guest is reached over ssh at an address only the qemu-guest-agent
//     knows. That address is discovered by MATCHING net0's MAC (see guestIP —
//     the single most failure-prone piece here), cached, and dropped on every
//     power transition, which is exactly when a DHCP lease can change hands.
//   - A permission failure is PERMANENT. Every retry loop in this file aborts on
//     one immediately, because the canonical failure in comparable tools was a
//     readiness predicate swallowing a 403 as "not ready yet" and hanging
//     forever instead of reporting a privilege the operator could have granted
//     in one command.
//
// Provisioning (Create/Recreate/Reset) is deliberately absent: those three are
// stubbed at the bottom of this file.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/pve"
	"github.com/lullabot/sandbar/internal/vm"
)

// PVE's own status vocabulary for a qemu guest, distinct from the "Running" /
// "Stopped" strings sand's UI and provisioner branch on (see limaStatus).
const (
	pveRunning = "running"
	pveStopped = "stopped"
)

// The oldest Proxmox VE this provider supports. 9.0 is the floor because the
// minimum-privilege role the setup guide grants is expressed in PVE 9's
// privilege vocabulary: the VM.GuestAgent.* privileges it relies on were
// introduced in PVE 9, and VM.Monitor (which older guides used) was removed in
// PVE 9. On an 8.x host that role cannot even be created, so the token would
// fail later with an opaque 403 rather than anything an operator can act on —
// which is exactly what Preflight exists to prevent by naming the version here.
const (
	minPVEMajor = 9
	minPVEMinor = 0
)

// defaultImageStorage is the storage a Proxmox profile's cloud-image download
// falls back to when image_storage is unset — "local", the directory storage
// PVE creates by default on every node. It is file-based, so it can hold the
// content=import download that block storages (zfspool, lvm-thin) reject. A node
// whose "local" does not enable "import" content, or that wants a different
// file-based storage, sets image_storage explicitly (Preflight checks it).
const defaultImageStorage = "local"

// Timings. All are vars, not consts, so tests can shrink them and keep the suite
// fast rather than waiting on real minutes.
var (
	// agentPollInterval / agentWaitTimeout bound the guest-agent readiness wait:
	// a cloud image answers within a few seconds of boot, and a VM that has not
	// answered in three minutes is not booting.
	agentPollInterval = 2 * time.Second
	agentWaitTimeout  = 3 * time.Minute

	// apiTimeout bounds one API-backed call on a Provider method that takes no
	// context of its own (List, Get, Status, Preflight). Without it those would
	// inherit context.Background and a wedged endpoint would block the caller —
	// for the TUI, a board refresh — indefinitely.
	apiTimeout = 30 * time.Second

	// powerTimeout bounds a whole power operation, which is a PVE task plus (for
	// a start) the readiness wait, so it must comfortably exceed agentWaitTimeout.
	powerTimeout = 15 * time.Minute

	// attachResolveTimeout bounds the address lookup AttachArgv may have to do.
	// It is short on purpose: that method has no error return and is called on
	// the TUI's own update goroutine, so it trades completeness for not freezing
	// the board (the remote provider's AttachArgv makes an ssh round trip on the
	// same goroutine, so a bounded API call here is no new hazard).
	attachResolveTimeout = 10 * time.Second

	// sshWaitDelay reaps the orphaned ssh child of a cancelled guest command, the
	// same hazard and the same treatment as internal/lima's runner.
	sshWaitDelay = 2 * time.Second
)

// proxmoxProvider drives one POOL on one PVE node. The pool is the isolation
// boundary the whole design rests on — the API token is scoped to /pool/{pool},
// so a VM outside it is structurally unreachable rather than merely unlisted —
// and every field below is either that identity or a cache of what PVE told us
// about it.
type proxmoxProvider struct {
	client *pve.Client
	// host/node/pool/storage/bridge mirror the client's own configuration
	// because error messages must name them: a preflight failure that does not
	// say WHICH pool or storage it was looking for is a support ticket.
	host, node, pool, storage, bridge string

	// imageStorage is the FILE-BASED storage the one-time cloud-image download
	// lands on (content=import). It is deliberately SEPARATE from storage: the
	// download-url endpoint only accepts content=import on file-based storages
	// (dir/NFS/CIFS), which block storages like zfspool and lvm-thin reject — yet
	// those block storages are exactly what a VM's disks want. So the image is
	// downloaded here and the disk is imported onto `storage` from it (PVE's
	// import-from allows a source on a different storage than the target disk).
	// Empty in the config defaults to defaultImageStorage; see NewProxmox.
	imageStorage string

	// baseImageURL is the cloud image the base template is built from, and
	// baseImageFile is the filename derived from it (the "<imageStorage>:import/
	// <baseImageFile>" volid the download lands on). Both come from the profile's
	// base_image when set, or the built-in Debian default otherwise — see
	// NewProxmox. Kept as fields (not the package vars) so one endpoint's custom
	// image cannot leak into another's build.
	baseImageURL, baseImageFile string

	// ciUser is the guest login user: the cloud-init ciuser this provider
	// configures at create time, and therefore the account every ssh, every scp,
	// and every guest home resolves against. It is the answer to HostUser (see
	// that method) and MUST stay the single source for both, or a VM would be
	// provisioned for one account and shelled into as another.
	ciUser string
	// identityPath is the private key file matching the public key cloud-init
	// installs for ciUser. Never key material.
	identityPath string

	// files is the local, per-endpoint state directory standing in for "the host
	// where limactl runs" (proxmoxfiles.go).
	files proxmoxFiles

	// runSSH executes a fully-built ssh/scp argv. It is a field, not a direct
	// exec call, so a test can assert the exact argv without a real ssh binary or
	// a real VM — the same rule internal/lima keeps for limactl.
	runSSH func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) error

	// mu guards both caches below. They are read on the TUI's refresh path and
	// written by every lifecycle call, so a plain mutex is the honest tool.
	mu sync.Mutex
	// vmids is the name->VMID index (see the file header for why it exists and
	// why every use of it is verified).
	vmids map[string]int
	// ips is name->reachable guest address, invalidated on every power
	// transition.
	ips map[string]string
}

var _ Provider = (*proxmoxProvider)(nil)

// NewProxmox builds the Proxmox provider for cfg. It performs NO network round
// trip: BuildFleet constructs one provider per enabled profile on the TUI's
// startup path, so a handshake here would make launching sand as slow as the
// least reachable endpoint. Everything that needs the API to answer lives in
// Preflight.
//
// Reading the token file is the one thing construction does touch, and
// deliberately: a missing or world-readable token file becomes a clear error
// binding at build time (profiles.LoadToken refuses one readable by group or
// other) instead of a confusing failure on first use. The token stops here — it
// is never stored on TargetConfig, whose fields Scope() folds into an identity
// the registry persists.
func NewProxmox(cfg TargetConfig) (Provider, error) {
	switch {
	case cfg.Host == "":
		return nil, errors.New("proxmox: profile has no host (the address the PVE API answers on)")
	case cfg.Node == "":
		return nil, errors.New("proxmox: profile has no node (PVE's own name for the node, e.g. \"pve\" — not the hostname you connect to)")
	case cfg.Pool == "":
		return nil, errors.New("proxmox: profile has no pool (the resource pool that scopes both the token and every VM sand creates)")
	case cfg.TokenFile == "":
		return nil, errors.New("proxmox: profile has no token_file (a path to a 0600 file holding the API token; the token itself is never stored in the profile)")
	}

	token, err := profiles.LoadToken(cfg.TokenFile)
	if err != nil {
		return nil, err // already phrased in terms of the token file
	}

	client, err := pve.New(pve.Config{
		Host:               cfg.Host,
		Node:               cfg.Node,
		TokenID:            token,
		InsecureSkipVerify: cfg.Insecure,
		CAFile:             cfg.CAFile,
	})
	if err != nil {
		return nil, err
	}

	// The guest login user defaults to this machine's user, matching what the
	// other providers do — but for a different and better reason: here sand
	// CREATES the account (cloud-init ciuser), so the default is a choice rather
	// than an observation of someone else's machine. See HostUser.
	ciUser := cfg.User
	if ciUser == "" {
		ciUser = vm.HostUser()
	}

	// The image-download storage is separate from the VM-disk storage (see the
	// imageStorage field's doc). An unset image_storage falls back to
	// defaultImageStorage — "local", the dir storage present on essentially every
	// PVE node — so a block-storage disk target (the common case) still has a
	// file-based place to stage the cloud-image import without extra config.
	imageStorage := cfg.ImageStorage
	if imageStorage == "" {
		imageStorage = defaultImageStorage
	}

	// Expand a leading ~ in identity_path exactly as LoadToken does for the token
	// file. This one value is used three ways — read as "<path>.pub" for cloud-init
	// and passed as ssh -i for the transport — none of which goes through a shell,
	// so a literal "~/.ssh/id_ed25519" would never resolve. Empty stays empty;
	// Preflight and readPublicKey report a missing key.
	identityPath, err := profiles.ExpandHome(cfg.IdentityPath)
	if err != nil {
		return nil, fmt.Errorf("proxmox: identity_path: %w", err)
	}

	// base_image lets a profile point at a golden image (e.g. one with
	// qemu-guest-agent baked in); "" uses the built-in Debian default. The
	// filename PVE stores the import under is derived from the URL, stripping any
	// query/fragment so "…/img.qcow2?sig=…" still names "img.qcow2".
	baseURL, baseFile := baseImageURL, baseImageFile
	if cfg.BaseImage != "" {
		baseURL = cfg.BaseImage
		name := baseURL
		if i := strings.IndexAny(name, "?#"); i >= 0 {
			name = name[:i]
		}
		baseFile = path.Base(name)
	}

	return &proxmoxProvider{
		client:        client,
		host:          cfg.Host,
		node:          cfg.Node,
		pool:          cfg.Pool,
		storage:       cfg.Storage,
		imageStorage:  imageStorage,
		baseImageURL:  baseURL,
		baseImageFile: baseFile,
		bridge:        cfg.Bridge,
		ciUser:        ciUser,
		identityPath:  identityPath,
		files:         newProxmoxFiles(proxmoxStateRoot(cfg)),
		runSSH:        execSSH,
		vmids:         map[string]int{},
		ips:           map[string]string{},
	}, nil
}

// --- name/VMID index and address cache ------------------------------------------

func (p *proxmoxProvider) cachedVMID(name string) (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	id, ok := p.vmids[name]
	return id, ok
}

func (p *proxmoxProvider) setVMID(name string, vmid int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.vmids[name] = vmid
}

func (p *proxmoxProvider) cachedGuestIP(name string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ip, ok := p.ips[name]
	return ip, ok
}

func (p *proxmoxProvider) setGuestIP(name, ip string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ips[name] = ip
}

// invalidateGuest drops a cached address. Every power transition calls it: a VM
// that stops and starts can come back on a different DHCP lease, and a cached
// address that outlives the boot sends every guest command to whoever holds that
// lease now.
func (p *proxmoxProvider) invalidateGuest(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.ips, name)
}

// forget drops everything cached about a name, for use after a delete: the VMID
// is immediately available for PVE to hand to someone else's VM.
func (p *proxmoxProvider) forget(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.vmids, name)
	delete(p.ips, name)
}

// setIndex replaces the whole name->VMID index from a fresh listing, and drops
// cached addresses for names the listing no longer contains. Replacing rather
// than merging is what keeps a deleted VM from lingering in the index.
func (p *proxmoxProvider) setIndex(index map[string]int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.vmids = index
	for name := range p.ips {
		if _, ok := index[name]; !ok {
			delete(p.ips, name)
		}
	}
}

// --- discovery ------------------------------------------------------------------

// List returns every VM in the configured pool that this provider can actually
// act on. Two filters beyond the pool are applied, both deliberate:
//
//   - qemu only, which pve.ListVMs already does (the same query also returns LXC
//     containers, which sand does not manage).
//   - THIS node only. The client is bound to one node (every per-VM endpoint is
//     /nodes/{node}/qemu/{vmid}/…), so a pool member living on another node
//     would produce a tile whose every verb 404s. Hiding it is the lesser
//     surprise, and it re-appears the moment it is migrated back.
//
// A nameless VM is skipped for the same reason: sand's whole surface is
// name-keyed, and an unnamed tile has no verb that could address it.
func (p *proxmoxProvider) List() ([]vm.VM, error) {
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()

	resources, err := p.client.ListVMs(ctx, p.pool)
	if err != nil {
		return nil, fmt.Errorf("proxmox: listing pool %q on node %s: %w", p.pool, p.node, err)
	}

	index := p.indexOf(resources)
	p.setIndex(index)

	// Fetched ONCE for the whole listing (see diskUsedByVMID) rather than once
	// per VM, so a fleet listing costs one extra round trip total, not one per
	// tile.
	diskUsed := p.diskUsedIndex(ctx)

	out := make([]vm.VM, 0, len(index))
	for _, r := range resources {
		// Emit exactly the resource the index chose for each name, so a listing
		// and a later Get can never disagree about which VM a name means.
		if id, ok := index[r.Name]; !ok || id != r.VMID {
			continue
		}
		out = append(out, p.resourceVM(r, diskUsed))
	}
	return out, nil
}

// indexOf builds the name->VMID index from a listing. PVE permits two VMs to
// share a name; the LOWEST id wins, so repeated calls resolve an ambiguous name
// the same way every time rather than flipping between two VMs depending on the
// order the API happened to return them in.
func (p *proxmoxProvider) indexOf(resources []pve.VMResource) map[string]int {
	index := make(map[string]int, len(resources))
	for _, r := range resources {
		if r.Name == "" || r.Node != p.node {
			continue
		}
		if cur, ok := index[r.Name]; !ok || r.VMID < cur {
			index[r.Name] = r.VMID
		}
	}
	return index
}

// Get looks up ONE instance, and is emphatically not a scan of List: it resolves
// the name to a VMID and then reads that VM's own status endpoint. The interface
// documents why scanning is wrong, and the reasoning survives the change of
// backend — a listing describes the whole pool at one instant, while a caller
// asking about one VM wants that VM's live state, not a snapshot that another
// VM's in-flight clone can perturb.
//
// The name->VMID index is the one place a listing is legitimately involved,
// because PVE offers no lookup-by-name endpoint at all. It is consulted from
// cache, refreshed only when the cache cannot answer, and always verified
// against the VM's own reported name (see resolve).
func (p *proxmoxProvider) Get(name string) (vm.VM, error) {
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()

	_, st, err := p.resolve(ctx, name)
	if err != nil {
		return vm.VM{}, err
	}
	return p.statusVM(name, st, p.diskUsedIndex(ctx)), nil
}

// Status reports one instance's status in the vocabulary the UI and provisioner
// branch on.
func (p *proxmoxProvider) Status(name string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()

	_, st, err := p.resolve(ctx, name)
	if err != nil {
		return "", err
	}
	return limaStatus(st.Status), nil
}

// resolve maps a name to its VMID and that VM's current status, returning
// lima.ErrNoSuchInstance when the pool holds no such VM — the sentinel every
// consumer already branches on, reused here on purpose: it is the interface's
// shared vocabulary, not a Lima implementation detail.
//
// The cached id is VERIFIED against the name the VM reports before it is
// returned. That check is the whole reason caching an id is safe: PVE allocates
// the lowest free VMID, so an id freed by a delete is reused within minutes, and
// an unverified cache would eventually address someone else's VM with power and
// delete calls. A mismatch (or a 404) drops the entry and re-resolves once from
// a fresh listing.
func (p *proxmoxProvider) resolve(ctx context.Context, name string) (int, pve.VMStatus, error) {
	if vmid, ok := p.cachedVMID(name); ok {
		st, err := p.client.GetStatus(ctx, vmid)
		switch {
		case err == nil && st.Name == name:
			return vmid, st, nil
		case err == nil, pve.IsNotFound(err):
			// The id no longer means this name: recycled, renamed, or deleted.
			p.forget(name)
		default:
			// Anything else (notably a 403) is the caller's to see verbatim; a
			// permission problem must never be reported as a missing VM.
			return 0, pve.VMStatus{}, fmt.Errorf("proxmox: reading %s (VMID %d): %w", name, vmid, err)
		}
	}

	vmid, err := p.lookupVMID(ctx, name)
	if err != nil {
		return 0, pve.VMStatus{}, err
	}
	st, err := p.client.GetStatus(ctx, vmid)
	if err != nil {
		if pve.IsNotFound(err) {
			return 0, pve.VMStatus{}, fmt.Errorf("%w: %s", lima.ErrNoSuchInstance, name)
		}
		return 0, pve.VMStatus{}, fmt.Errorf("proxmox: reading %s (VMID %d): %w", name, vmid, err)
	}
	if st.Name != name {
		// The listing raced a rename or a delete-and-recreate. Report absence
		// rather than acting on a VM the caller did not ask for.
		return 0, pve.VMStatus{}, fmt.Errorf("%w: %s", lima.ErrNoSuchInstance, name)
	}
	p.setVMID(name, vmid)
	return vmid, st, nil
}

// lookupVMID refreshes the index from the pool listing and returns name's id.
func (p *proxmoxProvider) lookupVMID(ctx context.Context, name string) (int, error) {
	resources, err := p.client.ListVMs(ctx, p.pool)
	if err != nil {
		return 0, fmt.Errorf("proxmox: looking up %q in pool %q: %w", name, p.pool, err)
	}
	index := p.indexOf(resources)
	p.setIndex(index)
	vmid, ok := index[name]
	if !ok {
		return 0, fmt.Errorf("%w: %s", lima.ErrNoSuchInstance, name)
	}
	return vmid, nil
}

// resourceVM converts a cluster-listing entry into sand's VM record. diskUsed
// is this call's storage-content index (see diskUsedIndex); a VM absent from
// it (the fetch failed, or it simply owns no volume yet) gets DiskUsed=="",
// the same "unknown" byteString already uses for Memory/Disk.
func (p *proxmoxProvider) resourceVM(r pve.VMResource, diskUsed map[int]int64) vm.VM {
	return vm.VM{
		Name:     r.Name,
		Status:   limaStatus(r.Status),
		CPUs:     int(r.CPUs),
		Memory:   byteString(r.MaxMem),
		Disk:     byteString(r.MaxDisk),
		DiskUsed: byteString(diskUsed[r.VMID]),
		Dir:      p.instanceDir(r.Name),
	}
}

// statusVM converts a single-VM status reading into sand's VM record.
// diskUsed is this call's storage-content index (see diskUsedIndex).
func (p *proxmoxProvider) statusVM(name string, st pve.VMStatus, diskUsed map[int]int64) vm.VM {
	return vm.VM{
		Name:     name,
		Status:   limaStatus(st.Status),
		CPUs:     int(st.CPUs),
		Memory:   byteString(st.MaxMem),
		Disk:     byteString(st.MaxDisk),
		DiskUsed: byteString(diskUsed[st.VMID]),
		Dir:      p.instanceDir(name),
	}
}

// diskUsedIndex fetches this provider's configured storage's content listing
// ONCE and reduces it to each owning VM's disk usage (diskUsedByVMID). PVE
// hardcodes a running QEMU guest's own `disk` field to 0 in status/current —
// upstream literally writes `$d->{disk} = 0; # no info available` — and
// `maxdisk` is the boot disk's configured size, not actual allocation, so this
// listing is the only honest source. A failed fetch (storage down, a token
// without Datastore.Audit) degrades to a nil map — every VM's DiskUsed falls
// back to byteString's "" ("unknown") — rather than failing the whole List/Get
// over a reading the UI already treats as optional.
func (p *proxmoxProvider) diskUsedIndex(ctx context.Context) map[int]int64 {
	if p.storage == "" {
		return nil
	}
	items, err := p.client.StorageContent(ctx, p.storage)
	if err != nil {
		return nil
	}
	return diskUsedByVMID(items)
}

// diskUsedByVMID sums each owning VMID's "images"-content volumes (the boot
// disk and, where present, the cloud-init drive — both classified "images" by
// PVE) into a per-VM total allocated size. ISO/vztmpl/backup content and any
// volume with no owning VMID (a shared, unattached resource) are excluded.
//
// Indexed by owning VMID — which StorageContentItem reports directly — rather
// than by matching each volume's volid string: the volid's own shape
// ("vm-<vmid>-disk-N" for LVM-thin, "<vmid>/vm-<vmid>-disk-N.qcow2" for a
// dir-backed store, …) varies by storage plugin, so parsing it would be
// strictly more fragile than the field PVE already hands over.
func diskUsedByVMID(items []pve.StorageContentItem) map[int]int64 {
	out := make(map[int]int64, len(items))
	for _, it := range items {
		if it.VMID == 0 || it.Content != "images" {
			continue
		}
		out[it.VMID] += it.Size
	}
	return out
}

// instanceDir is the per-VM state directory, and it is the whole of what
// vm.VM.Dir means for this backend: a provider-OPAQUE handle, never a path a
// consumer may build on. A Proxmox VM keeps nothing on this machine, so the
// directory holds only sand's own state about it — which is precisely what every
// consumer of Dir (the disk sampler, the up-since probe) is reaching for anyway.
// The VMID lives in the index, not here, so nothing outside this file can grow a
// dependency on PVE's addressing.
func (p *proxmoxProvider) instanceDir(name string) string {
	return filepath.Join(p.files.LimaHome(), name)
}

// limaStatus maps PVE's status vocabulary onto the exact strings sand already
// branches on — the UI's status bands and the provisioner's guards compare
// against "Running" and "Stopped" literally, so a lower-case passthrough would
// silently colour every tile as unknown. Anything PVE reports that is not one of
// the two is capitalised and passed through rather than forced into one of them:
// an unfamiliar state should read as unfamiliar, not as a comfortable lie.
func limaStatus(status string) string {
	switch status {
	case pveRunning:
		return "Running"
	case pveStopped:
		return "Stopped"
	case "":
		return ""
	default:
		return strings.ToUpper(status[:1]) + status[1:]
	}
}

// byteString renders a byte count the way vm.VM.Memory and vm.VM.Disk carry it —
// a decimal string the UI parses and humanizes. Zero becomes "" (unknown) rather
// than "0", so a figure PVE did not report renders as an absence instead of as a
// VM with no memory.
func byteString(n int64) string {
	if n <= 0 {
		return ""
	}
	return strconv.FormatInt(n, 10)
}

// --- power ----------------------------------------------------------------------

func (p *proxmoxProvider) Start(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), powerTimeout)
	defer cancel()
	return p.start(ctx, name, nil)
}

func (p *proxmoxProvider) StartStreaming(ctx context.Context, name string, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, powerTimeout)
	defer cancel()
	return p.start(ctx, name, out)
}

func (p *proxmoxProvider) Stop(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), powerTimeout)
	defer cancel()
	return p.stop(ctx, name, nil)
}

func (p *proxmoxProvider) StopStreaming(ctx context.Context, name string, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, powerTimeout)
	defer cancel()
	return p.stop(ctx, name, out)
}

// start boots a VM and does not return until sand can actually USE it: the PVE
// task has finished, the guest agent answers, and net0 has an address. That is
// the same contract `limactl start` keeps (it returns when the instance is
// ssh-ready), and it is what lets a caller shell in on the next line. Returning
// at "the task finished" would hand back a VM whose address is not yet knowable.
//
// The buffered and streaming forms share this one implementation so the two can
// never drift; out is nil for the buffered form.
func (p *proxmoxProvider) start(ctx context.Context, name string, out io.Writer) error {
	vmid, st, err := p.resolve(ctx, name)
	if err != nil {
		return err
	}
	// Drop any cached address BEFORE the boot, not after: the VM may come back
	// on a different lease, and a reader racing the boot must not see the old one.
	p.invalidateGuest(name)

	if st.Status != pveRunning {
		progress(out, "Starting %s (VMID %d) on node %s\n", name, vmid, p.node)
		upid, err := p.client.StartVM(ctx, vmid)
		if err != nil {
			return fmt.Errorf("proxmox: starting %s: %w", name, err)
		}
		if err := p.client.WaitTask(ctx, upid.Raw); err != nil {
			return fmt.Errorf("proxmox: starting %s: %w", name, err)
		}
	} else {
		// Already running: PVE rejects a start against a running VM, and
		// `limactl start` on a running instance is a no-op, so confirm
		// readiness instead of issuing a call that can only fail.
		progress(out, "%s is already running\n", name)
	}

	progress(out, "Waiting for %s's guest agent\n", name)
	if err := p.waitAgent(ctx, vmid, name, out); err != nil {
		return err
	}
	progress(out, "Resolving %s's address\n", name)
	ip, err := p.waitGuestIP(ctx, vmid, name)
	if err != nil {
		return err
	}
	p.setGuestIP(name, ip)
	progress(out, "%s is up at %s\n", name, ip)
	return nil
}

// stop shuts a VM down gracefully — PVE's "shutdown", which asks the guest via
// ACPI/the agent, rather than "stop", which is the equivalent of pulling the
// power. Delete's force path is the one place the hard stop is used, because
// there the disk is about to be destroyed anyway.
func (p *proxmoxProvider) stop(ctx context.Context, name string, out io.Writer) error {
	vmid, st, err := p.resolve(ctx, name)
	if err != nil {
		return err
	}
	p.invalidateGuest(name)

	if st.Status == pveStopped {
		progress(out, "%s is already stopped\n", name)
		return nil
	}
	progress(out, "Shutting down %s (VMID %d)\n", name, vmid)
	upid, err := p.client.ShutdownVM(ctx, vmid)
	if err != nil {
		return fmt.Errorf("proxmox: shutting down %s: %w", name, err)
	}
	if err := p.client.WaitTask(ctx, upid.Raw); err != nil {
		return fmt.Errorf("proxmox: shutting down %s: %w", name, err)
	}
	progress(out, "%s is stopped\n", name)
	return nil
}

// Delete destroys a VM and everything attached to it (purge=1 also removes it
// from backup jobs and HA resources, so no orphaned reference is left behind
// pointing at a VMID PVE is about to reuse).
//
// A running VM is refused unless force is set, mirroring limactl. With force it
// is hard-stopped first: PVE will not delete a running VM, and a graceful
// shutdown that the guest ignores would leave the caller waiting on a VM it has
// already asked to destroy.
func (p *proxmoxProvider) Delete(name string, force bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), powerTimeout)
	defer cancel()

	vmid, st, err := p.resolve(ctx, name)
	if err != nil {
		return err
	}
	if st.Status != pveStopped {
		if !force {
			return fmt.Errorf("proxmox: %s is %s; stop it before deleting it", name, limaStatus(st.Status))
		}
		upid, err := p.client.StopVM(ctx, vmid)
		if err != nil {
			return fmt.Errorf("proxmox: stopping %s before delete: %w", name, err)
		}
		if err := p.client.WaitTask(ctx, upid.Raw); err != nil {
			return fmt.Errorf("proxmox: stopping %s before delete: %w", name, err)
		}
	}

	upid, err := p.client.DeleteVM(ctx, vmid, true)
	if err != nil {
		return fmt.Errorf("proxmox: deleting %s: %w", name, err)
	}
	if err := p.client.WaitTask(ctx, upid.Raw); err != nil {
		return fmt.Errorf("proxmox: deleting %s: %w", name, err)
	}

	p.forget(name)
	// Best-effort: the local state directory describes a VM that no longer
	// exists. Failing the delete over it would be reporting failure for an
	// operation that succeeded on the only host that matters.
	_ = os.RemoveAll(p.instanceDir(name))
	return nil
}

// --- readiness ------------------------------------------------------------------

// permanentError marks a failure a poll loop must not retry. It exists because
// the two loops below face errors that look transient (an HTTP 500, a refusal)
// but never resolve: a 403 will still be a 403 in three minutes, and a VM with
// no guest agent configured will never grow one.
type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

func permanentFailure(err error) error { return permanentError{err: err} }

func isPermanentFailure(err error) bool {
	var pe permanentError
	return errors.As(err, &pe)
}

// pollUntil calls fn until it succeeds, the deadline passes, or fn reports a
// permanent failure — in which case it returns IMMEDIATELY. On timeout it
// returns fn's last error, so the report says what it was still waiting for
// rather than "timed out".
func pollUntil(ctx context.Context, timeout time.Duration, fn func(context.Context) error) error {
	deadline := time.Now().Add(timeout)
	for {
		err := fn(ctx)
		if err == nil {
			return nil
		}
		if isPermanentFailure(err) {
			return err
		}
		if !time.Now().Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(agentPollInterval):
		}
	}
}

// waitAgent blocks until vmid's qemu-guest-agent answers a ping.
//
// The classification is the whole point, and one arm of it is the reason this
// function is not three lines of retry:
//
//   - A PERMISSION error (401/403) aborts immediately. This is the canonical
//     failure mode in comparable tools — the readiness predicate treated a 403 as
//     "not ready yet" and the operation hung until the user killed it, instead of
//     naming a privilege that takes one command to grant. PVE puts a bare 403's
//     only detail in the HTTP reason phrase, which pve.APIError carries, so the
//     wrapped error still says "Forbidden".
//   - "not-configured" aborts immediately: a VM with no agent will never answer,
//     and sand cannot discover its address without one.
//   - "vm-stopped", "agent-down" and "timeout" are exactly what a VM that is
//     still booting reports, and are retried.
func (p *proxmoxProvider) waitAgent(ctx context.Context, vmid int, name string, out io.Writer) error {
	return pollUntil(ctx, agentWaitTimeout, func(ctx context.Context) error {
		err := p.client.AgentPing(ctx, vmid)
		if err == nil {
			return nil
		}
		if pve.IsPermission(err) {
			return permanentFailure(fmt.Errorf("proxmox: %s: the API token does not have permission to reach the guest agent — grant it VM.GuestAgent.Audit on /pool/%s: %w", name, p.pool, err))
		}
		if pve.AgentUnavailableReason(err) == "not-configured" {
			return permanentFailure(fmt.Errorf("proxmox: %s has no qemu guest agent configured, so its address can never be discovered — set agent=1 on the VM and install qemu-guest-agent in the image: %w", name, err))
		}
		return fmt.Errorf("proxmox: waiting for %s's guest agent: %w", name, err)
	})
}

// waitGuestIP blocks until net0 has a routable address. It is a separate wait
// from waitAgent on purpose: the agent answers as soon as it starts, which is
// routinely BEFORE DHCP has handed the guest a lease, so treating a ping as
// "ready" would return a VM whose address is still unknowable a moment later.
// A permission failure aborts immediately here for the same reason it does there.
func (p *proxmoxProvider) waitGuestIP(ctx context.Context, vmid int, name string) (string, error) {
	var ip string
	err := pollUntil(ctx, agentWaitTimeout, func(ctx context.Context) error {
		var err error
		ip, err = p.resolveGuestIP(ctx, vmid, name)
		if err != nil && pve.IsPermission(err) {
			return permanentFailure(err)
		}
		return err
	})
	return ip, err
}

// --- guest address discovery ----------------------------------------------------

// guestIP resolves name's reachable address, from cache when possible.
func (p *proxmoxProvider) guestIP(ctx context.Context, name string) (string, error) {
	if ip, ok := p.cachedGuestIP(name); ok {
		return ip, nil
	}
	vmid, _, err := p.resolve(ctx, name)
	if err != nil {
		return "", err
	}
	ip, err := p.resolveGuestIP(ctx, vmid, name)
	if err != nil {
		return "", err
	}
	p.setGuestIP(name, ip)
	return ip, nil
}

// resolveGuestIP asks the guest agent for the VM's address, matching the
// interface by its MAC against net0 rather than by name. Every clause of that
// sentence is load-bearing, and each one is a way a name-based or first-match
// implementation silently picks the wrong address:
//
//   - Interface naming is not stable across images (eth0, ens18, enp6s18), so
//     there is no name to match on.
//   - `lo` is ALWAYS present and always carries an address.
//   - IPv6 link-local (fe80::/10) is always present on an up interface and is
//     never routable from the machine running sand.
//   - An interface that is up but unaddressed omits the ip-addresses key
//     ENTIRELY rather than returning an empty array, so a nil slice is normal
//     input, not a malformed response.
//   - A VM may legitimately have more interfaces than net0 (a second NIC, a
//     docker bridge, a VPN), each with a perfectly plausible global address that
//     sand cannot reach.
//
// The MAC comes from the VM's own config, and the comparison is
// case-insensitive: PVE renders it upper case in net0 while the guest agent
// reports it lower case.
func (p *proxmoxProvider) resolveGuestIP(ctx context.Context, vmid int, name string) (string, error) {
	cfg, err := p.client.GetConfig(ctx, vmid)
	if err != nil {
		return "", fmt.Errorf("proxmox: reading %s's network configuration: %w", name, err)
	}
	net0, _ := cfg["net0"].(string)
	mac := net0MAC(net0)
	if mac == "" {
		return "", fmt.Errorf("proxmox: %s has no MAC address on net0 (net0=%q), so its guest interface cannot be identified", name, net0)
	}

	ifaces, err := p.client.AgentNetworkGetInterfaces(ctx, vmid)
	if err != nil {
		return "", fmt.Errorf("proxmox: asking %s's guest agent for its network interfaces: %w", name, err)
	}
	for _, ifc := range ifaces {
		if !strings.EqualFold(ifc.HardwareAddress, mac) {
			continue
		}
		if ip := firstRoutable(ifc.IPAddresses); ip != "" {
			return ip, nil
		}
		return "", fmt.Errorf("proxmox: %s's net0 interface (%s) has no routable address yet", name, mac)
	}
	return "", fmt.Errorf("proxmox: %s's guest agent reports no interface with net0's MAC (%s)", name, mac)
}

// isMACAddress reports whether s is a colon-separated 48-bit MAC
// ("BC:24:11:AA:BB:CC"). Hand-rolled rather than a regexp because it runs on
// every field of every net0 parse and the shape is fixed: six hex pairs, five
// colons, seventeen characters.
func isMACAddress(s string) bool {
	if len(s) != 17 {
		return false
	}
	for i, r := range s {
		if i%3 == 2 {
			if r != ':' {
				return false
			}
			continue
		}
		if !isHexDigit(r) {
			return false
		}
	}
	return true
}

func isHexDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// net0MAC extracts the MAC from a PVE net0 config value, lower-cased for
// comparison. The value is a comma-separated key=value list whose FIRST key is
// usually the NIC model — "virtio=BC:24:11:AA:BB:CC,bridge=vmbr0" — but may
// instead spell the model separately and carry "macaddr=…". Rather than
// enumerate every model PVE supports (and silently return nothing the day it
// adds one), any value shaped like a MAC is taken as the MAC.
func net0MAC(net0 string) string {
	for _, field := range strings.Split(net0, ",") {
		_, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		if isMACAddress(value) {
			return strings.ToLower(value)
		}
	}
	return ""
}

// firstRoutable picks the address sand can actually reach from a guest
// interface's list, preferring IPv4.
//
// The filter is netip's own classification rather than a string prefix test:
// IsGlobalUnicast excludes loopback, multicast and the unspecified address,
// while IsLinkLocalUnicast excludes both fe80::/10 and 169.254.0.0/16 (the
// address a guest gives itself when DHCP has NOT answered — reachable-looking
// and useless). IPv4 is preferred because that is what a typical VM subnet
// routes; an IPv6-only guest still works via the fallback.
func firstRoutable(addrs []pve.IPAddress) string {
	var v6 string
	for _, a := range addrs {
		ip, err := netip.ParseAddr(a.IPAddress)
		if err != nil {
			continue
		}
		ip = ip.Unmap() // a 4-in-6 address is an IPv4 address
		if !ip.IsGlobalUnicast() || ip.IsLinkLocalUnicast() {
			continue
		}
		if ip.Is4() {
			return ip.String()
		}
		if v6 == "" {
			v6 = ip.String()
		}
	}
	return v6
}

// --- guest transport ------------------------------------------------------------

// sshHost builds the ssh connection identity for one guest address. It is
// constructed per call rather than cached because the host varies per VM (and
// can change across a boot); that costs nothing meaningful, and OpenSSH
// connection multiplexing still shares one authenticated channel per target
// because the control socket is keyed by ssh's own hash of the target.
//
// The port is deliberately not configurable: the guest's sshd is the one
// cloud-init provisions, on 22. TargetConfig.Port belongs to the remote-Lima
// provider's hop, which has no counterpart here.
func (p *proxmoxProvider) sshHost(host string) *lima.SSHHost {
	return lima.NewSSHHost(lima.SSHConfig{
		Host:         host,
		User:         p.ciUser,
		IdentityPath: p.identityPath,
	})
}

// guestArgv builds the ssh argv that runs argv inside name's guest.
func (p *proxmoxProvider) guestArgv(ctx context.Context, name string, tty bool, argv ...string) ([]string, error) {
	ip, err := p.guestIP(ctx, name)
	if err != nil {
		return nil, err
	}
	return p.sshHost(ip).SSHArgv(tty, argv...), nil
}

// Shell runs argv in the guest with stdout and stderr MERGED into out for live
// display.
//
// No PTY is requested even when argv is empty: out is a pipe here, not a
// terminal, and a remote PTY would fold stderr into stdout at the kernel and
// translate newlines into CRLF, corrupting exactly the output the caller is
// reading. The one path that genuinely needs a terminal is the interactive
// attach, which the caller execs against its own TTY (see AttachArgv).
func (p *proxmoxProvider) Shell(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error {
	full, err := p.guestArgv(ctx, name, false, argv...)
	if err != nil {
		return err
	}
	return p.runSSH(ctx, full, stdin, out, out)
}

// ShellStreamOut streams the guest command's stdout ONLY to out, keeping stderr
// out of the payload (it is folded into the error) so a binary stream — a
// `tar -czf -` piped into an archive — cannot be corrupted by a warning.
func (p *proxmoxProvider) ShellStreamOut(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error {
	full, err := p.guestArgv(ctx, name, false, argv...)
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	if err := p.runSSH(ctx, full, stdin, out, &stderr); err != nil {
		return foldStderr(err, stderr.Bytes())
	}
	return nil
}

// ShellOut returns the guest command's stdout, with stderr kept separate for the
// same reason ShellStreamOut does: every caller of this parses what it gets back.
func (p *proxmoxProvider) ShellOut(ctx context.Context, name string, argv ...string) ([]byte, error) {
	full, err := p.guestArgv(ctx, name, false, argv...)
	if err != nil {
		return nil, err
	}
	var stdout, stderr bytes.Buffer
	if err := p.runSSH(ctx, full, nil, &stdout, &stderr); err != nil {
		return stdout.Bytes(), foldStderr(err, stderr.Bytes())
	}
	return stdout.Bytes(), nil
}

// Copy transfers between the host and a guest with scp. Either endpoint may be
// one this provider's GuestPath produced, in either of its two forms (see
// GuestPath), and a plain host path passes through untouched.
func (p *proxmoxProvider) Copy(ctx context.Context, out io.Writer, recursive bool, src, dst string) error {
	src, srcIP, err := p.resolveEndpoint(ctx, src)
	if err != nil {
		return err
	}
	dst, dstIP, err := p.resolveEndpoint(ctx, dst)
	if err != nil {
		return err
	}
	guest := srcIP
	if guest == "" {
		guest = dstIP
	}
	// scp's endpoints carry their own targets, so this connection identity
	// contributes only the flags (identity file, multiplexing) — which is why an
	// already-resolved endpoint, whose address this provider never learned, is
	// still copied correctly.
	argv := p.sshHost(guest).SCPArgv(recursive, src, dst)
	return p.runSSH(ctx, argv, nil, out, out)
}

// GuestPath forms a transport endpoint for Copy. For an ssh transport that is
// scp's own `user@host:path`, which is what it returns whenever the address is
// already known.
//
// When it is not known, it returns the DEFERRED `<name>:<path>` form for Copy to
// resolve, rather than looking the address up here. This method has no error
// return and no context, and it is called on the TUI's update goroutine — an API
// round trip in it would freeze the board, and a failure would have nowhere to
// go but into a fabricated endpoint. Copy has both a context and an error, so
// that is where the resolution belongs. The deferred form is also what the user
// sees in the transfer log, and `web:/home/dev` reads exactly as it does today
// for local Lima.
func (p *proxmoxProvider) GuestPath(name, path string) string {
	if ip, ok := p.cachedGuestIP(name); ok {
		return guestEndpoint(p.ciUser, ip, path)
	}
	return name + ":" + path
}

// resolveEndpoint turns whichever endpoint form Copy was handed into one scp
// understands, and reports the guest address it resolved (empty when the
// endpoint was a host path or was already resolved).
//
// The three cases are distinguished exactly as Lima's copy endpoints are: a
// colon whose left side contains no slash is a guest endpoint, so an absolute
// host path can never be mistaken for one. An '@' on that left side means the
// endpoint already names a user and host.
func (p *proxmoxProvider) resolveEndpoint(ctx context.Context, endpoint string) (string, string, error) {
	i := strings.IndexByte(endpoint, ':')
	if i <= 0 || strings.ContainsAny(endpoint[:i], "/@") {
		return endpoint, "", nil
	}
	name, path := endpoint[:i], endpoint[i+1:]
	ip, err := p.guestIP(ctx, name)
	if err != nil {
		return "", "", err
	}
	return guestEndpoint(p.ciUser, ip, path), ip, nil
}

// guestEndpoint renders scp's `user@host:path`, bracketing an IPv6 literal.
// Without the brackets scp reads the address's own colons as the host/path
// separator and fails on a perfectly valid address.
func guestEndpoint(user, ip, path string) string {
	if addr, err := netip.ParseAddr(ip); err == nil && addr.Is6() && !addr.Is4In6() {
		return user + "@[" + ip + "]:" + path
	}
	return user + "@" + ip + ":" + path
}

// foldStderr appends a command's stderr to its error, so the caller sees the
// guest's own explanation rather than a bare exit status.
func foldStderr(err error, stderr []byte) error {
	if msg := strings.TrimSpace(string(stderr)); msg != "" {
		return fmt.Errorf("%w: %s", err, msg)
	}
	return err
}

// execSSH is the production ssh/scp executor. cmd.WaitDelay reaps the orphaned
// ssh child of a cancelled guest command — the same multi-generation orphan
// hazard internal/lima's runner documents, one process shallower here because
// there is no limactl in between.
func execSSH(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = sshWaitDelay
	return cmd.Run()
}

// --- interactive attach & guest identity ----------------------------------------

// AttachArgv wraps the SAME guest tmux expression the Lima providers use in an
// `ssh -t` to the VM, so `sand shell` and the TUI's S verb behave identically on
// every backend. The expression comes from lima.GuestAttachArgv rather than
// being retyped here — see that function and internal/lima/attach.go for why a
// second copy of it is the most destructive drift in the codebase.
//
// Unlike the Lima providers there is no --workdir to pass: `limactl shell`
// injects a `cd <host-cwd>` that the flag exists to override, while sshd starts
// the login shell in the guest user's own home already.
//
// The address lookup is bounded (attachResolveTimeout) because this method
// cannot report an error and runs on the TUI's update goroutine. When it fails,
// the returned argv is a command that FAILS LOUDLY. That is deliberate on two
// counts: returning nil would panic the caller (which indexes argv[0]), and
// returning an ssh to the bare instance name could connect to an unrelated
// machine that happens to answer to it on the local network.
func (p *proxmoxProvider) AttachArgv(v vm.VM) []string {
	ctx, cancel := context.WithTimeout(context.Background(), attachResolveTimeout)
	defer cancel()

	ip, err := p.guestIP(ctx, v.Name)
	if err != nil {
		return failArgv(fmt.Sprintf("sand: cannot attach to %q: %v", v.Name, err))
	}
	return p.sshHost(ip).SSHArgv(true, lima.GuestAttachArgv(os.Getenv("COLORTERM"))...)
}

// RunArgv returns the full argv that runs ONE interactive guest command (expr)
// with a real TTY, in workdir — the Landing pane's commit-and-push action needs
// it because `git commit` opens the user's editor, which requires a terminal
// rather than the captured-output transport every other path uses. The Lima
// providers reach this through `limactl shell --workdir`; this one ssh -t's
// straight to the guest, since a Proxmox VM has no limactl in front of it.
//
// SAFETY: workdir is the lowest-trust string in the system — a checkout path
// DISCOVERED BY SWEEPING THE GUEST — so, exactly as the Lima RunArgv keeps it
// out of expr by passing it as its own `--workdir` element, this passes workdir
// (and COLORTERM) to the guest bash as POSITIONAL DATA PARAMETERS ($1/$2),
// never spliced into the script text. `cd "$1"` therefore treats a hostile path
// as a directory name, never as shell to interpret; expr stays a fixed literal
// that must compute anything it needs about the checkout from the cwd it lands
// in, never receive it by host-side interpolation.
func (p *proxmoxProvider) RunArgv(v vm.VM, workdir, expr string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), attachResolveTimeout)
	defer cancel()

	ip, err := p.guestIP(ctx, v.Name)
	if err != nil {
		return failArgv(fmt.Sprintf("sand: cannot run in %q: %v", v.Name, err))
	}
	// $1 = workdir, $2 = COLORTERM (either may be empty); both are data, so a
	// path or terminal value containing shell metacharacters cannot execute.
	// `shift 2` then clears them so expr sees no leftover positional parameters.
	script := `cd "$1" || exit 1
[ -n "$2" ] && export COLORTERM="$2"
shift 2
` + expr
	return p.sshHost(ip).SSHArgv(true, "bash", "-c", script, "sand-run", workdir, os.Getenv("COLORTERM"))
}

// failArgv is a real, runnable command that prints msg and exits non-zero — the
// only honest answer for an argv-returning method that has just failed. The
// message travels as $0 rather than being spliced into the script, so nothing in
// it can be interpreted by the shell.
func failArgv(msg string) []string {
	return []string{"sh", "-c", `printf '%s\n' "$0" >&2; exit 1`, msg}
}

// GuestUser is the cloud-init ciuser this provider configures — read from
// configuration rather than from the guest, because sand chose it.
func (p *proxmoxProvider) GuestUser(vm.VM) string { return p.ciUser }

// GuestHome is the guest login user's home. cloud-init creates it at
// /home/<ciuser> — with none of Lima's ".guest" suffix, which is why the Lima
// providers must read it out of a generated file and this one need not.
func (p *proxmoxProvider) GuestHome(vm.VM) string {
	if p.ciUser == "" {
		return ""
	}
	return "/home/" + p.ciUser
}

// HostUser is the GUEST login user, not this machine's.
//
// The interface's contract is that a new VM's user must default to whatever
// account the shell will land in, or the playbook provisions one account (git
// identity, ~/.tmux.conf, secrets) while the user logs into another. For local
// Lima that account happens to mirror the host user, and for remote Lima it is
// the remote host's — but for Proxmox there is no "host limactl runs on" at all:
// sand creates the account itself as the cloud-init ciuser. Returning that same
// value here is what keeps the account sand provisions and the account sand logs
// into the same account.
func (p *proxmoxProvider) HostUser() string { return p.ciUser }

// HostResources reports node capacity from the Proxmox API: CPU count and
// total memory from GET /nodes/{node}/status, and disk free/total from the
// CONFIGURED VM STORAGE's own status — the meaningful denominator for "how
// much room is left for sandboxes" — never the node's own rootfs.
//
// Every field the API does not supply is left 0 ("unknown"): the board header
// treats 0 as a missing clause and drops it, and the low-capacity warning
// refuses to compute a percentage from it. A fabricated real-looking zero
// would instead read as "0 bytes free" and fire a false low-disk warning —
// this is the single most important correctness property of this method.
//
// The interface has no error return, so a failed sample (a node this token
// cannot Sys.Audit, an unreachable storage) degrades to zeros rather than
// propagating. This runs on the TUI's refresh timer, so it does not log on
// every call.
func (p *proxmoxProvider) HostResources() HostResources {
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()

	var hr HostResources
	if ns, err := p.client.NodeStatus(ctx); err == nil {
		hr.CPUs = ns.CPUInfo.CPUs
		hr.MemBytes = ns.Memory.Total
	}
	// Do NOT compute headroom as total-used: PVE defines
	// memused = memtotal - memavailable, so used+free != total. Memory
	// headroom is deliberately not surfaced here at all (see ns.Memory.
	// Available if a future caller wants it) — HostResources only ever
	// carries the total.
	if p.storage != "" {
		if ss, err := p.client.StorageStatus(ctx, p.storage); err == nil && ss.HasSizeReading() {
			hr.DiskFreeBytes = ss.Avail
			hr.DiskTotalBytes = ss.Total
		}
	}
	return hr
}

// HostFiles returns the local, per-endpoint state directory that stands in for
// "the host where limactl runs" — see proxmoxfiles.go, which explains why that
// is the honest answer for a backend with no such host.
func (p *proxmoxProvider) HostFiles() lima.HostFiles { return p.files }

// --- preflight ------------------------------------------------------------------

// Preflight verifies everything a lifecycle operation will need, and names the
// specific cause of each failure — the whole point being that an operator gets
// "the token lacks Pool.Audit on /pool/sandbar" instead of a 403 surfacing
// mid-create as a task that failed for no stated reason.
//
// It checks, in order: that the API answers, that the token is accepted, that
// the node exists, that the version is supported, that the pool exists, and that
// the storage exists and can hold VM images.
func (p *proxmoxProvider) Preflight() error {
	// Fail fast on a missing/unreadable SSH key BEFORE any network call: sand
	// installs <identity_path>.pub into the guest via cloud-init and then
	// authenticates the ssh transport with the private key, so a Proxmox profile
	// without a readable identity_path can never yield a reachable VM. Catching it
	// here — the cheapest check, a local file read — beats failing minutes into
	// the first base build, which is exactly where it used to surface.
	if _, err := readPublicKey(p.identityPath); err != nil {
		return fmt.Errorf("proxmox: %w; set identity_path in the profile to an SSH private key whose .pub sits beside it (e.g. ~/.ssh/id_ed25519, with ~/.ssh/id_ed25519.pub present)", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()

	status, err := p.client.NodeStatus(ctx)
	if err != nil {
		switch {
		case pve.IsPermission(err):
			return fmt.Errorf("proxmox: %s rejected the API token — check the token id and secret in the token file, and that the token has Sys.Audit on /nodes/%s: %w", p.host, p.node, err)
		case pve.IsNotFound(err):
			return fmt.Errorf("proxmox: %s has no node named %q — this is PVE's own node name (e.g. \"pve\"), not the hostname you connect to: %w", p.host, p.node, err)
		default:
			return fmt.Errorf("proxmox: cannot reach the Proxmox VE API at %s: %w", p.host, err)
		}
	}

	// A version this code cannot parse is NOT treated as too old: PVE has
	// changed the format of this field before, and refusing to run against a
	// perfectly good host over a cosmetic change would be worse than skipping a
	// check whose failure mode (an opaque 403 later) the checks below mostly
	// cover anyway.
	if atLeast, parsed := pveVersionAtLeast(status.PVEVersion, minPVEMajor, minPVEMinor); parsed && !atLeast {
		return fmt.Errorf("proxmox: node %s runs %s; sand requires Proxmox VE %d.%d or newer", p.node, status.PVEVersion, minPVEMajor, minPVEMinor)
	}

	pools, err := p.client.Pools(ctx)
	if err != nil {
		return fmt.Errorf("proxmox: cannot list resource pools on %s: %w", p.host, err)
	}
	if !slices.ContainsFunc(pools, func(pool pve.Pool) bool { return pool.PoolID == p.pool }) {
		// PVE filters this listing by permission, so the two causes are
		// indistinguishable from here and BOTH must be named — asserting the
		// pool does not exist would send an operator hunting for one that is
		// sitting right there, unauditable.
		return fmt.Errorf("proxmox: pool %q does not exist on %s, or this token cannot see it (it needs Pool.Audit on /pool/%s)", p.pool, p.host, p.pool)
	}

	if p.storage == "" {
		return errors.New("proxmox: profile has no storage (the PVE storage that will hold VM disks and cloud-init drives)")
	}
	storage, err := p.client.StorageStatus(ctx, p.storage)
	if err != nil {
		return fmt.Errorf("proxmox: storage %q is not usable on node %s: %w", p.storage, p.node, err)
	}
	if !storage.SupportsContent("images") {
		return fmt.Errorf("proxmox: storage %q on node %s does not accept \"images\" content (it accepts %q), so VM disks and cloud-init drives cannot be created there", p.storage, p.node, storage.Content)
	}

	// The cloud image is downloaded once with content=import, which PVE's
	// download-url endpoint accepts ONLY on file-based storages (dir/NFS/CIFS) —
	// a block storage like zfspool or lvm-thin rejects it with a 500 far into the
	// first build. Catch it here, on the image storage (which may be p.storage or
	// the "local" default), and say exactly how to fix it. Skip the second lookup
	// when the image storage IS the disk storage and already passed above.
	if p.imageStorage == p.storage {
		if !storage.SupportsContent("import") {
			return fmt.Errorf("proxmox: storage %q on node %s does not accept \"import\" content (it accepts %q) — the cloud image is downloaded there with content=import, which block storages like zfspool and lvm-thin reject; set image_storage to a file-based (dir/NFS/CIFS) storage, or enable it with `pvesm set %s --content %s,import`", p.storage, p.node, storage.Content, p.storage, storage.Content)
		}
		return nil
	}
	imgStore, err := p.client.StorageStatus(ctx, p.imageStorage)
	if err != nil {
		return fmt.Errorf("proxmox: image storage %q is not usable on node %s: %w (set image_storage to a file-based storage that exists, or leave it unset to use %q)", p.imageStorage, p.node, err, defaultImageStorage)
	}
	if !imgStore.SupportsContent("import") {
		return fmt.Errorf("proxmox: image storage %q on node %s does not accept \"import\" content (it accepts %q) — the cloud image is downloaded there with content=import, which block storages like zfspool and lvm-thin reject; set image_storage to a file-based (dir/NFS/CIFS) storage, or enable it with `pvesm set %s --content %s,import`", p.imageStorage, p.node, imgStore.Content, p.imageStorage, imgStore.Content)
	}
	return nil
}

// pveVersionAtLeast reports whether a PVE version string is at least
// major.minor, and — separately — whether it could be parsed at all. The two
// answers are distinct on purpose: "older than 9.0" and "unreadable" call for
// opposite responses, and collapsing them would let a format change lock every
// user out (see Preflight).
//
// It accepts both shapes the API produces: the full "pve-manager/8.2.4/abcdef"
// and a bare "8.2.4".
func pveVersionAtLeast(version string, major, minor int) (atLeast, parsed bool) {
	s := version
	if _, rest, ok := strings.Cut(s, "/"); ok {
		s = rest // drop the "pve-manager/" prefix
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i] // drop the trailing build hash
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return false, false
	}
	gotMajor, err1 := strconv.Atoi(parts[0])
	gotMinor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false, false
	}
	if gotMajor != major {
		return gotMajor > major, true
	}
	return gotMinor >= minor, true
}

// --- provisioning lifecycle -----------------------------------------------------
//
// The implementations live in proxmoxprovision.go — building the base as a PVE
// template, cloning from it, and the partial-failure cleanup — so these three
// methods stay one-line delegations and this file keeps its discovery/power/
// transport focus.

func (p *proxmoxProvider) Create(ctx context.Context, cfg vm.CreateConfig, opts provision.CreateOptions, out io.Writer) error {
	return p.createInstance(ctx, cfg, opts, out)
}

func (p *proxmoxProvider) Recreate(ctx context.Context, cfg vm.CreateConfig, opts provision.CreateOptions, out io.Writer) error {
	return p.recreateInstance(ctx, cfg, opts, out)
}

func (p *proxmoxProvider) Reset(ctx context.Context, cfg vm.CreateConfig, opts provision.ResetOptions, out io.Writer) error {
	return p.resetInstance(ctx, cfg, opts, out)
}

// --- progress -------------------------------------------------------------------

// progress writes one line of narration for the streaming lifecycle calls,
// tolerating the nil writer the buffered forms pass. The TUI shows these while a
// multi-minute boot runs, so a silent operation reads as a hung one.
func progress(out io.Writer, format string, args ...any) {
	if out == nil {
		return
	}
	fmt.Fprintf(out, format, args...)
}

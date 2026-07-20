package pve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// VMResource is one entry of GET /cluster/resources. The endpoint's own
// "type" query parameter enum is vm|storage|node|sdn — "qemu" is not a valid
// value and returns 400 — but each returned item's *own* Type field then
// reads "qemu" or "lxc" (both report through type=vm). ListVMs keeps only the
// "qemu" ones.
type VMResource struct {
	VMID    int     `json:"vmid"`
	Name    string  `json:"name"`
	Node    string  `json:"node"`
	Pool    string  `json:"pool"`
	Status  string  `json:"status"`
	Type    string  `json:"type"`
	MaxMem  int64   `json:"maxmem"`
	MaxDisk int64   `json:"maxdisk"`
	CPUs    float64 `json:"cpus"`
}

// ListVMs lists qemu VMs across the cluster, filtered to pool when non-empty.
// It queries type=vm (the only valid enum value) and filters client-side to
// items whose own Type field is "qemu", excluding "lxc" containers that the
// same query also returns.
func (c *Client) ListVMs(ctx context.Context, pool string) ([]VMResource, error) {
	var all []VMResource
	if err := c.do(ctx, http.MethodGet, "/cluster/resources", url.Values{"type": {"vm"}}, nil, &all); err != nil {
		return nil, err
	}
	out := make([]VMResource, 0, len(all))
	for _, r := range all {
		if r.Type != "qemu" {
			continue
		}
		if pool != "" && r.Pool != pool {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// NextID returns a free VMID via GET /cluster/nextid. This endpoint takes no
// lock and reserves nothing — the returned id can still be taken by another
// creator before this client's create call lands. Never use
// "nextid?vmid=N" as an existence check: it 400s when the id is taken, rather
// than reporting availability.
func (c *Client) NextID(ctx context.Context) (int, error) {
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodGet, "/cluster/nextid", nil, nil, &raw); err != nil {
		return 0, err
	}
	// PVE returns the id as a JSON string ("100"); tolerate a bare number too
	// since this is a one-field response not worth failing over.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		id, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("pve: parsing nextid %q: %w", s, err)
		}
		return id, nil
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, fmt.Errorf("pve: parsing nextid: %w", err)
	}
	return n, nil
}

// CreateVMOptions configures POST /nodes/{node}/qemu. Its formValues method
// emits defaults that override PVE's own — which are wrong for cloud images —
// per the endpoint semantics verified against upstream PVE source; see the
// per-field comments in formValues.
type CreateVMOptions struct {
	VMID    int
	Name    string
	Cores   int
	Memory  int // MiB
	DiskGB  int
	Storage string
	Bridge  string
	Pool    string
	SSHKeys []string
	CIUser  string
	// ImportFrom is an optional source storage volid (e.g.
	// "local:import/debian-13.qcow2") to import as scsi0 instead of
	// allocating a fresh empty disk.
	ImportFrom string
}

// formValues builds the POST body for CreateVM. Storage, Bridge, and Pool are
// required: Storage backs scsi0/ide2, an omitted Bridge silently gives QEMU
// user-mode NAT (unreachable over SSH in a way that looks like a boot
// failure), and an omitted Pool is what makes every later permission check
// fail under a pool-scoped token.
func (o CreateVMOptions) formValues() (url.Values, error) {
	if o.Storage == "" {
		return nil, errors.New("pve: CreateVMOptions.Storage is required")
	}
	if o.Bridge == "" {
		return nil, errors.New("pve: CreateVMOptions.Bridge is required (omitting it silently gives QEMU user-mode NAT, not \"no network\")")
	}
	if o.Pool == "" {
		return nil, errors.New("pve: CreateVMOptions.Pool is required (it is what makes the VM a pool member)")
	}

	form := url.Values{
		"vmid": {strconv.Itoa(o.VMID)},
		// PVE defaults scsihw to "lsi", which cloud images do not drive.
		"scsihw": {"virtio-scsi-pci"},
		// The bare/legacy "boot=scsi0" form is deprecated in favour of
		// the explicit "order=" form.
		"boot":    {"order=scsi0"},
		"agent":   {"1"},
		"ostype":  {"l26"},
		"serial0": {"socket"},
		"vga":     {"serial0"},
		// ip=auto is IPv6 SLAAC only and is rejected here; gw may not be
		// combined with ip=dhcp, so it is deliberately never set.
		"ipconfig0": {"ip=dhcp"},
		// This is what makes the new VM a pool member, and therefore what
		// makes every later permission check succeed under a pool-scoped
		// token. Must never be omitted.
		"pool": {o.Pool},
		// Omitting bridge silently gives QEMU user-mode NAT, not "no
		// network" — which would make the VM unreachable over SSH in a
		// way that looks like a boot failure.
		"net0": {"virtio,bridge=" + o.Bridge},
	}
	if o.Name != "" {
		form.Set("name", o.Name)
	}
	if o.Cores > 0 {
		form.Set("cores", strconv.Itoa(o.Cores))
	}
	if o.Memory > 0 {
		form.Set("memory", strconv.Itoa(o.Memory))
	}
	if o.ImportFrom != "" {
		// The ":0" is enforced by PVE when import-from is used.
		form.Set("scsi0", fmt.Sprintf("%s:0,import-from=%s", o.Storage, o.ImportFrom))
	} else {
		// A bare number here means GiB (unlike resize, where a bare
		// number means bytes).
		form.Set("scsi0", fmt.Sprintf("%s:%d", o.Storage, o.DiskGB))
	}
	// Requires a storage with "images" content; the built-in "local"
	// storage has none by default. Callers are responsible for surfacing
	// a clear error from the resulting task failure if it does not.
	form.Set("ide2", o.Storage+":cloudinit")
	if o.CIUser != "" {
		form.Set("ciuser", o.CIUser)
	}
	if len(o.SSHKeys) > 0 {
		form.Set("sshkeys", encodeSSHKeys(o.SSHKeys))
	}
	return form, nil
}

// encodeSSHKeys encodes keys for the sshkeys form field. PVE's server does
// URI::Escape::uri_unescape on this value, and the request body is itself
// form-urlencoded — so url.Values.Encode() escapes this a second time while
// the server decodes only once: two encodes, one decode. That double-encode
// is why '+' must be replaced with the literal "%20" here rather than left
// as-is: a real '+' would decode to a space on the FIRST (server-side)
// unescape, then still be sitting as a literal '+' after the SECOND escape
// undoes only one layer — producing a literal "ssh-rsa%20AAAA..." in the
// guest's authorized_keys. Do not "simplify" this; it is what mature Go PVE
// clients converged on.
func encodeSSHKeys(keys []string) string {
	joined := strings.Join(keys, "\n")
	return strings.ReplaceAll(url.QueryEscape(joined), "+", "%20")
}

// CreateVM creates a VM via POST /nodes/{node}/qemu, an asynchronous call
// that returns a UPID.
func (c *Client) CreateVM(ctx context.Context, opts CreateVMOptions) (UPID, error) {
	form, err := opts.formValues()
	if err != nil {
		return UPID{}, err
	}
	var raw string
	path := fmt.Sprintf("/nodes/%s/qemu", c.node)
	if err := c.do(ctx, http.MethodPost, path, nil, form, &raw); err != nil {
		return UPID{}, err
	}
	return ParseUPID(raw)
}

// createVMExistsRE matches the 500 PVE returns when the chosen vmid is
// already taken by another VM or container, distinguishing that specific
// collision from any other create failure (which must propagate to the
// caller, never be silently retried).
var createVMExistsRE = regexp.MustCompile(`(?i)already exists|config file already exists`)

// maxNextIDAttempts bounds CreateVMWithNextID's retry loop.
const maxNextIDAttempts = 5

// CreateVMWithNextID allocates a vmid via NextID and creates a VM with it. If
// the create fails because that vmid was taken by a concurrent creator
// between the NextID call and the create call (NextID reserves nothing), it
// re-asks NextID for a FRESH id and retries, bounded to maxNextIDAttempts.
// It deliberately never increments the id locally: linear probing stalls for
// seconds across occupied ID ranges.
func (c *Client) CreateVMWithNextID(ctx context.Context, opts CreateVMOptions) (int, UPID, error) {
	var lastErr error
	for attempt := 0; attempt < maxNextIDAttempts; attempt++ {
		vmid, err := c.NextID(ctx)
		if err != nil {
			return 0, UPID{}, err
		}
		opts.VMID = vmid

		upid, err := c.CreateVM(ctx, opts)
		if err == nil {
			return vmid, upid, nil
		}

		var ae *APIError
		if errors.As(err, &ae) && ae.Status == http.StatusInternalServerError && createVMExistsRE.MatchString(ae.Message) {
			lastErr = err
			continue // fresh NextID next iteration, never a local increment
		}
		return 0, UPID{}, err
	}
	return 0, UPID{}, fmt.Errorf("pve: CreateVMWithNextID: exhausted %d attempts, last error: %w", maxNextIDAttempts, lastErr)
}

// CloneVMOptions configures POST .../qemu/{vmid}/clone.
type CloneVMOptions struct {
	NewID int
	Name  string
	Pool  string
	// Full, when true, requests a full (independent) clone rather than a
	// linked clone. PVE's own default is !is_template(source); this client
	// never relies on that default and always sends full explicitly.
	Full bool
	// Storage and Format are ONLY sent when Full is true: passing either
	// for a linked clone is a hard error upstream.
	Storage string
	Format  string
}

// CloneVM clones vmid via POST .../qemu/{vmid}/clone, an asynchronous call
// that returns a UPID. Storage and Format are included in the request only
// when opts.Full is true — sending either for a linked clone is rejected by
// PVE as a hard error.
func (c *Client) CloneVM(ctx context.Context, vmid int, opts CloneVMOptions) (UPID, error) {
	form := url.Values{"newid": {strconv.Itoa(opts.NewID)}}
	if opts.Name != "" {
		form.Set("name", opts.Name)
	}
	if opts.Pool != "" {
		form.Set("pool", opts.Pool)
	}
	if opts.Full {
		form.Set("full", "1")
		if opts.Storage != "" {
			form.Set("storage", opts.Storage)
		}
		if opts.Format != "" {
			form.Set("format", opts.Format)
		}
	}
	// else: linked clone — storage and format are HARD ERRORS, send neither.

	var raw string
	path := fmt.Sprintf("/nodes/%s/qemu/%d/clone", c.node, vmid)
	if err := c.do(ctx, http.MethodPost, path, nil, form, &raw); err != nil {
		return UPID{}, err
	}
	return ParseUPID(raw)
}

// ConvertToTemplate converts vmid to a template via POST .../qemu/{vmid}/template,
// an asynchronous call that returns a UPID.
func (c *Client) ConvertToTemplate(ctx context.Context, vmid int) (UPID, error) {
	var raw string
	path := fmt.Sprintf("/nodes/%s/qemu/%d/template", c.node, vmid)
	if err := c.do(ctx, http.MethodPost, path, nil, nil, &raw); err != nil {
		return UPID{}, err
	}
	return ParseUPID(raw)
}

// VMConfig is a qemu VM's configuration, returned as a free-form map because
// its keys vary enormously by hardware configuration (diskN, netN, ...); the
// "digest" key can be carried back on a subsequent mutating call for
// optimistic concurrency.
type VMConfig map[string]any

// GetConfig reads vmid's configuration via GET .../qemu/{vmid}/config. It
// always sends current=1: without it, this endpoint returns *pending* values
// (uncommitted, possibly never-applied edits) rather than the VM's real
// current state.
func (c *Client) GetConfig(ctx context.Context, vmid int) (VMConfig, error) {
	var cfg VMConfig
	path := fmt.Sprintf("/nodes/%s/qemu/%d/config", c.node, vmid)
	if err := c.do(ctx, http.MethodGet, path, url.Values{"current": {"1"}}, nil, &cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// SetConfigSync writes vmid's configuration via PUT .../qemu/{vmid}/config,
// which — counter-intuitively — is the SYNCHRONOUS form (no UPID): PUT
// applies immediately, POST (SetConfigAsync) is the one that runs as a
// background task. Prefer this for simple metadata writes.
func (c *Client) SetConfigSync(ctx context.Context, vmid int, form url.Values) error {
	path := fmt.Sprintf("/nodes/%s/qemu/%d/config", c.node, vmid)
	return c.do(ctx, http.MethodPut, path, nil, form, nil)
}

// SetConfigAsync writes vmid's configuration via POST .../qemu/{vmid}/config,
// which — counter-intuitively — is the ASYNCHRONOUS form, returning a UPID
// ("almost any configuration change can involve hot-plug actions", per
// upstream). SetConfigSync (PUT) is synchronous.
func (c *Client) SetConfigAsync(ctx context.Context, vmid int, form url.Values) (UPID, error) {
	path := fmt.Sprintf("/nodes/%s/qemu/%d/config", c.node, vmid)
	var raw string
	if err := c.do(ctx, http.MethodPost, path, nil, form, &raw); err != nil {
		return UPID{}, err
	}
	return ParseUPID(raw)
}

// ResizeDisk resizes disk on vmid via PUT .../qemu/{vmid}/resize, returning a
// UPID. sizeBytes is normalized to an ABSOLUTE size with an explicit unit
// suffix ("20G") before being sent: PVE reads a BARE number as bytes, so
// never emit one here. Shrinking is a hard error upstream; resizing to the
// current size is a silent successful no-op, which makes repeated calls with
// the same sizeBytes safely idempotent.
func (c *Client) ResizeDisk(ctx context.Context, vmid int, disk string, sizeBytes int64) (UPID, error) {
	form := url.Values{
		"disk": {disk},
		"size": {fmt.Sprintf("%dG", sizeBytes/(1<<30))},
	}
	path := fmt.Sprintf("/nodes/%s/qemu/%d/resize", c.node, vmid)
	var raw string
	if err := c.do(ctx, http.MethodPut, path, nil, form, &raw); err != nil {
		return UPID{}, err
	}
	if raw == "" {
		return UPID{}, nil
	}
	return ParseUPID(raw)
}

// RegenerateCloudInit regenerates vmid's cloud-init image via PUT
// .../qemu/{vmid}/cloudinit (synchronous, no UPID). A config write alone does
// NOT regenerate the cloud-init image — only VM start, this call, or hotplug
// do, and cloudinit is not in the default hotplug set — so callers must
// invoke this (or stop/start the VM) after any cloud-init-affecting config
// write for it to take effect.
func (c *Client) RegenerateCloudInit(ctx context.Context, vmid int) error {
	path := fmt.Sprintf("/nodes/%s/qemu/%d/cloudinit", c.node, vmid)
	return c.do(ctx, http.MethodPut, path, nil, nil, nil)
}

// vmStatusAction issues a power-control call, POST .../qemu/{vmid}/status/{action},
// returning a UPID.
func (c *Client) vmStatusAction(ctx context.Context, vmid int, action string) (UPID, error) {
	path := fmt.Sprintf("/nodes/%s/qemu/%d/status/%s", c.node, vmid, action)
	var raw string
	if err := c.do(ctx, http.MethodPost, path, nil, nil, &raw); err != nil {
		return UPID{}, err
	}
	return ParseUPID(raw)
}

// StartVM starts vmid via POST .../qemu/{vmid}/status/start.
func (c *Client) StartVM(ctx context.Context, vmid int) (UPID, error) {
	return c.vmStatusAction(ctx, vmid, "start")
}

// StopVM hard-stops vmid via POST .../qemu/{vmid}/status/stop.
func (c *Client) StopVM(ctx context.Context, vmid int) (UPID, error) {
	return c.vmStatusAction(ctx, vmid, "stop")
}

// ShutdownVM gracefully shuts down vmid via POST .../qemu/{vmid}/status/shutdown.
func (c *Client) ShutdownVM(ctx context.Context, vmid int) (UPID, error) {
	return c.vmStatusAction(ctx, vmid, "shutdown")
}

// RebootVM reboots vmid via POST .../qemu/{vmid}/status/reboot.
func (c *Client) RebootVM(ctx context.Context, vmid int) (UPID, error) {
	return c.vmStatusAction(ctx, vmid, "reboot")
}

// VMStatus is the shape of GET .../qemu/{vmid}/status/current.
type VMStatus struct {
	VMID      int     `json:"vmid"`
	Name      string  `json:"name"`
	Status    string  `json:"status"` // "running" or "stopped"
	QMPStatus string  `json:"qmpstatus"`
	Uptime    int64   `json:"uptime"`
	Mem       int64   `json:"mem"`
	MaxMem    int64   `json:"maxmem"`
	Disk      int64   `json:"disk"`
	MaxDisk   int64   `json:"maxdisk"`
	CPU       float64 `json:"cpu"`
	Lock      string  `json:"lock"`
	// CPUs is the number of CONFIGURED virtual CPUs, not a usage figure —
	// unlike CPU above, which is a 0..1 utilisation fraction. The two are one
	// character apart in the JSON ("cpus"/"cpu") and mean entirely different
	// things, so a caller wanting a core count must take this one. It is a
	// float64 because PVE publishes it as a JSON number that some versions
	// render fractionally (a cpulimit-shaped value), which would fail to decode
	// into an int.
	CPUs float64 `json:"cpus"`
}

// GetStatus reads vmid's current runtime status via GET .../qemu/{vmid}/status/current.
func (c *Client) GetStatus(ctx context.Context, vmid int) (VMStatus, error) {
	var st VMStatus
	path := fmt.Sprintf("/nodes/%s/qemu/%d/status/current", c.node, vmid)
	if err := c.do(ctx, http.MethodGet, path, nil, nil, &st); err != nil {
		return VMStatus{}, err
	}
	return st, nil
}

// DeleteVM deletes vmid via DELETE .../qemu/{vmid}, returning a UPID. When
// purge is true, it also removes vmid from backup jobs and HA resources
// (PVE's "purge" query parameter).
func (c *Client) DeleteVM(ctx context.Context, vmid int, purge bool) (UPID, error) {
	query := url.Values{}
	if purge {
		query.Set("purge", "1")
	}
	var raw string
	path := fmt.Sprintf("/nodes/%s/qemu/%d", c.node, vmid)
	if err := c.do(ctx, http.MethodDelete, path, query, nil, &raw); err != nil {
		return UPID{}, err
	}
	return ParseUPID(raw)
}

// Snapshot is one entry of GET .../qemu/{vmid}/snapshot.
type Snapshot struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parent      string `json:"parent"`
	SnapTime    int64  `json:"snaptime"`
}

// ListSnapshots lists vmid's snapshots via GET .../qemu/{vmid}/snapshot.
func (c *Client) ListSnapshots(ctx context.Context, vmid int) ([]Snapshot, error) {
	var snaps []Snapshot
	path := fmt.Sprintf("/nodes/%s/qemu/%d/snapshot", c.node, vmid)
	if err := c.do(ctx, http.MethodGet, path, nil, nil, &snaps); err != nil {
		return nil, err
	}
	return snaps, nil
}

// CreateSnapshot creates a snapshot named name via POST .../qemu/{vmid}/snapshot,
// returning a UPID. When vmstate is true, RAM state is included (only
// meaningful for a running VM).
func (c *Client) CreateSnapshot(ctx context.Context, vmid int, name, description string, vmstate bool) (UPID, error) {
	form := url.Values{"snapname": {name}}
	if description != "" {
		form.Set("description", description)
	}
	if vmstate {
		form.Set("vmstate", "1")
	}
	var raw string
	path := fmt.Sprintf("/nodes/%s/qemu/%d/snapshot", c.node, vmid)
	if err := c.do(ctx, http.MethodPost, path, nil, form, &raw); err != nil {
		return UPID{}, err
	}
	return ParseUPID(raw)
}

// DeleteSnapshot deletes snapshot name via DELETE .../qemu/{vmid}/snapshot/{name},
// returning a UPID.
func (c *Client) DeleteSnapshot(ctx context.Context, vmid int, name string) (UPID, error) {
	var raw string
	path := fmt.Sprintf("/nodes/%s/qemu/%d/snapshot/%s", c.node, vmid, url.PathEscape(name))
	if err := c.do(ctx, http.MethodDelete, path, nil, nil, &raw); err != nil {
		return UPID{}, err
	}
	return ParseUPID(raw)
}

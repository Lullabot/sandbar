package pve

import (
	"context"
	"fmt"
	"net/http"
)

// NodeStatus is the decoded body of GET /nodes/{node}/status: host-wide CPU,
// memory, root filesystem, and CPU inventory. Every size here is in BYTES and
// every "cpu"-named fraction is 0..1, NOT a percentage — PVE's own web UI
// multiplies by 100 only at render time.
type NodeStatus struct {
	CPU  float64 `json:"cpu"`  // fraction 0..1, NOT percent
	Wait float64 `json:"wait"` // IO-wait fraction 0..1

	// Do NOT read an `idle` field from this endpoint: it is initialized to 0
	// and never overwritten upstream, so it is always 0 regardless of actual
	// load.

	Uptime  int64    `json:"uptime"`  // seconds
	LoadAvg []string `json:"loadavg"` // ARRAY OF STRINGS, not numbers

	Memory struct {
		Total int64 `json:"total"`
		Used  int64 `json:"used"`
		Free  int64 `json:"free"`
		// MemAvailable is the number to show as headroom. Proxmox computes
		// memused = memtotal - memavailable, so Used + Free does NOT equal
		// Total and Total - Used is not free memory. Available is the
		// honest figure.
		Available int64 `json:"available"`
	} `json:"memory"`

	RootFS struct {
		Total int64 `json:"total"`
		Used  int64 `json:"used"`
		Avail int64 `json:"avail"`
		Free  int64 `json:"free"`
	} `json:"rootfs"`

	CPUInfo struct {
		CPUs    int    `json:"cpus"`
		Sockets int    `json:"sockets"`
		Cores   int    `json:"cores"`
		Model   string `json:"model"`
		MHz     string `json:"mhz"` // published as a string, e.g. "2100.000"
	} `json:"cpuinfo"`

	PVEVersion string `json:"pveversion"`

	// Disk/MaxDisk are absent from the published API schema but present in
	// practice on some PVE versions. Pointers so their absence decodes
	// cleanly instead of silently becoming a false zero.
	Disk    *int64 `json:"disk,omitempty"`
	MaxDisk *int64 `json:"maxdisk,omitempty"`
}

// NodeStatus fetches host-wide CPU, memory, and root filesystem status for
// this client's node via GET /nodes/{node}/status.
func (c *Client) NodeStatus(ctx context.Context) (NodeStatus, error) {
	var out NodeStatus
	path := fmt.Sprintf("/nodes/%s/status", c.node)
	if err := c.do(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return NodeStatus{}, err
	}
	return out, nil
}

// StorageStatus is the decoded body of GET /nodes/{node}/storage/{storage}/status
// — the free/total space of one storage backend. All size fields are BYTES.
//
// An enabled-but-unreachable storage (e.g. an NFS mount that is down) returns
// enabled:1, active:0 and OMITS every size field entirely rather than sending
// zeros. Total/Used/Avail therefore decode as the Go zero value (0) in that
// case; callers MUST check HasSizeReading before trusting them — treating a
// bare 0 as "0 bytes free" would falsely trip a low-disk warning.
type StorageStatus struct {
	Total        int64   `json:"total"`
	Used         int64   `json:"used"`
	Avail        int64   `json:"avail"`
	UsedFraction float64 `json:"used_fraction"` // 0..1
	Active       int     `json:"active"`        // reachable right now
	Enabled      int     `json:"enabled"`       // enabled in config
	Content      string  `json:"content"`       // comma-separated, e.g. "images,iso"
}

// HasSizeReading reports whether s's size fields (Total/Used/Avail) are an
// honest reading rather than an omitted-fields zero value. A storage that is
// enabled but currently unreachable reports Active == 0 and its size fields
// are absent from the response — decoding to 0 — so that case must be
// reported as "unknown", never as zero free space.
func (s StorageStatus) HasSizeReading() bool {
	return s.Active == 1 && s.Total > 0
}

// SupportsContent reports whether kind (e.g. "images") is one of s's
// comma-separated Content types. Used before attempting to create a
// cloud-init drive on a storage, so a misconfigured storage produces a clear
// error instead of a cryptic PVE failure deep in VM creation.
func (s StorageStatus) SupportsContent(kind string) bool {
	for _, c := range splitContent(s.Content) {
		if c == kind {
			return true
		}
	}
	return false
}

// splitContent splits a comma-separated storage "content" string, skipping
// empty elements so "" yields no entries rather than one.
func splitContent(content string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(content); i++ {
		if i == len(content) || content[i] == ',' {
			if i > start {
				out = append(out, content[start:i])
			}
			start = i + 1
		}
	}
	return out
}

// StorageStatus fetches the free/total space of one storage backend via
// GET /nodes/{node}/storage/{storage}/status.
func (c *Client) StorageStatus(ctx context.Context, storage string) (StorageStatus, error) {
	var out StorageStatus
	path := fmt.Sprintf("/nodes/%s/storage/%s/status", c.node, storage)
	if err := c.do(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return StorageStatus{}, err
	}
	return out, nil
}

// Package baseimage holds the pinned base-image release and verified acquisition
// for the host where Lima runs. Proxmox uses the same manifest but downloads via
// PVE's server-side API.
package baseimage

import (
	"context"
	"fmt"
	"path"
	"runtime"
	"sort"
	"strings"

	"github.com/lullabot/sandbar/internal/lima"
)

//go:generate go run generate.go -tag base-image-2026.09.29.151245

type ImageEntry struct {
	URL      string
	Filename string
	SHA256   string
	Size     int64
}

type Manifest struct {
	Version string
	Images  map[string]ImageEntry
}

func (m Manifest) ForArch(arch string) (ImageEntry, error) {
	entry, ok := m.Images[arch]
	if !ok {
		keys := make([]string, 0, len(m.Images))
		for key := range m.Images {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return ImageEntry{}, fmt.Errorf("unsupported base-image architecture %q (supported: %s)", arch, strings.Join(keys, ", "))
	}
	return entry, nil
}

// LocalArch describes the machine running the local limactl process.
func LocalArch() (string, error) { return normalizeArch(runtime.GOARCH) }

// RemoteArch probes the remote limactl host. The controller's GOARCH can differ.
func RemoteArch(ctx context.Context, host lima.HostCommandRunner) (string, error) {
	out, err := host.HostOutput(ctx, "uname", "-m")
	if err != nil {
		return "", fmt.Errorf("detecting remote Lima host architecture: %w", err)
	}
	return normalizeArch(strings.TrimSpace(string(out)))
}

func normalizeArch(arch string) (string, error) {
	switch arch {
	case "amd64", "x86_64":
		return "amd64", nil
	case "arm64", "aarch64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported Lima host architecture %q (supported: amd64, arm64)", arch)
	}
}

func (e ImageEntry) valid() error {
	if path.Base(e.Filename) != e.Filename || e.Filename == "." || e.Filename == ".." || e.Filename == "" {
		return fmt.Errorf("invalid base-image filename %q", e.Filename)
	}
	if len(e.SHA256) != 64 || strings.Trim(e.SHA256, "0123456789abcdef") != "" {
		return fmt.Errorf("invalid SHA-256 for %s", e.Filename)
	}
	if e.URL == "" || e.Size <= 0 {
		return fmt.Errorf("incomplete base-image entry for %s", e.Filename)
	}
	return nil
}

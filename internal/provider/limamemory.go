package provider

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path/filepath"
	"strconv"
	"strings"
)

var _ VMHostMemoryProvider = (*limaProvider)(nil)

// VMHostMemory measures the host process that backs a running Lima VM. QEMU
// publishes its own PID in qemu.pid. VZ publishes the limactl host-agent PID in
// vz.pid, while macOS accounts the guest to a separate VirtualMachine worker;
// that worker is identified by its open instance disk, not by vz.pid's RSS.
// Process RSS is a host-side approximation and can include VM process overhead.
func (p *limaProvider) VMHostMemory(ctx context.Context, name string) (int64, error) {
	v, err := p.Get(name)
	if err != nil {
		return 0, err
	}
	if v.Status != "Running" || v.Dir == "" {
		return 0, nil
	}
	qemuPID, err := p.hostFiles.ReadFile(filepath.Join(v.Dir, "qemu.pid"))
	if err == nil {
		pid, err := strconv.Atoi(strings.TrimSpace(string(qemuPID)))
		if err != nil || pid <= 0 {
			return 0, fmt.Errorf("lima: invalid qemu.pid for %s", name)
		}
		return p.processRSS(ctx, pid, "qemu-system-")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return 0, fmt.Errorf("lima: read qemu.pid for %s: %w", name, err)
	}
	if _, err := p.hostFiles.ReadFile(filepath.Join(v.Dir, "vz.pid")); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil // another Lima driver with no measured host process
		}
		return 0, fmt.Errorf("lima: read vz.pid for %s: %w", name, err)
	}
	return p.vzWorkerRSS(ctx, v.Dir)
}

func (p *limaProvider) vzWorkerRSS(ctx context.Context, dir string) (int64, error) {
	// Lima 2 uses disk; older instances can retain the legacy diffdisk name.
	// lsof uses file identity to find the one VZ XPC worker holding this VM's
	// disk. Matching by process name alone could assign another VM's memory.
	var lastErr error
	for _, disk := range []string{"disk", "diffdisk"} {
		out, err := p.core.HostOutput(ctx, "lsof", "-nP", "-t", filepath.Join(dir, disk))
		if err != nil {
			lastErr = err
			continue
		}
		var found int64
		seen := make(map[int]bool)
		for _, field := range strings.Fields(string(out)) {
			pid, err := strconv.Atoi(field)
			if err != nil || pid <= 0 || seen[pid] {
				continue
			}
			seen[pid] = true
			rss, err := p.processRSS(ctx, pid, "com.apple.Virtualization.VirtualMachine")
			if err != nil {
				continue // another process may also hold the disk
			}
			if found != 0 {
				return 0, errors.New("lima: multiple VZ workers hold the VM disk")
			}
			found = rss
		}
		if found > 0 {
			return found, nil
		}
	}
	return 0, fmt.Errorf("lima: VZ worker for %s not found: %v", dir, lastErr)
}

func (p *limaProvider) processRSS(ctx context.Context, pid int, processName string) (int64, error) {
	out, err := p.core.HostOutput(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "rss=", "-o", "comm=")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue // tolerate a platform-specific header line
		}
		if kb <= 0 || kb > math.MaxInt64/1024 || !strings.Contains(strings.Join(fields[1:], " "), processName) {
			return 0, fmt.Errorf("lima: PID %d is not a measured %s process", pid, processName)
		}
		return kb * 1024, nil // ps rss is in KiB on macOS and Linux
	}
	return 0, fmt.Errorf("lima: no resident memory for PID %d", pid)
}

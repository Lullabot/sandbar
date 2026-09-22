package provider

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

// TestMemoryCapabilities pins support to the concrete backend that can actually
// return memory to its host. A Linux cache drop inside Lima does not reduce the
// macOS VM footprint, so merely having guest shell access must not advertise
// this capability.
func TestMemoryCapabilities(t *testing.T) {
	if _, ok := any(&limaProvider{}).(MemoryReclaimer); ok {
		t.Error("local Lima advertises memory reclaim; its host footprint does not shrink after a guest cache drop")
	}
	if _, ok := any(&limaProvider{}).(VMHostMemoryProvider); ok {
		t.Error("local Lima advertises a host-side per-VM memory reading it does not have")
	}

	m := newPVEMock(t)
	p := newProxmoxForTest(t, m)
	if _, ok := any(p).(MemoryReclaimer); !ok {
		t.Error("Proxmox does not advertise memory reclaim")
	}
	if _, ok := any(p).(VMHostMemoryProvider); !ok {
		t.Error("Proxmox does not advertise host-side per-VM memory measurement")
	}
}

// TestProxmoxReclaimMemoryUsesGuestSSH proves the operation crosses the
// provider boundary all the way to the existing direct guest SSH transport. It
// also pins the ordering and privilege boundary: sync completes before root
// writes drop_caches.
func TestProxmoxReclaimMemoryUsesGuestSSH(t *testing.T) {
	m, p := withGuest(t)
	argvs := recordSSH(p)
	m.reset()

	reclaimer := any(p).(MemoryReclaimer)
	if err := reclaimer.ReclaimMemory(context.Background(), "web"); err != nil {
		t.Fatalf("ReclaimMemory: %v", err)
	}
	if len(*argvs) != 1 {
		t.Fatalf("ReclaimMemory ran %d guest commands; want 1", len(*argvs))
	}
	argv := (*argvs)[0]
	if argv[0] != "ssh" || !slices.Contains(argv, "dev@192.168.1.50") {
		t.Fatalf("ReclaimMemory did not use the guest SSH transport: %v", argv)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"sudo bash -c", "sync", "echo 3 > /proc/sys/vm/drop_caches"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ReclaimMemory argv %q missing %q", joined, want)
		}
	}
	if got := m.seen(); len(got) != 0 {
		t.Errorf("ReclaimMemory contacted the PVE API instead of only the guest: %v", got)
	}
}

func TestProxmoxReclaimMemoryReportsGuestFailure(t *testing.T) {
	_, p := withGuest(t)
	want := errors.New("drop caches denied")
	p.runSSH = func(context.Context, []string, io.Reader, io.Writer, io.Writer) error { return want }

	err := any(p).(MemoryReclaimer).ReclaimMemory(context.Background(), "web")
	if !errors.Is(err, want) {
		t.Fatalf("ReclaimMemory error = %v; want guest failure %v", err, want)
	}
}

// TestProxmoxVMHostMemory reads status/current's mem field. That field is the
// QEMU process's host/hypervisor accounting and must stay separate from the
// guest's MemAvailable-derived utilization sample.
func TestProxmoxVMHostMemory(t *testing.T) {
	m, p := withGuest(t)
	const want int64 = 30 << 30
	m.data("/nodes/pve1/qemu/100/status/current",
		`{"vmid":100,"name":"web","status":"running","mem":32212254720,"maxmem":34359738368}`)
	m.reset()

	got, err := any(p).(VMHostMemoryProvider).VMHostMemory(context.Background(), "web")
	if err != nil {
		t.Fatalf("VMHostMemory: %v", err)
	}
	if got != want {
		t.Fatalf("VMHostMemory = %d; want PVE status/current mem %d", got, want)
	}
	if !m.sawPath("/nodes/pve1/qemu/100/status/current") {
		t.Fatalf("VMHostMemory did not read status/current; requests: %v", m.seen())
	}
}

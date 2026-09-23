package provider

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
)

// TestMemoryCapabilities pins support to the concrete backend that can actually
// return memory to its host. A Linux cache drop inside Lima does not reduce the
// macOS VM footprint, so merely having guest shell access must not advertise
// this capability.
func TestMemoryCapabilities(t *testing.T) {
	if _, ok := any(&limaProvider{}).(MemoryReclaimer); ok {
		t.Error("local Lima advertises memory reclaim; its host footprint does not shrink after a guest cache drop")
	}
	if _, ok := any(&limaProvider{}).(VMHostMemoryProvider); !ok {
		t.Error("local Lima does not expose its host-side per-VM process memory reading")
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

// hostMemoryRunner keeps both limactl discovery and the host process probes
// behind test seams; these tests never run limactl, ps, or lsof on this machine.
type hostMemoryRunner struct {
	dir       string
	hostCalls [][]string
	responses map[string]string
}

func (r *hostMemoryRunner) Output(_ context.Context, _ ...string) ([]byte, error) {
	return []byte(`{"name":"web","status":"Running","dir":"` + r.dir + `"}` + "\n"), nil
}
func (*hostMemoryRunner) Stream(context.Context, io.Reader, io.Writer, ...string) error { return nil }
func (*hostMemoryRunner) StreamOut(context.Context, io.Reader, io.Writer, ...string) error {
	return nil
}
func (r *hostMemoryRunner) HostOutput(_ context.Context, argv ...string) ([]byte, error) {
	r.hostCalls = append(r.hostCalls, append([]string(nil), argv...))
	if out, ok := r.responses[strings.Join(argv, "\x00")]; ok {
		return []byte(out), nil
	}
	return nil, errors.New("unexpected host command")
}

func TestLimaVZHostMemoryUsesVMWorkerOpeningItsDisk(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vz.pid"), []byte("123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(dir, "disk")
	r := &hostMemoryRunner{dir: dir, responses: map[string]string{
		strings.Join([]string{"lsof", "-nP", "-t", disk}, "\x00"):                        "123\n56040\n",
		strings.Join([]string{"ps", "-p", "123", "-o", "rss=", "-o", "comm="}, "\x00"):   "12000 /usr/local/bin/limactl\n",
		strings.Join([]string{"ps", "-p", "56040", "-o", "rss=", "-o", "comm="}, "\x00"): "7248256 /System/Library/Frameworks/Virtualization.framework/Versions/A/XPCServices/com.apple.Virtualization.VirtualMachine.xpc/Contents/MacOS/com.apple.Virtualization.VirtualMachine\n",
	}}
	p := NewLocalLima(lima.New(r), nil)
	got, err := p.(VMHostMemoryProvider).VMHostMemory(context.Background(), "web")
	if err != nil || got != 7248256*1024 {
		t.Fatalf("VZ host memory = %d, %v; want worker RSS in bytes", got, err)
	}
	want := [][]string{{"lsof", "-nP", "-t", disk}, {"ps", "-p", "123", "-o", "rss=", "-o", "comm="}, {"ps", "-p", "56040", "-o", "rss=", "-o", "comm="}}
	if !reflect.DeepEqual(r.hostCalls, want) {
		t.Fatalf("host commands = %v, want %v", r.hostCalls, want)
	}
}

func TestLimaQEMUHostMemoryUsesDriverPIDAndRejectsStalePID(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "qemu.pid"), []byte("1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ps := strings.Join([]string{"ps", "-p", "1234", "-o", "rss=", "-o", "comm="}, "\x00")
	r := &hostMemoryRunner{dir: dir, responses: map[string]string{ps: "500000 /usr/bin/qemu-system-aarch64\n"}}
	p := NewLocalLima(lima.New(r), nil)
	got, err := p.(VMHostMemoryProvider).VMHostMemory(context.Background(), "web")
	if err != nil || got != 500000*1024 {
		t.Fatalf("QEMU host memory = %d, %v; want process RSS in bytes", got, err)
	}
	r.responses[ps] = "500000 /bin/bash\n"
	if _, err := p.(VMHostMemoryProvider).VMHostMemory(context.Background(), "web"); err == nil {
		t.Fatal("a reused qemu.pid belonging to another process must not report that process's memory")
	}
}

func TestLimaVZHostMemoryFindsLegacyDiskAndRejectsWrongWorker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vz.pid"), []byte("123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyDisk := filepath.Join(dir, "diffdisk")
	ps := strings.Join([]string{"ps", "-p", "56040", "-o", "rss=", "-o", "comm="}, "\x00")
	r := &hostMemoryRunner{dir: dir, responses: map[string]string{
		strings.Join([]string{"lsof", "-nP", "-t", legacyDisk}, "\x00"): "56040\n56040\n",
		ps: "1024 /System/Library/com.apple.Virtualization.VirtualMachine\n",
	}}
	p := NewLocalLima(lima.New(r), nil).(VMHostMemoryProvider)
	got, err := p.VMHostMemory(context.Background(), "web")
	if err != nil || got != 1024*1024 {
		t.Fatalf("legacy VZ disk memory = %d, %v; want one worker RSS", got, err)
	}
	if !reflect.DeepEqual(r.hostCalls, [][]string{
		{"lsof", "-nP", "-t", filepath.Join(dir, "disk")},
		{"lsof", "-nP", "-t", legacyDisk},
		{"ps", "-p", "56040", "-o", "rss=", "-o", "comm="},
	}) {
		t.Fatalf("legacy disk probe = %v", r.hostCalls)
	}
	r.responses[ps] = "1024 /usr/local/bin/limactl\n"
	if _, err := p.VMHostMemory(context.Background(), "web"); err == nil {
		t.Fatal("a non-VM process opening the disk must not be reported as VM memory")
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

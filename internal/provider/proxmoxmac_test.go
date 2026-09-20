package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/pve"
)

func TestNicMACsFromConfig(t *testing.T) {
	cfg := pve.VMConfig{
		"net0":     "virtio=BC:24:11:AA:BB:CC,bridge=vmbr0",
		"net1":     "e1000,bridge=vmbr1,macaddr=BC:24:11:00:00:02",
		"net2":     "virtio,bridge=vmbr2", // no MAC: nothing to keep
		"scsi0":    "local-lvm:vm-105-disk-0,size=100G",
		"smbios1":  "uuid=deadbeef",
		"netblock": "not a nic", // not netN, must not be read as one
		"digest":   "abc123",
	}
	want := map[string]string{
		"net0": "bc:24:11:aa:bb:cc",
		"net1": "bc:24:11:00:00:02",
	}
	if got := nicMACsFromConfig(cfg); !reflect.DeepEqual(got, want) {
		t.Errorf("nicMACsFromConfig = %v, want %v", got, want)
	}
}

func TestNetWithMAC(t *testing.T) {
	cases := []struct {
		name string
		net  string
		mac  string
		want string
	}{
		{
			// The common shape: PVE hangs the MAC off the model key.
			name: "model key",
			net:  "virtio=BC:24:11:AA:BB:CC,bridge=vmbr0",
			mac:  "bc:24:11:de:ad:01",
			want: "virtio=BC:24:11:DE:AD:01,bridge=vmbr0",
		},
		{
			// Everything the operator put on the line survives, in place. A VLAN
			// tag silently dropped by a reset is a VM on the wrong network.
			name: "keeps every other field",
			net:  "virtio=BC:24:11:AA:BB:CC,bridge=vmbr0,tag=42,firewall=1,mtu=9000",
			mac:  "bc:24:11:de:ad:01",
			want: "virtio=BC:24:11:DE:AD:01,bridge=vmbr0,tag=42,firewall=1,mtu=9000",
		},
		{
			name: "macaddr key",
			net:  "e1000,bridge=vmbr1,macaddr=BC:24:11:AA:BB:CC",
			mac:  "bc:24:11:de:ad:01",
			want: "e1000,bridge=vmbr1,macaddr=BC:24:11:DE:AD:01",
		},
		{
			name: "no MAC to replace gains one",
			net:  "virtio,bridge=vmbr0",
			mac:  "bc:24:11:de:ad:01",
			want: "virtio,bridge=vmbr0,macaddr=BC:24:11:DE:AD:01",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := netWithMAC(tc.net, tc.mac); got != tc.want {
				t.Errorf("netWithMAC(%q, %q) = %q, want %q", tc.net, tc.mac, got, tc.want)
			}
		})
	}
}

// TestProxmoxResetKeepsTheMACAddress is the end-to-end claim: the MAC the VM had
// before the reset is written onto the clone, over the API, before it boots.
//
// The assertion is on the config WRITE rather than on the model, because the MAC
// is not sand's state to remember — it lives in PVE, and the only thing that
// makes the DHCP reservation keep matching is that PUT actually going out.
func TestProxmoxResetKeepsTheMACAddress(t *testing.T) {
	const (
		oldMAC = "BC:24:11:DE:AD:01"
		// What PVE generated for the clone, and what the guest agent fixture
		// reports — so guest-IP discovery still resolves while the test drives
		// the MAC back to oldMAC.
		cloneNet0 = "virtio=" + testMAC + ",bridge=vmbr0,tag=42,firewall=1"
	)

	m := newPVEMock(t)
	rec := &createRecorder{nextID: 100}
	stubProvisioning(t)
	shortAgentPolling(t, 5*time.Second)

	m.data("/cluster/resources", `[
	  {"vmid":100,"name":"sandbar-base","node":"pve1","pool":"sandbar","status":"stopped","type":"qemu","template":1},
	  {"vmid":105,"name":"web","node":"pve1","pool":"sandbar","status":"running","type":"qemu"}
	]`)
	m.data("/nodes/pve1/qemu/105/status/current", `{"vmid":105,"name":"web","status":"running"}`)
	m.data("/nodes/pve1/qemu/105/config", fmt.Sprintf(`{"net0":"virtio=%s,bridge=vmbr0,tag=7"}`, oldMAC))
	m.on("/nodes/pve1/qemu/105/status/stop", func(w http.ResponseWriter, _ *http.Request) { upidData(w, testUPID) })
	m.on("/nodes/pve1/qemu/105", func(w http.ResponseWriter, _ *http.Request) { upidData(w, testUPID) })
	m.on("/cluster/nextid", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":%q}`, strconv.Itoa(rec.nextVMID()))
	})
	m.on("/nodes/pve1/qemu/100/clone", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		newid, _ := strconv.Atoi(r.PostForm.Get("newid"))
		rec.setName(newid, r.PostForm.Get("name"))
		upidData(w, testUPID)
	})
	registerVM(m, rec, 101)

	// Override the clone's config route (last registration wins) to capture what
	// gets written to it and to serve a net0 with fields worth not losing.
	var mu sync.Mutex
	var writes []url.Values
	m.on("/nodes/pve1/qemu/101/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			_ = r.ParseForm()
			mu.Lock()
			writes = append(writes, r.PostForm)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":null}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":{"net0":%q}}`, cloneNet0)
	})
	m.okTask(testUPID)

	p := newProxmoxForTest(t, m)
	recordSSH(p)
	want, _ := p.templateVersion(webConfig())
	_ = provision.WriteBaseVersion(p.files, "sandbar-base", want, time.Now())

	if err := p.Reset(context.Background(), webConfig(), provision.ResetOptions{}, nil); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// The clone's OWN line, with only the MAC swapped: its tag and firewall flag
	// are PVE's to keep, not sand's to re-derive.
	wantNet0 := "virtio=" + oldMAC + ",bridge=vmbr0,tag=42,firewall=1"
	for _, form := range writes {
		if got := form.Get("net0"); got != "" {
			if got != wantNet0 {
				t.Fatalf("net0 written as %q, want %q", got, wantNet0)
			}
			return
		}
	}
	t.Fatalf("the reset never wrote net0 to the clone, so its MAC was left newly generated; writes=%v", writes)
}

// A reset whose VM is already gone has no MAC to keep, and must not treat that
// as a failure: `sand create --recreate` legitimately runs against a name whose
// VM was destroyed out from under it.
func TestProxmoxNicMACsMissingVMIsNotAnError(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", `[]`)
	p := newProxmoxForTest(t, m)

	macs, err := p.nicMACs(context.Background(), "gone")
	if err == nil {
		t.Fatal("a missing VM should report itself as missing")
	}
	if len(macs) != 0 {
		t.Errorf("a missing VM yielded MACs: %v", macs)
	}
	// The reset's own guard is errors.Is(err, lima.ErrNoSuchInstance); prove that
	// is the error a missing VM actually produces, or the guard silently turns
	// into "warn on every recreate".
	if !errors.Is(err, lima.ErrNoSuchInstance) {
		t.Errorf("a missing VM reported %v, which the reset will not recognise as 'no VM to read'", err)
	}
}

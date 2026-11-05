package registry

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"
)

// TestDDEVPortSlotRecordCompatibility: an index written before VMs had a ddev
// port slot must load unchanged with the slot absent (nil), not as slot 0 —
// a VM read as slot 0 would be told ports nobody allocated to it. And a slot
// that IS recorded, including slot 0, must survive a write and reload.
func TestDDEVPortSlotRecordCompatibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")
	// The shape the previous release wrote: schema v4, config without the key.
	prev := `{"version":4,"vms":[
		{"name":"web","provider":"lima","base":"sandbar-base","config":{"Name":"web","BaseName":"sandbar-base","CPUs":4,"Memory":"8GiB","WithDDEV":true,"WithClaude":false}}
	],"templates":[]}`
	if err := os.WriteFile(path, []byte(prev), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load previous-release index: %v", err)
	}
	got, ok := r.ConfigInScope("web", LocalScope)
	want := vm.CreateConfig{Name: "web", BaseName: "sandbar-base", CPUs: 4, Memory: "8GiB", WithDDEV: true}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("previous-release entry = %+v (ok=%v), want %+v with no slot", got, ok, want)
	}

	zero := 0
	if err := r.AddScoped(vm.CreateConfig{Name: "api", BaseName: "sandbar-base", WithDDEV: true, DDEVPortSlot: &zero}, LocalScope); err != nil {
		t.Fatal(err)
	}
	r2, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := r2.ConfigInScope("api", LocalScope); c.DDEVPortSlot == nil || *c.DDEVPortSlot != 0 {
		t.Fatalf("recorded slot 0 reloaded as %v, want 0", c.DDEVPortSlot)
	}
	if c, _ := r2.ConfigInScope("web", LocalScope); c.DDEVPortSlot != nil {
		t.Fatalf("unrecorded slot reloaded as %d, want none", *c.DDEVPortSlot)
	}
}

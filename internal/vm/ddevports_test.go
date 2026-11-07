package vm

import (
	"encoding/json"
	"strings"
	"testing"
)

func slotPtr(n int) *int { return &n }

// TestAllocateDDEVSlot covers the allocator's probe: the preferred slot is a
// pure function of the name (pinned, so a change to the hash or modulus —
// which would move every unrecorded VM's ports — fails here rather than in a
// user's browser), a held slot is probed past, the probe wraps from the last
// slot to 0, and a full table is an error rather than a reused slot.
func TestAllocateDDEVSlot(t *testing.T) {
	// FNV-1a 32 of "claude" is 3791422743; 3791422743 % 1000 = 743.
	if got := PreferredDDEVSlot("claude"); got != 743 {
		t.Fatalf("PreferredDDEVSlot(claude) = %d, want 743", got)
	}

	heldFrom := func(lo, hi int) map[int]bool {
		h := map[int]bool{}
		for s := lo; s <= hi; s++ {
			h[s] = true
		}
		return h
	}
	tests := []struct {
		name string
		held map[int]bool
		want int
	}{
		{"nothing held takes the preferred slot", nil, 743},
		{"slots held by other VMs are probed past", map[int]bool{743: true, 744: true, 12: true}, 745},
		{"probe wraps from the last slot to 0", heldFrom(743, DDEVPortSlots-1), 0},
		{"wrap continues past held low slots", func() map[int]bool {
			h := heldFrom(743, DDEVPortSlots-1)
			h[0], h[1] = true, true
			return h
		}(), 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := AllocateDDEVSlot("claude", tt.held)
			if err != nil || got != tt.want {
				t.Fatalf("AllocateDDEVSlot = %d, %v; want %d, nil", got, err, tt.want)
			}
		})
	}

	t.Run("every slot held is an error", func(t *testing.T) {
		got, err := AllocateDDEVSlot("claude", heldFrom(0, DDEVPortSlots-1))
		if err == nil {
			t.Fatalf("AllocateDDEVSlot on a full table = %d, nil; want an error", got)
		}
		if !strings.Contains(err.Error(), "no free ddev port slot") {
			t.Fatalf("error = %q, want it to say no slot is free", err)
		}
	})
}

// TestDDEVPortsForSlot pins the block layout: the guest role and the agent
// documentation both rely on these offsets, and the top slot must still land
// on a valid TCP port.
func TestDDEVPortsForSlot(t *testing.T) {
	want := DDEVPorts{
		RouterHTTP: 20070, RouterHTTPS: 20071,
		MailpitHTTP: 20072, MailpitHTTPS: 20073,
		XHGuiHTTP: 20074, XHGuiHTTPS: 20075,
		TraefikMonitor: 20076,
	}
	if got := DDEVPortsForSlot(7); got != want {
		t.Fatalf("DDEVPortsForSlot(7) = %+v, want %+v", got, want)
	}
	top := DDEVPortsForSlot(DDEVPortSlots - 1)
	if top.RouterHTTP != 29990 || top.TraefikMonitor != 29996 {
		t.Fatalf("DDEVPortsForSlot(%d) = %+v, want block 29990..29996", DDEVPortSlots-1, top)
	}
}

// TestDDEVPortSlotJSON proves slot 0 is a recorded value, distinct from "no
// slot": the field is a pointer precisely so a VM allocated slot 0 is not
// mistaken for a pre-feature record and reallocated on its next reset. A
// record written before the field existed must decode as absent.
func TestDDEVPortSlotJSON(t *testing.T) {
	for _, tt := range []struct {
		name    string
		slot    *int
		wantKey bool
	}{
		{"absent", nil, false},
		{"slot zero", slotPtr(0), true},
		{"slot 999", slotPtr(999), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(CreateConfig{Name: "web", DDEVPortSlot: tt.slot})
			if err != nil {
				t.Fatal(err)
			}
			if has := strings.Contains(string(data), `"DDEVPortSlot"`); has != tt.wantKey {
				t.Fatalf("encoded %s; DDEVPortSlot key present = %v, want %v", data, has, tt.wantKey)
			}
			var got CreateConfig
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			switch {
			case tt.slot == nil && got.DDEVPortSlot != nil:
				t.Fatalf("absent slot decoded as %d", *got.DDEVPortSlot)
			case tt.slot != nil && (got.DDEVPortSlot == nil || *got.DDEVPortSlot != *tt.slot):
				t.Fatalf("slot %d decoded as %v", *tt.slot, got.DDEVPortSlot)
			}
		})
	}
}

func TestValidateDDEVPortSlot(t *testing.T) {
	for _, tt := range []struct {
		slot    *int
		wantErr bool
	}{
		{nil, false},
		{slotPtr(0), false},
		{slotPtr(DDEVPortSlots - 1), false},
		{slotPtr(-1), true},
		{slotPtr(DDEVPortSlots), true},
	} {
		c := validConfig()
		c.DDEVPortSlot = tt.slot
		if err := c.Validate(); (err != nil) != tt.wantErr {
			t.Errorf("Validate with slot %v = %v, wantErr %v", tt.slot, err, tt.wantErr)
		}
	}
}

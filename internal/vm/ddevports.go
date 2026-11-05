package vm

import (
	"fmt"
	"hash/fnv"
)

// A ddev port slot is a block of host-visible ports one Lima VM's ddev router
// listens on instead of ddev's defaults (80/443, Mailpit 8025/8026, XHGui
// 8143/8142, Traefik monitor 10999).
//
// Lima forwards every TCP port a guest listens on to the SAME port on the
// Lima host's loopback, so two VMs both running ddev on its defaults race for
// one host port and the loser's forward fails silently — and on Linux the
// privileged 80/443 are not forwarded at all. Moving each VM's router onto
// its own block above 1024 lets several VMs, and a host-native ddev, serve at
// once. The ports are changed inside the guest's ddev configuration rather
// than remapped by Lima, so a URL is identical inside the guest and in the
// workstation's browser.
//
// The constants are fixed, not configurable: a recorded slot is turned back
// into ports on every finalize, so changing any of them would silently move
// every existing VM's URLs.
const (
	// DDEVPortBase is the first port of slot 0.
	DDEVPortBase = 20000
	// DDEVPortStride is the number of ports reserved per slot. Seven are used;
	// offsets +7..+9 are spare so a later ddev service can be added without
	// renumbering every recorded slot.
	DDEVPortStride = 10
	// DDEVPortSlots is the number of slots, so ports span 20000–29999.
	DDEVPortSlots = 1000
)

// DDEVPorts is one slot's port block.
type DDEVPorts struct {
	RouterHTTP     int
	RouterHTTPS    int
	MailpitHTTP    int
	MailpitHTTPS   int
	XHGuiHTTP      int
	XHGuiHTTPS     int
	TraefikMonitor int
}

// DDEVPortsForSlot returns the ports of slot. It does not range-check: callers
// hold a slot that came from AllocateDDEVSlot or passed CreateConfig.Validate.
func DDEVPortsForSlot(slot int) DDEVPorts {
	b := DDEVPortBase + DDEVPortStride*slot
	return DDEVPorts{
		RouterHTTP:     b,
		RouterHTTPS:    b + 1,
		MailpitHTTP:    b + 2,
		MailpitHTTPS:   b + 3,
		XHGuiHTTP:      b + 4,
		XHGuiHTTPS:     b + 5,
		TraefikMonitor: b + 6,
	}
}

// PreferredDDEVSlot is the slot a VM named name tries first: FNV-1a 32-bit of
// the name modulo DDEVPortSlots. The hash is pinned (not, say, Go's map hash,
// which is seeded per process) so the same name prefers the same slot on
// every controller and across releases; recreating a VM under its old name
// therefore tends to get its old ports back even without a record.
func PreferredDDEVSlot(name string) int {
	h := fnv.New32a()
	h.Write([]byte(name)) // hash.Hash.Write never returns an error
	return int(h.Sum32() % DDEVPortSlots)
}

// AllocateDDEVSlot returns the first slot not in held, probing forward from
// name's preferred slot and wrapping past the last slot to 0. The probe is
// what makes the hash usable: a plain hash over 1000 slots already collides
// about 4% of the time at ten VMs. held must already exclude the VM being
// allocated for, so a reset never collides with its own previous record.
//
// A full table is an error rather than a reused slot, which would put two
// VMs back on the same host ports this exists to separate.
func AllocateDDEVSlot(name string, held map[int]bool) (int, error) {
	pref := PreferredDDEVSlot(name)
	for i := range DDEVPortSlots {
		if s := (pref + i) % DDEVPortSlots; !held[s] {
			return s, nil
		}
	}
	return 0, fmt.Errorf("no free ddev port slot: all %d slots in use", DDEVPortSlots)
}

// validateDDEVPortSlot rejects a recorded slot whose ports would fall outside
// the reserved range — a hand-edited record or marker could otherwise put a
// VM's router on an arbitrary (or invalid) host port.
func (c CreateConfig) validateDDEVPortSlot() error {
	if c.DDEVPortSlot == nil {
		return nil
	}
	if s := *c.DDEVPortSlot; s < 0 || s >= DDEVPortSlots {
		return fmt.Errorf("ddev port slot %d is out of range: must be 0–%d", s, DDEVPortSlots-1)
	}
	return nil
}

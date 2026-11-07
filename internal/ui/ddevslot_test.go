package ui

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

func ddevSlot(n int) *int { return &n }

func slotString(p *int) string {
	if p == nil {
		return "none"
	}
	return fmt.Sprint(*p)
}

// kindFleet is singleFleet with the member's profile type chosen by the test,
// since the type is what decides whether a VM gets a ddev port slot.
func kindFleet(p provider.Provider, kind profiles.Type) provider.Fleet {
	f := singleFleet(p, registry.LocalScope)
	f[0].Profile.Type = kind
	return f
}

// TestTUIResetDDEVSlot drives the reset form end to end: openResetForm
// captures the recorded slot, buildConfig replays it, and it is what reaches
// the provider and is recorded again — even though another VM now holds the
// same slot, because moving a VM's ports would break its URLs. A VM recorded
// before slots existed is given one, clear of the slots its neighbours hold in
// the member's cached markers.
func TestTUIResetDDEVSlot(t *testing.T) {
	const name = "vm1"
	pref := vm.PreferredDDEVSlot(name)
	cases := []struct {
		name     string
		recorded *int
		want     int
	}{
		{"recorded slot is replayed, not re-probed", ddevSlot(pref), pref},
		{"unrecorded slot is allocated past a neighbour's", nil, (pref + 1) % vm.DDEVPortSlots},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateHostState(t)
			var received vm.CreateConfig
			p := &providerfake.Provider{
				ResetFunc: func(_ context.Context, c vm.CreateConfig, _ provision.ResetOptions, _ io.Writer) error {
					received = c
					return nil
				},
			}
			m := New(singleFleet(p, registry.LocalScope)).(model)
			m.members[0].provenance = map[string]provider.Provenance{
				"neighbour": {Config: vm.CreateConfig{Name: "neighbour", WithDDEV: true, DDEVPortSlot: ddevSlot(pref)}},
			}
			cfg := resetVMConfig()
			cfg.WithDDEV = true
			cfg.DDEVPortSlot = tc.recorded
			m.openResetForm(registry.LocalScope, name, cfg)

			built, err := m.buildConfig()
			if err != nil {
				t.Fatal(err)
			}
			if slotString(built.DDEVPortSlot) != slotString(tc.recorded) {
				t.Fatalf("buildConfig slot = %s, want the recorded %s", slotString(built.DDEVPortSlot), slotString(tc.recorded))
			}

			loop := newTeaLoop(t, m)
			loop.send(ctrlKey('s'))
			if loop.m.formErr != nil {
				t.Fatal(loop.m.formErr)
			}
			loop.pump("reset completion", func(m model) bool {
				job, ok := m.jobs.snapshot(provisionKey(registry.LocalScope, name))
				return ok && !job.Running()
			})
			if received.DDEVPortSlot == nil || *received.DDEVPortSlot != tc.want {
				t.Fatalf("provider received slot %s, want %d", slotString(received.DDEVPortSlot), tc.want)
			}
			rec, _ := loop.m.reg.ConfigInScope(name, registry.LocalScope)
			if rec.DDEVPortSlot == nil || *rec.DDEVPortSlot != tc.want {
				t.Errorf("registry recorded slot %s, want %d", slotString(rec.DDEVPortSlot), tc.want)
			}
		})
	}
}

// TestTUIResetFormDoesNotLeakASlot: the model is reused from form to form, so
// a slot captured for one reset must not ride into the next one's config.
func TestTUIResetFormDoesNotLeakASlot(t *testing.T) {
	m := newTestModel(t)
	withSlot := resetVMConfig()
	withSlot.DDEVPortSlot = ddevSlot(5)
	m.openResetForm(registry.LocalScope, withSlot.Name, withSlot)
	m.openResetForm(registry.LocalScope, "vm2", vm.CreateConfig{Name: "vm2", BaseName: "sandbar-base", CPUs: 2, Memory: "8GiB", Disk: "100GiB"})
	built, err := m.buildConfig()
	if err != nil {
		t.Fatal(err)
	}
	if built.DDEVPortSlot != nil {
		t.Fatalf("vm2's reset inherited vm1's slot %d", *built.DDEVPortSlot)
	}
}

// TestTUICreateDDEVSlot drives the create form to completion: a Lima create
// reaches the provider with a slot and records it; a Proxmox create, whose
// guest has its own address, gets none.
func TestTUICreateDDEVSlot(t *testing.T) {
	const name = "fresh"
	cases := []struct {
		kind profiles.Type
		want *int
	}{
		{profiles.TypeLocal, ddevSlot(vm.PreferredDDEVSlot(name))},
		{profiles.TypeRemoteSSH, ddevSlot(vm.PreferredDDEVSlot(name))},
		{profiles.TypeProxmox, nil},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			isolateHostState(t)
			var received vm.CreateConfig
			p := &providerfake.Provider{
				CreateFunc: func(_ context.Context, c vm.CreateConfig, _ provision.CreateOptions, _ io.Writer) error {
					received = c
					return nil
				},
			}
			m := New(kindFleet(p, tc.kind)).(model)
			fillCreateForm(t, &m, name)

			loop := newTeaLoop(t, m)
			loop.send(ctrlKey('s'))
			if loop.m.formErr != nil {
				t.Fatal(loop.m.formErr)
			}
			loop.pump("create completion", func(m model) bool {
				job, ok := m.jobs.snapshot(provisionKey(registry.LocalScope, name))
				return ok && !job.Running()
			})
			if slotString(received.DDEVPortSlot) != slotString(tc.want) {
				t.Fatalf("provider received slot %s, want %s", slotString(received.DDEVPortSlot), slotString(tc.want))
			}
			rec, ok := loop.m.reg.ConfigInScope(name, registry.LocalScope)
			if !ok {
				t.Fatal("create was not recorded")
			}
			if slotString(rec.DDEVPortSlot) != slotString(tc.want) {
				t.Errorf("registry recorded slot %s, want %s", slotString(rec.DDEVPortSlot), slotString(tc.want))
			}
		})
	}
}

// TestTUICreateAvoidsSlotOfABuildInFlight: two creates submitted back to back
// must not share a slot. The first is neither in the registry (recorded only
// on success) nor yet in the cached markers (its in-flight marker appears at
// clone time and reaches the board on a later refresh), so the only record of
// its slot is its own job.
func TestTUICreateAvoidsSlotOfABuildInFlight(t *testing.T) {
	isolateHostState(t)
	const first = "fresh"
	pref := vm.PreferredDDEVSlot(first)
	second := ""
	for i := 0; second == ""; i++ {
		if n := fmt.Sprintf("other-%d", i); vm.PreferredDDEVSlot(n) == pref {
			second = n
		}
	}
	m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)
	fillCreateForm(t, &m, first)
	next, _ := m.submitForm() // the run is never executed: the build stays in flight
	m = next.(model)
	if m.formErr != nil {
		t.Fatal(m.formErr)
	}
	fillCreateForm(t, &m, second)
	next, _ = m.submitForm()
	m = next.(model)
	if m.formErr != nil {
		t.Fatal(m.formErr)
	}
	a, _ := m.jobs.config(registry.LocalScope, first)
	b, _ := m.jobs.config(registry.LocalScope, second)
	if a.DDEVPortSlot == nil || b.DDEVPortSlot == nil || *a.DDEVPortSlot == *b.DDEVPortSlot {
		t.Fatalf("back-to-back creates got slots %s and %s; want two different slots", slotString(a.DDEVPortSlot), slotString(b.DDEVPortSlot))
	}
}

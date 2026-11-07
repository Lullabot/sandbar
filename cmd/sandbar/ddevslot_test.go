package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
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

// slotMarkers is a Provenancer whose whole-fleet read returns markers (or err).
// ProvenanceOf reports nothing, so a recreate falls back to the registry for
// the VM's own record — the marker map only stands in for OTHER VMs here.
type slotMarkers struct {
	*providerfake.Provider
	markers map[string]provider.Provenance
	err     error
	reads   int
}

func (p *slotMarkers) Provenance(context.Context) (map[string]provider.Provenance, error) {
	p.reads++
	return p.markers, p.err
}
func (p *slotMarkers) ProvenanceOf(context.Context, string) (provider.Provenance, bool, error) {
	return provider.Provenance{}, false, nil
}
func (p *slotMarkers) MarkManaged(context.Context, string, provider.Provenance) error { return nil }
func (p *slotMarkers) Unmark(context.Context, string) error                           { return nil }

func bindCreateKind(p provider.Provider, kind profiles.Type) createBinder {
	return func(store *profiles.Store, name string) (provider.Provider, registry.Scope, profiles.Profile, error) {
		profile, err := resolveProfileName(store, name)
		profile.Type = kind
		return p, registry.LocalScope, profile, err
	}
}

// TestRecordedDDEVSlotSurvivesEveryCLIResetPath: a VM's ddev ports are part of
// its identity, like its name and repo. Every CLI path that rebuilds a VM
// starts from DefaultCreateConfig and copies the record field by field, so a
// field it forgets is silently dropped — and a dropped slot means the next
// finalize hands the VM a different port block, breaking its bookmarks.
func TestRecordedDDEVSlotSurvivesEveryCLIResetPath(t *testing.T) {
	rec := recordedVM()
	rec.WithDDEV = true
	rec.DDEVPortSlot = ddevSlot(42)

	cases := map[string]func(t *testing.T) vm.CreateConfig{
		"reset (resetConfigFromRecord)": func(t *testing.T) vm.CreateConfig {
			cfg, err := resetConfigFromRecord(rec, true, rec.Name, rec.BaseName, map[string]bool{}, resetFlagValues{}, "hostuser")
			if err != nil {
				t.Fatal(err)
			}
			return cfg
		},
		"create --recreate (adoptRecordedConfig)": func(t *testing.T) vm.CreateConfig {
			cfg := vm.DefaultCreateConfig()
			cfg.Name = rec.Name
			adoptRecordedConfig(&cfg, rec, map[string]bool{})
			return cfg
		},
		"reset --from-backup (reset.json)": func(t *testing.T) vm.CreateConfig {
			dir := backupDir(t, &provision.ResetManifest{Schema: 1, Config: rec, Plan: provision.PreservePlan{WholeHome: true}}, "home.tar")
			o, explicit := resumeFlags(dir)
			cfg, _, err := resumeReset(registry.NewEmpty(), rec.Name, registry.LocalScope, o, explicit, "ada", nil)
			if err != nil {
				t.Fatal(err)
			}
			return cfg
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := build(t)
			if cfg.DDEVPortSlot == nil || *cfg.DDEVPortSlot != 42 {
				t.Fatalf("slot = %s, want the recorded 42", slotString(cfg.DDEVPortSlot))
			}
			if cfg.DDEVPortSlot == rec.DDEVPortSlot {
				t.Error("the rebuilt config aliases the record's slot pointer; it must hold its own copy")
			}
			// Even with the recorded slot now claimed by another VM, a recorded
			// slot is kept: moving it would break the VM's URLs.
			reg := registry.NewEmpty()
			if err := reg.AddScoped(vm.CreateConfig{Name: "other", WithDDEV: true, DDEVPortSlot: ddevSlot(42)}, registry.LocalScope); err != nil {
				t.Fatal(err)
			}
			if err := assignDDEVSlot(context.Background(), &cfg, profiles.TypeLocal, nil, reg, registry.LocalScope); err != nil {
				t.Fatal(err)
			}
			if *cfg.DDEVPortSlot != 42 {
				t.Errorf("a recorded slot was re-probed to %d", *cfg.DDEVPortSlot)
			}
		})
	}
}

// TestCLICreateAssignsDDEVSlotBeforeProvisioning drives `sandbar create`
// end to end over a fake provider: an eligible VM reaches the provider WITH
// its slot (so the in-flight marker written at clone time already holds it),
// the slot avoids every slot another VM in the profile holds, and it is what
// gets recorded. A Proxmox guest has its own address and gets none.
func TestCLICreateAssignsDDEVSlotBeforeProvisioning(t *testing.T) {
	const name = "fresh"
	pref := vm.PreferredDDEVSlot(name)
	cases := []struct {
		name     string
		kind     profiles.Type
		markers  map[string]provider.Provenance
		readErr  error
		registry []vm.CreateConfig
		recreate *vm.CreateConfig
		want     *int
	}{
		{
			name: "local probes past a slot held by another controller's VM",
			kind: profiles.TypeLocal,
			markers: map[string]provider.Provenance{
				"elsewhere": {Config: vm.CreateConfig{Name: "elsewhere", WithDDEV: true, DDEVPortSlot: ddevSlot(pref)}},
			},
			want: ddevSlot((pref + 1) % vm.DDEVPortSlots),
		},
		{
			name:     "remote falls back to the registry when markers cannot be read",
			kind:     profiles.TypeRemoteSSH,
			readErr:  errors.New("ssh: connection refused"),
			registry: []vm.CreateConfig{{Name: "cached", BaseName: "sandbar-base", WithDDEV: true, DDEVPortSlot: ddevSlot(pref)}},
			want:     ddevSlot((pref + 1) % vm.DDEVPortSlots),
		},
		{
			name: "proxmox gets no slot",
			kind: profiles.TypeProxmox,
			want: nil,
		},
		{
			name: "recreate keeps the recorded slot",
			kind: profiles.TypeLocal,
			markers: map[string]provider.Provenance{
				"elsewhere": {Config: vm.CreateConfig{Name: "elsewhere", WithDDEV: true, DDEVPortSlot: ddevSlot(7)}},
			},
			recreate: &vm.CreateConfig{Name: name, BaseName: "sandbar-base", User: "tester", GitName: "T", GitEmail: "t@example.com",
				CPUs: 2, Memory: "8GiB", Disk: "100GiB", WithDDEV: true, DDEVPortSlot: ddevSlot(7)},
			want: ddevSlot(7),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateCreateState(t)
			reg, err := registry.Load()
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range tc.registry {
				if err := reg.AddScoped(c, registry.LocalScope); err != nil {
					t.Fatal(err)
				}
			}
			if tc.recreate != nil {
				if err := reg.AddScoped(*tc.recreate, registry.LocalScope); err != nil {
					t.Fatal(err)
				}
			}
			var received vm.CreateConfig
			got := func(_ context.Context, cfg vm.CreateConfig, _ provision.CreateOptions, _ io.Writer) error {
				received = cfg
				return nil
			}
			// Every recorded VM is live, or create's reconcile would prune it
			// from the registry before the slot is allocated.
			var live []vm.VM
			for _, c := range tc.registry {
				live = append(live, vm.VM{Name: c.Name})
			}
			if tc.recreate != nil {
				live = append(live, vm.VM{Name: name})
			}
			fake := &providerfake.Provider{
				HostFilesFunc: lima.LocalFiles,
				ListFunc:      func() ([]vm.VM, error) { return live, nil },
				CreateFunc:    got,
				RecreateFunc:  got,
			}
			p := &slotMarkers{Provider: fake, markers: tc.markers, err: tc.readErr}
			args := createAgentArgs(name)
			if tc.recreate != nil {
				args = append(args, "--recreate")
			}
			if err := runCreateWithBinding(args, bindCreateKind(p, tc.kind)); err != nil {
				t.Fatal(err)
			}
			if slotString(received.DDEVPortSlot) != slotString(tc.want) {
				t.Fatalf("provider received slot %s, want %s", slotString(received.DDEVPortSlot), slotString(tc.want))
			}
			after, err := registry.Load()
			if err != nil {
				t.Fatal(err)
			}
			rec, _ := after.ConfigInScope(name, registry.LocalScope)
			if slotString(rec.DDEVPortSlot) != slotString(tc.want) {
				t.Errorf("registry recorded slot %s, want %s", slotString(rec.DDEVPortSlot), slotString(tc.want))
			}
			if tc.want == nil && p.reads != 0 {
				t.Errorf("read provenance %d times for a VM that cannot get a slot", p.reads)
			}
		})
	}
}

// TestCLIResetAllocatesDDEVSlotForUnrecordedVM: a VM created before slots
// existed gets one on its next reset — one that does not collide with another
// VM in the same profile — and the reset reports it. A VM without ddev gets
// neither a slot nor a port line.
func TestCLIResetAllocatesDDEVSlotForUnrecordedVM(t *testing.T) {
	for _, withDDEV := range []bool{true, false} {
		t.Run(fmt.Sprintf("ddev=%v", withDDEV), func(t *testing.T) {
			isolateCreateState(t)
			rec := recordedVM()
			rec.WithDDEV = withDDEV
			pref := vm.PreferredDDEVSlot(rec.Name)
			reg := registry.NewEmpty()
			for _, c := range []vm.CreateConfig{rec, {Name: "neighbour", BaseName: "sandbar-base", WithDDEV: true, DDEVPortSlot: ddevSlot(pref)}} {
				if err := reg.AddScoped(c, registry.LocalScope); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := resetConfigFor(reg, rec.Name, registry.LocalScope, rec.BaseName, map[string]bool{}, resetFlagValues{}, "ada")
			if err != nil {
				t.Fatal(err)
			}
			if err := assignDDEVSlot(context.Background(), &cfg, profiles.TypeLocal, nil, reg, registry.LocalScope); err != nil {
				t.Fatal(err)
			}
			stub := &stubResetter{}
			var out bytes.Buffer
			if err := doReset(context.Background(), reg, stub, cfg, registry.LocalScope, provision.ResetOptions{}, &out); err != nil {
				t.Fatal(err)
			}
			recorded, _ := reg.ConfigInScope(rec.Name, registry.LocalScope)
			if !withDDEV {
				if stub.cfg.DDEVPortSlot != nil || recorded.DDEVPortSlot != nil {
					t.Fatalf("a VM without ddev got slot %s", slotString(stub.cfg.DDEVPortSlot))
				}
				if strings.Contains(out.String(), "ddev:") {
					t.Errorf("printed ddev ports for a VM without ddev:\n%s", out.String())
				}
				return
			}
			want := (pref + 1) % vm.DDEVPortSlots
			if stub.cfg.DDEVPortSlot == nil || *stub.cfg.DDEVPortSlot != want {
				t.Fatalf("reset reached the provider with slot %s, want %d (the neighbour holds %d)", slotString(stub.cfg.DDEVPortSlot), want, pref)
			}
			if recorded.DDEVPortSlot == nil || *recorded.DDEVPortSlot != want {
				t.Errorf("registry recorded slot %s, want %d", slotString(recorded.DDEVPortSlot), want)
			}
			ports := vm.DDEVPortsForSlot(want)
			line := fmt.Sprintf("ddev: http://<project>.ddev.site:%d  https://<project>.ddev.site:%d\n", ports.RouterHTTP, ports.RouterHTTPS)
			if !strings.Contains(out.String(), line) {
				t.Errorf("reset output missing %q:\n%s", line, out.String())
			}
		})
	}
}

// TestHeadlessCreatePrintsDDEVPorts: the router ports are reported at the
// moment the user needs them, and only for a VM that has them.
func TestHeadlessCreatePrintsDDEVPorts(t *testing.T) {
	cfg := vm.CreateConfig{Name: "claude", BaseName: "sandbar-base", GitName: "A", GitEmail: "a@b.c", CPUs: 2, WithDDEV: true, DDEVPortSlot: ddevSlot(0)}
	var out bytes.Buffer
	if err := doHeadlessCreate(context.Background(), registry.NewEmpty(), &stubProvisioner{}, cfg, registry.LocalScope, false, false, "", &out); err != nil {
		t.Fatal(err)
	}
	if want := "ddev: http://<project>.ddev.site:20000  https://<project>.ddev.site:20001\n"; !strings.Contains(out.String(), want) {
		t.Errorf("create output missing %q:\n%s", want, out.String())
	}

	cfg.DDEVPortSlot = nil
	out.Reset()
	if err := doHeadlessCreate(context.Background(), registry.NewEmpty(), &stubProvisioner{}, cfg, registry.LocalScope, false, false, "", &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "ddev:") {
		t.Errorf("printed ddev ports for a VM without a slot:\n%s", out.String())
	}
}

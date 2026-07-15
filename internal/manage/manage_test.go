package manage

import (
	"reflect"
	"sort"
	"testing"

	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// TestReconcile verifies the drift-guard drops managed entries whose VM is no
// longer present in the live `limactl list` result, and reports the names it
// dropped — the mechanism that stops a VM deleted outside sand from staying
// flagged managed (and recreate-able) forever.
func TestReconcile(t *testing.T) {
	reg := registry.NewEmpty()
	for _, name := range []string{"a", "b", "c"} {
		cfg := vm.CreateConfig{Name: name, BaseName: "sandbar-base"}
		if err := reg.Add(cfg); err != nil {
			t.Fatalf("seed registry with %q: %v", name, err)
		}
	}

	live := []vm.VM{{Name: "a"}, {Name: "c"}}

	dropped, err := Reconcile(reg, live)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	sort.Strings(dropped)
	if !reflect.DeepEqual(dropped, []string{"b"}) {
		t.Fatalf("dropped = %v, want [b]", dropped)
	}

	if reg.IsManaged("b") {
		t.Fatal("Reconcile left \"b\" managed after it was absent from live")
	}
	if !reg.IsManaged("a") || !reg.IsManaged("c") {
		t.Fatalf("Reconcile dropped a live VM: a managed=%v c managed=%v", reg.IsManaged("a"), reg.IsManaged("c"))
	}
}

// TestReconcile_NoneDropped confirms a live list matching the registry
// exactly leaves it untouched and reports no drops.
func TestReconcile_NoneDropped(t *testing.T) {
	reg := registry.NewEmpty()
	cfg := vm.CreateConfig{Name: "claude", BaseName: "sandbar-base"}
	if err := reg.Add(cfg); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	dropped, err := Reconcile(reg, []vm.VM{{Name: "claude"}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if dropped != nil {
		t.Fatalf("dropped = %v, want nil", dropped)
	}
	if !reg.IsManaged("claude") {
		t.Fatal("Reconcile dropped a VM still present in live")
	}
}

// TestRecordSuccess verifies a successful create/recreate is recorded as
// managed with its CreateConfig retrievable from the registry — the
// bookkeeping shared between the TUI and the headless `sand create` path.
func TestRecordSuccess(t *testing.T) {
	reg := registry.NewEmpty()
	cfg := vm.CreateConfig{
		Name:     "claude",
		BaseName: "sandbar-base",
		GitName:  "Ada Lovelace",
		GitEmail: "ada@example.com",
		CPUs:     4,
		Memory:   "8GiB",
		Disk:     "100GiB",
	}

	if err := RecordSuccess(reg, cfg); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}

	if !reg.IsManaged(cfg.Name) {
		t.Fatalf("RecordSuccess did not mark %q managed", cfg.Name)
	}
	got, ok := reg.Config(cfg.Name)
	if !ok {
		t.Fatalf("registry has no config recorded for %q", cfg.Name)
	}
	if got != cfg {
		t.Fatalf("recorded config = %+v, want %+v", got, cfg)
	}
}

// TestRecreateBase covers the three-way gate that decides which VMs may be
// recreated: refused outright for a VM sand did not create, the recorded
// base for one it did, and the default base name when a managed entry
// predates recording one (e.g. an older index format).
func TestRecreateBase(t *testing.T) {
	t.Run("unmanaged VM is refused", func(t *testing.T) {
		reg := registry.NewEmpty()

		base, ok := RecreateBase(reg, "not-managed")
		if ok {
			t.Fatalf("RecreateBase(unmanaged) ok = true, want false (base=%q)", base)
		}
		if base != "" {
			t.Fatalf("RecreateBase(unmanaged) base = %q, want empty", base)
		}
	})

	t.Run("managed VM returns its recorded base", func(t *testing.T) {
		reg := registry.NewEmpty()
		cfg := vm.CreateConfig{Name: "claude", BaseName: "custom-base"}
		if err := reg.Add(cfg); err != nil {
			t.Fatalf("seed registry: %v", err)
		}

		base, ok := RecreateBase(reg, "claude")
		if !ok {
			t.Fatal("RecreateBase(managed) ok = false, want true")
		}
		if base != "custom-base" {
			t.Fatalf("RecreateBase(managed) base = %q, want %q", base, "custom-base")
		}
	})

	t.Run("managed VM with no recorded base falls back to default", func(t *testing.T) {
		reg := registry.NewEmpty()
		cfg := vm.CreateConfig{Name: "claude", BaseName: ""}
		if err := reg.Add(cfg); err != nil {
			t.Fatalf("seed registry: %v", err)
		}

		base, ok := RecreateBase(reg, "claude")
		if !ok {
			t.Fatal("RecreateBase(managed, no base) ok = false, want true")
		}
		want := vm.DefaultCreateConfig().BaseName
		if base != want {
			t.Fatalf("RecreateBase(managed, no base) base = %q, want default %q", base, want)
		}
	})
}

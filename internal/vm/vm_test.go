package vm

import (
	"sort"
	"strings"
	"testing"
)

// validConfig returns a CreateConfig that passes Validate, so each test can
// mutate exactly the one field under test.
func validConfig() CreateConfig {
	c := DefaultCreateConfig()
	c.GitName = "Ada Lovelace"
	c.GitEmail = "ada@example.com"
	return c
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*CreateConfig)
		wantErr bool
	}{
		{
			name:    "valid config",
			mutate:  func(c *CreateConfig) {},
			wantErr: false,
		},
		{
			name:    "missing git name",
			mutate:  func(c *CreateConfig) { c.GitName = "" },
			wantErr: true,
		},
		{
			name:    "missing git email",
			mutate:  func(c *CreateConfig) { c.GitEmail = "" },
			wantErr: true,
		},
		{
			name:    "name equals base name",
			mutate:  func(c *CreateConfig) { c.Name = c.BaseName },
			wantErr: true,
		},
		{
			name:    "non-positive cpus",
			mutate:  func(c *CreateConfig) { c.CPUs = 0 },
			wantErr: true,
		},
		{
			// A clone can't shrink below the base floor, so a smaller disk must be
			// rejected up front rather than fail deep in a Proxmox create.
			name:    "disk below the base floor",
			mutate:  func(c *CreateConfig) { c.Disk = "10GiB" },
			wantErr: true,
		},
		{
			name:    "disk exactly at the floor",
			mutate:  func(c *CreateConfig) { c.Disk = BaseDiskFloor },
			wantErr: false,
		},
		{
			name:    "disk above the floor",
			mutate:  func(c *CreateConfig) { c.Disk = "200GiB" },
			wantErr: false,
		},
		{
			// An empty/unparseable disk is permissive — the provider decides.
			name:    "empty disk is not a floor violation",
			mutate:  func(c *CreateConfig) { c.Disk = "" },
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfig()
			tt.mutate(&c)
			err := c.Validate()
			if tt.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

// TestHostUserNeverEmpty locks the load-bearing invariant behind the headless
// `sand create` user default: HostUser must never return "". An empty user_name
// passed as an Ansible extra-var overrides the user role's default and breaks
// the base phase's in-guest user creation (the bug that broke the lima-e2e CI
// job when create sent an empty user). The fallback chain (id -un → $USER →
// "claude") guarantees a non-empty result in every environment.
func TestHostUserNeverEmpty(t *testing.T) {
	if u := HostUser(); u == "" {
		t.Fatal("HostUser() = \"\", want a non-empty default (id -un → $USER → \"claude\")")
	}
}

func TestDefaultCreateConfig(t *testing.T) {
	c := DefaultCreateConfig()
	if c.Name != "claude" {
		t.Errorf("Name = %q, want %q", c.Name, "claude")
	}
	if c.BaseName != "sandbar-base" {
		t.Errorf("BaseName = %q, want %q", c.BaseName, "sandbar-base")
	}
	if c.Memory != "8GiB" {
		t.Errorf("Memory = %q, want %q", c.Memory, "8GiB")
	}
	if c.Disk != "100GiB" {
		t.Errorf("Disk = %q, want %q", c.Disk, "100GiB")
	}
	if c.Domain != "lan" {
		t.Errorf("Domain = %q, want %q", c.Domain, "lan")
	}
	if c.Locale != "en_US.UTF-8" {
		t.Errorf("Locale = %q, want %q", c.Locale, "en_US.UTF-8")
	}
	if c.CPUs != 2 {
		t.Errorf("CPUs = %d, want %d", c.CPUs, 2)
	}
	if !c.WithClaude || !c.WithDDEV || !c.WithGo || !c.WithJava {
		t.Errorf("WithClaude/WithDDEV/WithGo/WithJava = %v/%v/%v/%v, want all true (backwards compatibility: an unconfigured `sand create` must install everything today's base does)", c.WithClaude, c.WithDDEV, c.WithGo, c.WithJava)
	}
	// Codex is the deliberate exception: opt-IN, so it must default false even
	// though the other four default true — an unconfigured `sand create` must
	// NOT start installing a tool no existing base has.
	if c.WithCodex {
		t.Errorf("WithCodex = true, want false (codex is opt-in, unlike the other four tools)")
	}
}

// TestToolsetKey_DefaultIsEveryTool locks the canonical rendering of the
// default (everything-on) selection, which baseversion.go's
// toolsetPlaceholder used to hardcode until this key replaced it.
func TestToolsetKey_DefaultIsEveryTool(t *testing.T) {
	c := DefaultCreateConfig()
	if got, want := c.ToolsetKey(), "claude+ddev+go+java"; got != want {
		t.Errorf("ToolsetKey() = %q, want %q", got, want)
	}
}

// TestToolsetKey_WithCodex proves codex slots alphabetically into the key
// when enabled (between claude and ddev), and — the load-bearing half of this
// test — that a default (codex-off) config still renders the exact
// byte-identical stamp `claude+ddev+go+java` that existed before codex was
// added. If the default key changed at all, every existing base would look
// stale against its own recorded stamp and needlessly re-converge.
func TestToolsetKey_WithCodex(t *testing.T) {
	c := DefaultCreateConfig()
	if got, want := c.ToolsetKey(), "claude+ddev+go+java"; got != want {
		t.Errorf("ToolsetKey() with codex omitted = %q, want %q (unchanged stamp for existing users)", got, want)
	}

	c.WithCodex = true
	if got, want := c.ToolsetKey(), "claude+codex+ddev+go+java"; got != want {
		t.Errorf("ToolsetKey() with codex enabled = %q, want %q", got, want)
	}
}

// TestApplyToolset_RoundTripsCodex proves ApplyToolset (how `sand create`
// adopts an existing base's recorded selection) correctly assigns codex both
// on and off, the same as any other tool.
func TestApplyToolset_RoundTripsCodex(t *testing.T) {
	var c CreateConfig
	c.ApplyToolset(map[string]bool{"claude": true, "codex": true, "ddev": true, "go": true, "java": true})
	if got, want := c.ToolsetKey(), "claude+codex+ddev+go+java"; got != want {
		t.Errorf("after ApplyToolset with codex=true, ToolsetKey() = %q, want %q", got, want)
	}
	if !c.WithCodex {
		t.Errorf("WithCodex = false after ApplyToolset with codex=true in the set")
	}

	// Names absent from the set are turned OFF, not left alone — assert codex
	// follows that same rule as the other four.
	c.ApplyToolset(map[string]bool{"claude": true})
	if c.WithCodex {
		t.Errorf("WithCodex = true after ApplyToolset with codex absent from the set, want false")
	}
}

// TestToolsetKey_IsSorted pins the rendering order to ALPHABETICAL, not
// declaration order. provision.toolsetKey rebuilds this same string from a
// parsed stamp (map iteration, so it sorts); if the two disagreed, a base would
// be perpetually stale against its own stamp and re-converge on every create.
// Appending a new tool to the end of ToolsetKey instead of its sorted position
// is exactly how that regression would arrive, so assert the invariant here.
func TestToolsetKey_IsSorted(t *testing.T) {
	got := DefaultCreateConfig().ToolsetKey()
	tools := strings.Split(got, "+")
	if !sort.StringsAreSorted(tools) {
		t.Errorf("ToolsetKey() = %q, whose tools are not in sorted order; provision.toolsetKey sorts, and the two renderings must agree exactly", got)
	}
}

// TestToolsetKey_OrderIndependent proves the key is order-independent: it is
// a rendering of a SET, not the order fields happened to be assigned in, so
// two configs built by assigning the same three booleans in different
// sequences still hash the base identically.
func TestToolsetKey_OrderIndependent(t *testing.T) {
	var a CreateConfig
	a.WithJava = true
	a.WithDDEV = true
	a.WithGo = true

	var b CreateConfig
	b.WithGo = true
	b.WithJava = true
	b.WithDDEV = true

	if a.ToolsetKey() != b.ToolsetKey() {
		t.Errorf("ToolsetKey() depended on assignment order: %q vs %q", a.ToolsetKey(), b.ToolsetKey())
	}
}

// TestToolsetKey_Empty is the shrink floor: deselecting everything must still
// render a stable, non-empty string ("none") rather than an empty one, since
// an empty string would collide with "no toolset info at all" when parsed
// back out of a stamp.
func TestToolsetKey_Empty(t *testing.T) {
	var c CreateConfig
	if got, want := c.ToolsetKey(), "none"; got != want {
		t.Errorf("ToolsetKey() = %q, want %q", got, want)
	}
}

// TestToolsetKey_PartialSelections pins the fixed field order (ddev, go,
// java) the key renders in regardless of which subset is on.
func TestToolsetKey_PartialSelections(t *testing.T) {
	tests := []struct {
		name            string
		ddev, goo, java bool
		want            string
	}{
		{"go only", false, true, false, "go"},
		{"java only", false, false, true, "java"},
		{"ddev+java", true, false, true, "ddev+java"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := CreateConfig{WithDDEV: tt.ddev, WithGo: tt.goo, WithJava: tt.java}
			if got := c.ToolsetKey(); got != tt.want {
				t.Errorf("ToolsetKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

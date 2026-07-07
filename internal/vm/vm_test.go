package vm

import "testing"

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
	if c.BaseName != "claude-base" {
		t.Errorf("BaseName = %q, want %q", c.BaseName, "claude-base")
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
}

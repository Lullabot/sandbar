package manage

import (
	"testing"

	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

type fakeConfigs map[string]vm.CreateConfig

func (f fakeConfigs) ConfigInScope(name string, _ registry.Scope) (vm.CreateConfig, bool) {
	cfg, ok := f[name]
	return cfg, ok
}

// TestRecordedCloneURL pins the precedence: a marker naming a repository
// wins, a marker without one falls through to the managed index, and
// neither knowing is "" (open the home directory).
func TestRecordedCloneURL(t *testing.T) {
	reg := fakeConfigs{"web": {CloneURL: "https://github.com/acme/from-index.git"}}
	marker := func(url string, found bool) func() (vm.CreateConfig, bool) {
		return func() (vm.CreateConfig, bool) { return vm.CreateConfig{CloneURL: url}, found }
	}
	cases := []struct {
		name   string
		marker func() (vm.CreateConfig, bool)
		reg    ConfigLookup
		vm     string
		want   string
	}{
		{"marker wins", marker("https://github.com/acme/from-marker.git", true), reg, "web", "https://github.com/acme/from-marker.git"},
		{"marker without url", marker("", true), reg, "web", "https://github.com/acme/from-index.git"},
		{"no marker", marker("ignored", false), reg, "web", "https://github.com/acme/from-index.git"},
		{"nil marker", nil, reg, "web", "https://github.com/acme/from-index.git"},
		{"neither", nil, reg, "other", ""},
		{"nil index", nil, nil, "web", ""},
	}
	for _, c := range cases {
		if got := RecordedCloneURL(c.marker, c.reg, registry.LocalScope, c.vm); got != c.want {
			t.Errorf("%s: RecordedCloneURL = %q, want %q", c.name, got, c.want)
		}
	}
}

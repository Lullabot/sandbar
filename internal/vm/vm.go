// Package vm defines the shared domain model for Claude Code development VMs:
// the VM record reported by Lima and the CreateConfig answers gathered when a
// new VM is provisioned. The lima, provision, and ui packages all consume it.
package vm

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// BaseDiskFloor is the virtual disk size the base image is built at. Clones are
// grown from this floor to their requested size; qcow2 can grow but not shrink
// live, so a small floor is what lets each VM pick (and effectively "shrink" to)
// any size >= the floor without rebuilding the base.
const BaseDiskFloor = "20GiB"

// VM is one Lima instance as reported by `limactl list`.
type VM struct {
	Name     string
	Status   string // Running | Stopped | ...
	CPUs     int
	Memory   string
	Disk     string // virtual/maximum size Lima reports (qcow2 max)
	DiskUsed string // allocated on-disk bytes (raw string); "" = unknown/unmeasurable
	Dir      string
	Arch     string

	// UpSince / LastUsed are the tile's closing line ("up 2h14m" / "last used 3d
	// ago"), sampled from the Lima instance dir's files. They are ENRICHMENTS, like
	// DiskUsed: `limactl list` does not report them, and they are filled in by the
	// list command — which runs OFF the Bubble Tea goroutine — precisely so the
	// blocking os.Stat behind them cannot run on the render path. The zero value
	// means "unknown", which the tile renders as an absence, never as a fabricated
	// time.
	UpSince  time.Time // when the current boot began; zero unless running
	LastUsed time.Time // when a stopped VM was last up; zero = never used
}

// CreateConfig mirrors the answers the original bash provisioner gathers.
type CreateConfig struct {
	Name            string
	BaseName        string
	Hostname        string
	User            string
	GitName         string
	GitEmail        string
	CPUs            int
	Memory          string
	Disk            string
	Locale          string
	Domain          string
	DockerProxyHost string
	CloneURL        string
	CloneToken      string

	// WithClaude, WithDDEV, WithGo, WithJava, and WithCodex select the
	// configurable base-image tool-set (sand create --with-claude/--with-ddev/
	// --with-go/--with-java/--with-codex). They configure the shared BASE
	// image, not the individual clone — there is still exactly one base per
	// user, its contents just differ by selection. The first four default to
	// true (see DefaultCreateConfig), so an unconfigured `sand create` installs
	// everything today's base does; those flags are opt-OUT. Claude Code is one
	// selection among the tools rather than a fixture of the image, so a user
	// can bring their own agent instead.
	//
	// WithCodex is the deliberate exception: it defaults to FALSE (opt-IN), so
	// existing users' bases keep the exact tool-set (and stamp) they already
	// have unless they explicitly ask for Codex too.
	WithClaude bool
	WithDDEV   bool
	WithGo     bool
	WithJava   bool
	WithCodex  bool
}

// DefaultCreateConfig returns the script's defaults (cpus left to caller/host).
func DefaultCreateConfig() CreateConfig {
	return CreateConfig{
		Name:       "claude",
		BaseName:   "sandbar-base",
		Memory:     "8GiB",
		Disk:       "100GiB",
		Domain:     "lan",
		Locale:     "en_US.UTF-8",
		CPUs:       2,
		WithClaude: true,
		WithDDEV:   true,
		WithGo:     true,
		WithJava:   true,
		// WithCodex is deliberately omitted: its zero value (false) IS the
		// default — codex is opt-in, unlike the four tools above.
	}
}

// ToolPtrs maps each tool's canonical name — the name that appears in the base
// image's version stamp, and in the --with-<name> flag — to the field holding
// its selection. It is the ONE place the tool names live: ToolsetKey renders
// from it, ApplyToolset assigns through it, and `sand create` adopts the base's
// recorded selection through it. Adding a tool means adding a field, a line
// here, and its flag; nothing else has to learn the name.
func (c *CreateConfig) ToolPtrs() map[string]*bool {
	return map[string]*bool{
		"claude": &c.WithClaude,
		"ddev":   &c.WithDDEV,
		"go":     &c.WithGo,
		"java":   &c.WithJava,
		"codex":  &c.WithCodex,
	}
}

// ApplyToolset overwrites the selection with the given set of enabled tool
// names — how a create adopts what an existing base was actually built with
// (provision.BaseToolset). Names absent from the set are set to FALSE, not left
// alone: the set is the whole answer, and a base that lacks a tool must produce
// a config that does not ask for it. Unknown names are ignored, so a stamp
// written by a NEWER sand that knows a tool this binary does not degrades to
// "not selected" rather than panicking.
func (c *CreateConfig) ApplyToolset(set map[string]bool) {
	for name, p := range c.ToolPtrs() {
		*p = set[name]
	}
}

// ToolsetKey renders the tool-set selection into a stable, order-independent
// string that feeds the base image's version stamp (see
// provision.PlaybookVersion): changing the selection changes this string,
// which changes the stamp, which marks the base stale so it converges (or
// rebuilds) instead of silently cloning a base with the wrong contents.
//
// The names are SORTED, so the same selection always renders identically no
// matter what order anything was assigned in — and, critically, identically to
// provision.toolsetKey, which rebuilds this same string from a parsed stamp and
// also sorts. The two renderings must agree exactly or a base would be
// perpetually stale against its own stamp and re-converge on every create.
// Sorting here (rather than relying on a hand-maintained field order that
// happens to be alphabetical) is what makes that true by construction.
//
// An empty selection renders as "none" rather than "" — an empty string would
// be indistinguishable from "no toolset information at all" when a stamp is
// parsed back apart (see provision.toolsetFromStamp).
func (c CreateConfig) ToolsetKey() string {
	var on []string
	for name, enabled := range c.ToolPtrs() {
		if *enabled {
			on = append(on, name)
		}
	}
	if len(on) == 0 {
		return "none"
	}
	sort.Strings(on)
	return strings.Join(on, "+")
}

// Validate enforces the same required-field and consistency rules as the
// original bash provisioner: a git identity is required, the instance name
// must differ from the base image name, and CPUs must be a positive integer.
func (c CreateConfig) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("instance name is required")
	}
	if c.GitName == "" {
		return fmt.Errorf("git user.name is required: pass --git-name or set it with `git config --global user.name \"...\"`")
	}
	if c.GitEmail == "" {
		return fmt.Errorf("git user.email is required: pass --git-email or set it with `git config --global user.email \"...\"`")
	}
	if c.Name == c.BaseName {
		return fmt.Errorf("instance name %q must differ from base image name %q", c.Name, c.BaseName)
	}
	if c.CPUs < 1 {
		return fmt.Errorf("cpus must be a positive integer (got %d)", c.CPUs)
	}
	return nil
}

// EffectiveHostname defaults to Name when unset; helper used by the form/provisioner.
func (c CreateConfig) EffectiveHostname() string {
	if c.Hostname != "" {
		return c.Hostname
	}
	return c.Name
}

// HostUser returns the primary VM user to default to when one is not given.
// Lima creates a guest user matching the host username, so mirror the original
// bash provisioner (`id -un`, falling back to $USER and then "claude"). It is
// deliberately never empty: an empty user_name passed to Ansible would override
// the user role's default and break in-guest user creation.
func HostUser() string {
	if out, err := exec.Command("id", "-un").Output(); err == nil {
		if u := strings.TrimSpace(string(out)); u != "" {
			return u
		}
	}
	if u := strings.TrimSpace(os.Getenv("USER")); u != "" {
		return u
	}
	return "claude"
}

// HostGitConfig reads a single value from the host git config, best-effort: any
// error (git missing, key unset) yields an empty string. Both the headless
// `sand create` path and the TUI form seed the git identity from here so
// --git-name/--git-email may be omitted when the host already has an identity;
// it is the single source of truth for that default (mirroring HostUser).
func HostGitConfig(key string) string {
	out, err := exec.Command("git", "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ParseCPUs validates the script's "cpus must be a positive integer" rule from a string field.
func ParseCPUs(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("cpus must be a positive integer (got %q)", s)
	}
	return n, nil
}

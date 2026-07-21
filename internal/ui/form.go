package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Form field indices. The slice order also drives tab/shift+tab focus movement.
const (
	fName = iota
	fHostname
	fUser
	fGitName
	fGitEmail
	fCPUs
	fMemory
	fDisk
	fDockerProxyHost
	fCloneURL
	fCloneToken
)

// fProfileSelector is a sentinel m.focusIdx value (never a real m.inputs
// index — every real one is >= 0) for the create form's profile selector,
// rendered on its own line above Name. It sits OUTSIDE the ordinary
// input <-> toggle ring: reached from fName by going backward (shift+tab / up)
// and returning to fName going forward (tab / down / enter), rather than
// being spliced into the ring's own wrap points. That is deliberate — the
// ring's existing wrap (last toggle -> fName, see formFocusNext) is exercised
// by TestCreateFormRebuildToggle, and a form freshly opened with 'n' (and a
// real user typing right after) must still land in — and type into — the
// Name field (teatest_test.go's golden-adjacent tests type immediately after
// 'n'), so the selector could not become the new first stop without breaking
// both. Reset mode never uses this value: a reset always targets its own VM's
// already-fixed member, so it has no selector to focus.
const fProfileSelector = -1

var fieldLabels = []string{
	"Name",
	"Hostname",
	"User",
	"Git name",
	"Git email",
	"CPUs",
	"Memory",
	"Disk",
	"Docker proxy host",
	"GitHub repo URL",
	"GitHub token",
}

// fieldInfo is the per-field help shown for the focused field. The GitHub token
// entry mirrors the original bash provisioner's github_token_help: where to
// create a fine-grained token and the recommended (deliberately limited)
// permissions.
var fieldInfo = []string{
	"Required. Lima instance name — also the VM you'll `limactl shell` into. Must differ from the base image.",
	"VM hostname inside the guest. Blank → same as the instance name.",
	"Primary VM user. Blank → your host username (Lima creates a matching user).",
	"Required. git user.name written into the VM's git config.",
	"Required. git user.email written into the VM's git config.",
	"vCPUs for the VM. Blank → half your host's cores (minimum 2).",
	"RAM for the VM, e.g. 8GiB. Blank → 8GiB, or half your host's RAM if that's less.",
	"Disk size for the VM, e.g. 100GiB. Blank → 100GiB.",
	"Optional. Docker registry pull-through proxy host. Blank to skip.",
	"Optional. HTTPS repo to clone into the VM now (GitHub-oriented). Blank to skip.",
	"Optional. Token for a private GitHub repo (blank = public / set up later).\n" +
		"Create a fine-grained token scoped to the repo at:\n" +
		"  https://github.com/settings/personal-access-tokens/new\n" +
		"Recommended permissions (PRs/Issues stay read-only so the agent can't\n" +
		"self-merge to main without human review):\n" +
		"  Actions: Read and write    Contents: Read and write\n" +
		"  Issues: Read    Pull requests: Read    Workflows: Read and write",
}

// hostGit seeds a git-identity field from the host git config. The headless
// `sand create` path seeds the same way, so both share vm.HostGitConfig as the
// single source of truth (mirroring hostUser/vm.HostUser below).
func hostGit(key string) string { return vm.HostGitConfig(key) }

// hostUser defaults the primary VM user to the host username (Lima creates a
// matching guest user). The headless `sand create` path defaults the same way,
// so both share vm.HostUser as the single source of truth.
func hostUser() string { return vm.HostUser() }

// defaultCPUs mirrors the original bash provisioner's default_cpus(): half the
// host's logical CPUs, with a floor of 2. hostCPUs is the core count of the host
// the VM will actually run on — the REMOTE host for a remote provider (sampled
// via Provider.HostResources) — so the suggestion scales to that machine, not the
// laptop driving it. A non-positive hostCPUs (unknown, or local Lima) falls back
// to THIS machine's count.
func defaultCPUs(hostCPUs int) int {
	if hostCPUs <= 0 {
		hostCPUs = runtime.NumCPU()
	}
	if n := hostCPUs / 2; n >= 2 {
		return n
	}
	return 2
}

// memCapBytes is the RAM ceiling a VM defaults to: 8GiB, unless that would take
// more than half the host's RAM.
const memCapBytes = 8 << 30

// defaultMemory is the blank-field RAM default: 8GiB capped at half the host's
// RAM so a small host isn't over-committed (a 16GiB+ host still gets 8GiB).
// hostMem is the total RAM of the host the VM will run on — the REMOTE host for a
// remote provider — so the cap reflects that machine. A non-positive hostMem
// (unknown, or local Lima) falls back to probing THIS machine.
func defaultMemory(hostMem int64) string {
	if hostMem <= 0 {
		hostMem = hostMemBytes()
	}
	return cappedMemoryGiB(hostMem, memCapBytes)
}

// cappedMemoryGiB returns min(capBytes, half of total) rounded to the nearest
// whole GiB as a Lima size string. total <= 0 (unknown) yields the cap; the
// result is floored at 1GiB so a tiny host still gets a usable value.
func cappedMemoryGiB(total, capBytes int64) string {
	limit := capBytes
	if total > 0 {
		if half := total / 2; half < limit {
			limit = half
		}
	}
	const gib = 1 << 30
	g := (limit + gib/2) / gib // round to the nearest GiB
	if g < 1 {
		g = 1
	}
	return strconv.FormatInt(g, 10) + "GiB"
}

// limaHomeDir is where Lima keeps its instance images: $LIMA_HOME if set, else
// ~/.lima. Empty when the home directory can't be resolved.
func limaHomeDir() string {
	if h := strings.TrimSpace(os.Getenv("LIMA_HOME")); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".lima")
}

// limaStorageDir resolves the directory whose filesystem freeDiskBytes and
// totalDiskBytes probe: Lima's home, climbing to the nearest existing
// ancestor — ~/.lima may not exist yet on a fresh host, and the nearest
// existing ancestor is the same filesystem the new VM's disk will land on.
// "" when it cannot be resolved at all (no home directory), which both
// callers turn into their "unknown" zero rather than statting a wrong path.
func limaStorageDir() string {
	dir := limaHomeDir()
	if dir == "" {
		return ""
	}
	for {
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir { // reached the root without an existing dir
			return ""
		}
		dir = parent
	}
}

// freeDiskBytes reports the free space on the volume backing Lima's instance
// store, best-effort (0 = unknown, so callers don't warn).
func freeDiskBytes() int64 {
	dir := limaStorageDir()
	if dir == "" {
		return 0
	}
	return hostDiskFreeBytes(dir)
}

// totalDiskBytes is freeDiskBytes' total-side companion: the TOTAL (not free)
// size of the same volume, resolved via the identical directory climb, so a
// host-disk low-capacity warning (hostwarn.go) can compute a free% for the
// local host without a second, differently-resolved path.
func totalDiskBytes() int64 {
	dir := limaStorageDir()
	if dir == "" {
		return 0
	}
	return hostDiskTotalBytes(dir)
}

// orDefault returns v when it has content, else def — the Go analogue of the
// script's `: "${VAR:=default}"`.
func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// newInputs builds the form's text inputs, seeded from DefaultCreateConfig and
// the host git identity. The clone token is masked.
// hostCPUs / hostMem describe the host the VM will run on (the remote host for a
// remote provider, 0 for local Lima or "not sampled yet"), so the CPU and memory
// suggestions scale to that machine — see defaultCPUs / defaultMemory.
func newInputs(hostCPUs int, hostMem int64, user string) []textinput.Model {
	def := vm.DefaultCreateConfig()
	seeds := []string{
		"",                                  // fName      (required; no default — user must name it)
		"",                                  // fHostname  (defaults to the instance name at submit)
		orDefault(user, hostUser()),         // fUser      (limactl host user; local fallback)
		hostGit("user.name"),                // fGitName
		hostGit("user.email"),               // fGitEmail
		strconv.Itoa(defaultCPUs(hostCPUs)), // fCPUs      (half the host cores, floor 2)
		defaultMemory(hostMem),              // fMemory    (8GiB, capped at half host RAM)
		def.Disk,                            // fDisk
		"",                                  // fDockerProxyHost
		"",                                  // fCloneURL
		"",                                  // fCloneToken
	}

	inputs := make([]textinput.Model, len(fieldLabels))
	for i := range inputs {
		ti := textinput.New()
		ti.CharLimit = 256
		ti.SetWidth(44)
		ti.SetValue(seeds[i])
		if i == fCloneToken {
			ti.EchoMode = textinput.EchoPassword
		}
		inputs[i] = ti
	}
	return inputs
}

// openForm initialises the create form and focuses the first field, returning
// the cursor-blink command batched with the async tool-set read (see
// kickFormToolsetLoad) — the form must render and accept keys THE INSTANT it
// opens, never stall behind either one.
func (m *model) openForm() tea.Cmd {
	// A NEW VM targets the profile selector's default pick — last-used, else
	// Local (setDefaultFormProfile). Its host sample supplies the cpu/memory/user
	// defaults; its provider seeds the toggles.
	m.setDefaultFormProfile()
	hs := m.formHostSample()
	m.inputs = newInputs(hs.cpus, hs.mem, hs.user)
	m.focusIdx = 0
	m.formErr = nil
	m.hostDiskFree = freeDiskBytes()
	m.resetMode = false // a create form is never in reset mode (even after a reset)
	m.toggleFocus = -1  // openResetForm already did this; create mode now has toggles too
	// The tool toggles START at the all-on default and are corrected
	// asynchronously (kickFormToolsetLoad) once the shared base's recorded
	// tool-set stamp comes back, via toolsetLoadedMsg (model.go). Reading it
	// HERE, synchronously, used to be a blocking ssh round trip whenever the
	// form's target profile is remote — the whole TUI froze behind a slow or
	// dead host. One frame showing the default before the real stamp lands is a
	// fair price for a form that never blocks the keyboard.
	cfg := vm.DefaultCreateConfig()
	m.toolClaude = cfg.WithClaude
	m.toolCodex = cfg.WithCodex
	m.toolDDEV = cfg.WithDDEV
	m.toolGo = cfg.WithGo
	m.toolJava = cfg.WithJava
	m.toolRebuild = false
	m.view = viewForm
	return tea.Batch(m.inputs[0].Focus(), m.kickFormToolsetLoad())
}

// toolsetLoadedMsg carries the SHARED base image's recorded tool-set, read
// off the Update goroutine by formToolsetCmd because the read goes through
// the form's TARGET member's HostFiles — a blocking ssh round trip for a
// remote profile (provision.BaseToolset ultimately calls HostFiles.ReadFile).
// scope is the formScope the read was kicked FOR, so a result that arrives
// after the user has closed the form, switched to reset mode, or cycled the
// profile selector on to a different target can be told apart from one still
// relevant — and ignored (see the handler in model.go's dispatch).
type toolsetLoadedMsg struct {
	scope   registry.Scope
	toolset map[string]bool
	ok      bool
}

// formToolsetCmd reads the shared base image's recorded tool-set stamp
// through hf (via provision.BaseToolset) OFF the Update goroutine. hf and
// baseName are captured by VALUE, exactly like every other tea.Cmd closure in
// this file (submitForm's run closure, refreshCmd's provider/hostFiles) — the
// closure must never read the mutable model, which the Update goroutine can
// go on mutating while this runs.
func formToolsetCmd(scope registry.Scope, hf lima.HostFiles, baseName string) tea.Cmd {
	return func() tea.Msg {
		base, ok := provision.BaseToolset(hf, baseName)
		return toolsetLoadedMsg{scope: scope, toolset: base, ok: ok}
	}
}

// kickFormToolsetLoad fires the async read of the shared base image's
// recorded tool-set stamp for the form's CURRENT target (m.formScope) — see
// formToolsetCmd. Called from openForm (the initially-selected profile) and
// again from cycleFormProfile every time the user picks a different one, so
// the toggles always converge on the newly-selected profile's own base,
// never a stale read left over from whichever profile was selected before.
// The active member can, in a degenerate fleet, be an error binding with no
// provider; there is then nothing to read, and the toggles simply keep
// whatever they already show (the all-on default, from openForm).
func (m *model) kickFormToolsetLoad() tea.Cmd {
	p := m.formProvider()
	if p == nil {
		return nil
	}
	return formToolsetCmd(m.formScope, p.HostFiles(), vm.DefaultCreateConfig().BaseName)
}

// openResetForm initialises the create form in reset mode, pre-filled from the
// target VM's recorded config. The Name is locked to the VM being reset, so focus
// starts on the first editable field (Hostname); the clone token is never stored,
// so it is left blank to be re-supplied for a private repo.
func (m *model) openResetForm(scope registry.Scope, name string, cfg vm.CreateConfig) tea.Cmd {
	// A reset targets the VM's OWN member (scope), not the active one — its host
	// sample, provider and bookkeeping all resolve through m.formScope.
	m.formScope = scope
	hs := m.formHostSample()
	m.inputs = newInputs(hs.cpus, hs.mem, hs.user)
	m.inputs[fName].SetValue(cfg.Name)
	m.inputs[fHostname].SetValue(cfg.Hostname)
	m.inputs[fUser].SetValue(cfg.User)
	m.inputs[fGitName].SetValue(cfg.GitName)
	m.inputs[fGitEmail].SetValue(cfg.GitEmail)
	m.inputs[fCPUs].SetValue(strconv.Itoa(cfg.CPUs))
	m.inputs[fMemory].SetValue(cfg.Memory)
	m.inputs[fDisk].SetValue(cfg.Disk)
	m.inputs[fDockerProxyHost].SetValue(cfg.DockerProxyHost)
	m.inputs[fCloneURL].SetValue(cfg.CloneURL)

	// The token itself is never stored in the recorded config, so the field seeds
	// blank. When the VM already has a saved GH_TOKEN secret, an empty box is
	// confusing ("is there no token?"); a placeholder makes clear that blank keeps
	// the saved token and typing replaces it (submitReset only overwrites the
	// secret when the field is non-empty).
	if m.hasStoredToken(scope, cfg.Name) {
		m.inputs[fCloneToken].Placeholder = "*** saved — leave blank to keep it"
	}

	m.hostDiskFree = freeDiskBytes()
	m.resetMode = true
	m.resetName = cfg.Name
	m.resetBaseName = cfg.BaseName
	m.resetWithClaude = cfg.WithClaude
	m.resetWithCodex = cfg.WithCodex
	m.resetWithDDEV = cfg.WithDDEV
	m.resetWithGo = cfg.WithGo
	m.resetWithJava = cfg.WithJava
	m.preserveClaude = false
	m.preserveProject = false
	orgRel, ok := provision.OrgRelDir(cfg.CloneURL)
	m.projectToggleEnabled = ok // no clone, or no org segment => nothing to preserve
	m.projectToggleLabel = ""
	if ok {
		m.projectToggleLabel = "Preserve ~/" + orgRel
	}
	m.toggleFocus = -1
	m.formErr = nil
	m.view = viewForm
	m.focusIdx = fHostname
	return m.inputs[fHostname].Focus()
}

// hasStoredToken reports whether the VM already has a GH_TOKEN secret in any
// scope (global or directory-scoped). The reset form uses it to decide whether
// to hint — via the token field's placeholder — that a saved token exists.
func (m model) hasStoredToken(scope registry.Scope, name string) bool {
	for _, pairs := range m.sec.GetAll(name, scope) {
		if _, ok := pairs["GH_TOKEN"]; ok {
			return true
		}
	}
	return false
}

// formProfiles returns the ENABLED profiles the create form's selector offers
// to choose among, in the profiles store's stable (insertion) order. A
// disabled profile is never offered here — even though its member can still
// linger in m.members (disableProfile keeps a disabled member around,
// dormant, so the header can still name it) — because it is not a place a new
// VM could ever actually be provisioned.
func (m model) formProfiles() []profiles.Profile {
	var out []profiles.Profile
	for _, p := range m.profileList() {
		if p.Enabled {
			out = append(out, p)
		}
	}
	return out
}

// indexOfProfileID returns the index of the profile with the given id in
// list, or -1 if absent (including id == "", so an unset last-used pointer
// never matches by accident).
func indexOfProfileID(list []profiles.Profile, id string) int {
	if id == "" {
		return -1
	}
	for i, p := range list {
		if p.ID == id {
			return i
		}
	}
	return -1
}

// retargetFormScope points formScope at profile p's live member — the
// provider, host sample and job/registry bookkeeping the rest of the form
// resolves through (formProvider, formHostSample, beginJob) all follow it. A
// profile with no live member (should not happen for anything formProfiles
// offers, but guarded rather than assumed) leaves formScope untouched.
func (m *model) retargetFormScope(p profiles.Profile) {
	if mem, ok := m.memberIndexByProfileIDValue(p.ID); ok {
		m.formScope = mem.scope
	}
}

// setDefaultFormProfile picks the create form's INITIAL profile selection —
// the last-used profile (by id, from the store), falling back to Local, and
// finally to whatever is first in the enabled list — and points formScope at
// it. Called from openForm BEFORE m.inputs exists: it touches formProfileIdx
// and formScope only, never an input field (newInputs, right after, seeds
// cpu/memory/user from the resulting formHostSample — see openForm).
func (m *model) setDefaultFormProfile() {
	list := m.formProfiles()
	if len(list) == 0 {
		// Nothing enabled to select (a degenerate store) — fall back exactly as
		// sand did before this field existed.
		m.formProfileIdx = 0
		m.formScope = m.activeScope()
		return
	}
	wantID := ""
	if m.profileStore != nil {
		wantID = m.profileStore.LastUsed()
	}
	idx := indexOfProfileID(list, wantID)
	if idx < 0 {
		// No last-used, or it points at a profile since disabled/deleted: Local.
		idx = indexOfProfileID(list, profiles.LocalProfileID)
	}
	if idx < 0 {
		idx = 0
	}
	m.formProfileIdx = idx
	m.retargetFormScope(list[idx])
}

// cycleFormProfile moves the profile selector by delta (+/-1, wrapping) among
// the enabled profiles, retargets formScope at the newly selected one, and
// re-seeds the cpu/memory/user fields from ITS host sample — so switching
// profiles mid-form immediately shows suggestions scaled to the new host
// rather than stale ones seeded (by newInputs, at open) for the old one. A
// field the user has since typed over loses that edit; the selector is meant
// to be picked before fine-tuning these, exactly as openForm itself seeds them
// fresh every time the form opens.
//
// It also RE-KICKS the async tool-set read (kickFormToolsetLoad) for the
// newly selected profile — a stale in-flight read for whichever profile was
// selected before is ignored by scope once it lands (toolsetLoadedMsg's
// handler in model.go), so switching profiles can never let an old read
// clobber the new selection's toggles. The toggles themselves are left
// exactly as they were until that read comes back — one frame showing the
// PREVIOUS profile's tool-set is a fair price for never blocking on a remote
// read here, which is the same trade openForm itself makes.
func (m *model) cycleFormProfile(delta int) tea.Cmd {
	list := m.formProfiles()
	n := len(list)
	if n == 0 {
		return nil
	}
	m.formProfileIdx = ((m.formProfileIdx+delta)%n + n) % n
	m.retargetFormScope(list[m.formProfileIdx])
	hs := m.formHostSample()
	m.inputs[fCPUs].SetValue(strconv.Itoa(defaultCPUs(hs.cpus)))
	m.inputs[fMemory].SetValue(defaultMemory(hs.mem))
	m.inputs[fUser].SetValue(orDefault(hs.user, hostUser()))
	return m.kickFormToolsetLoad()
}

// formToggle is one checkbox in the form: a label, its per-field help, and
// get/set closures onto the model field it drives. Both form modes (create
// and reset) build their own list of these and share one focus walk and one
// space/enter handler — see toggles, formFocusNext/Prev, and updateForm's
// toggleFocus guard.
type formToggle struct {
	label string
	help  string // shown as the focused-field help; "" renders nothing (reset mode's toggles)
	get   func(*model) bool
	set   func(*model, bool)
}

// baseWideHelp is shared by the three tool toggles: they configure the SHARED
// base image every future VM is cloned from, not just the VM this form is
// creating. That is never allowed to be a surprise from a per-VM screen.
func baseWideHelp(tool string) string {
	return "Installs " + tool + " into the SHARED base image every VM is cloned from — " +
		"not just this VM. Changing this rebuilds the base; de-selecting a tool " +
		"needs the \"Rebuild base image\" toggle below to actually remove it."
}

// createToggles is create mode's toggle list: the base-image tool-set
// (default on) and the rebuild intent (default off, wired to the same path
// `sand create --rebuild` uses — see submitForm).
func (m model) createToggles() []formToggle {
	return []formToggle{
		{
			label: "Install Claude Code",
			help:  baseWideHelp("Claude Code"),
			get:   func(m *model) bool { return m.toolClaude },
			set:   func(m *model, v bool) { m.toolClaude = v },
		},
		{
			label: "Install OpenAI Codex",
			help:  baseWideHelp("OpenAI Codex"),
			get:   func(m *model) bool { return m.toolCodex },
			set:   func(m *model, v bool) { m.toolCodex = v },
		},
		{
			label: "Install DDEV",
			help:  baseWideHelp("DDEV"),
			get:   func(m *model) bool { return m.toolDDEV },
			set:   func(m *model, v bool) { m.toolDDEV = v },
		},
		{
			label: "Install Go",
			help:  baseWideHelp("Go"),
			get:   func(m *model) bool { return m.toolGo },
			set:   func(m *model, v bool) { m.toolGo = v },
		},
		{
			label: "Install Java",
			help:  baseWideHelp("Java"),
			get:   func(m *model) bool { return m.toolJava },
			set:   func(m *model, v bool) { m.toolJava = v },
		},
		{
			label: "Rebuild base image",
			help: "Delete and rebuild the base image from scratch before creating. " +
				"Needed to actually remove a de-selected tool.",
			get: func(m *model) bool { return m.toolRebuild },
			set: func(m *model, v bool) { m.toolRebuild = v },
		},
	}
}

// resetToggles is reset mode's toggle list: preserve Claude Code settings,
// plus preserve the cloned project — the latter only when there is one
// (projectToggleEnabled), matching the pre-generalization behavior exactly.
func (m model) resetToggles() []formToggle {
	t := []formToggle{
		{
			label: "Preserve Claude Code settings",
			get:   func(m *model) bool { return m.preserveClaude },
			set:   func(m *model, v bool) { m.preserveClaude = v },
		},
	}
	if m.projectToggleEnabled {
		t = append(t, formToggle{
			label: m.projectToggleLabel,
			get:   func(m *model) bool { return m.preserveProject },
			set:   func(m *model, v bool) { m.preserveProject = v },
		})
	}
	return t
}

// toggles returns the active toggle list for the current form mode.
func (m model) toggles() []formToggle {
	if m.resetMode {
		return m.resetToggles()
	}
	return m.createToggles()
}

// focusNext / focusPrev move the cursor between fields in create mode,
// wrapping around. Past the last text input they walk into the toggles (see
// createToggles), then wrap back to the first input — mirroring
// resetFocusNext/Prev below, which do the same for reset mode's toggles.
//
// The profile selector (fProfileSelector) is a detour off the FRONT of that
// ring, not spliced into it: going backward from fName reaches it, and going
// forward from it returns to fName. The ring's own wrap (last toggle -> fName)
// is untouched, so it still lands a form freshly opened with 'n' — and a real
// user typing right after — in the Name field. See fProfileSelector's doc.
func (m *model) focusNext() tea.Cmd {
	if m.toggleFocus == -1 && m.focusIdx == fProfileSelector {
		m.focusIdx = fName
		return m.inputs[fName].Focus()
	}
	return m.formFocusNext(fName, fCloneToken)
}

func (m *model) focusPrev() tea.Cmd {
	switch {
	case m.toggleFocus == -1 && m.focusIdx == fProfileSelector:
		// Already at the front of the ring; nothing further back.
		return nil
	case m.toggleFocus == -1 && m.focusIdx == fName:
		m.inputs[fName].Blur()
		m.focusIdx = fProfileSelector
		return nil
	default:
		return m.formFocusPrev(fName, fCloneToken)
	}
}

// resetFocusNext advances focus in reset mode: through the editable inputs
// (starting at fHostname), then into the toggles, wrapping back to fHostname.
// The locked Name field is never focused, and toggles() already omits the
// project toggle when disabled (the VM cloned no repo).
func (m *model) resetFocusNext() tea.Cmd {
	return m.formFocusNext(fHostname, fCloneToken)
}

// resetFocusPrev reverses resetFocusNext.
func (m *model) resetFocusPrev() tea.Cmd {
	return m.formFocusPrev(fHostname, fCloneToken)
}

// formFocusNext/Prev are the shared focus walk behind focusNext/Prev and
// resetFocusNext/Prev: text inputs from firstInput to lastInput, then the
// current mode's toggles (m.toggles(), which already excludes any hidden
// ones), wrapping back to firstInput. Create mode passes fName as firstInput
// (the Name field is editable there); reset mode passes fHostname (the Name
// is locked and never focused).
func (m *model) formFocusNext(firstInput, lastInput int) tea.Cmd {
	n := len(m.toggles())
	switch {
	case m.toggleFocus == -1:
		if m.focusIdx < lastInput {
			m.inputs[m.focusIdx].Blur()
			m.focusIdx++
			return m.inputs[m.focusIdx].Focus()
		}
		// Past the last input → first toggle; blur all text inputs.
		m.inputs[m.focusIdx].Blur()
		m.toggleFocus = 0
		return nil
	case m.toggleFocus < n-1:
		m.toggleFocus++
		return nil
	default: // last toggle → wrap around to the first editable input
		m.toggleFocus = -1
		m.focusIdx = firstInput
		return m.inputs[firstInput].Focus()
	}
}

func (m *model) formFocusPrev(firstInput, lastInput int) tea.Cmd {
	n := len(m.toggles())
	switch {
	case m.toggleFocus > 0:
		m.toggleFocus--
		return nil
	case m.toggleFocus == 0:
		// Back up from the first toggle to the last input.
		m.toggleFocus = -1
		m.focusIdx = lastInput
		return m.inputs[lastInput].Focus()
	default: // focus is in the inputs
		if m.focusIdx > firstInput {
			m.inputs[m.focusIdx].Blur()
			m.focusIdx--
			return m.inputs[m.focusIdx].Focus()
		}
		// At the first editable input → wrap up to the last toggle.
		m.inputs[m.focusIdx].Blur()
		m.toggleFocus = n - 1
		return nil
	}
}

// field returns a trimmed form value, so a field holding only whitespace counts
// as blank for defaulting.
func (m model) field(i int) string { return strings.TrimSpace(m.inputs[i].Value()) }

// buildConfig assembles a CreateConfig from the form fields. Like the original
// bash provisioner, a blank field falls back to its default rather than
// producing an empty-named VM, an empty primary user, or an empty memory/disk
// that Lima would reject. Only the git identity has no default and is required
// (enforced by Validate).
func (m model) buildConfig() (vm.CreateConfig, error) {
	cfg := vm.DefaultCreateConfig()
	cfg.Name = m.field(fName)                              // required; Validate rejects empty
	cfg.Hostname = orDefault(m.field(fHostname), cfg.Name) // hostname defaults to the name
	cfg.User = orDefault(m.field(fUser), hostUser())
	cfg.GitName = m.field(fGitName)
	cfg.GitEmail = m.field(fGitEmail)

	if cpuStr := m.field(fCPUs); cpuStr == "" {
		cfg.CPUs = defaultCPUs(m.formHostSample().cpus)
	} else {
		cpus, err := vm.ParseCPUs(cpuStr)
		if err != nil {
			return cfg, err
		}
		cfg.CPUs = cpus
	}

	cfg.Memory = orDefault(m.field(fMemory), defaultMemory(m.formHostSample().mem))
	cfg.Disk = orDefault(m.field(fDisk), cfg.Disk)
	if lang := strings.TrimSpace(os.Getenv("LANG")); lang != "" {
		cfg.Locale = lang // matches the script's LOCALE="${LANG:-en_US.UTF-8}"
	}
	cfg.DockerProxyHost = m.field(fDockerProxyHost)
	cfg.CloneURL = m.field(fCloneURL)
	cfg.CloneToken = m.field(fCloneToken)
	if m.resetMode {
		// Reset mode shows no tool-set toggles, so it has to REPLAY the VM's
		// recorded selection (captured in openResetForm). cfg starts life as
		// DefaultCreateConfig(), whose tools are all on; leaving them there would
		// make every reset request the full tool-set, mark the SHARED base stale
		// against its stamp, and re-converge it — installing the very Claude
		// Code/Go/Java the user opted out of, silently, from a form that never
		// mentions them.
		cfg.WithClaude = m.resetWithClaude
		cfg.WithCodex = m.resetWithCodex
		cfg.WithDDEV = m.resetWithDDEV
		cfg.WithGo = m.resetWithGo
		cfg.WithJava = m.resetWithJava
		// Codex is replayed like its siblings: the RECORDED selection is the
		// truth here, not the default. WithCodex's default-off only protects the
		// ADD direction (an unconfigured create never installs it); a VM reset
		// from a recorded WithCodex=true must still replay true, or the reset
		// would silently de-select it and mark the shared base stale.
	} else {
		cfg.WithClaude = m.toolClaude
		cfg.WithCodex = m.toolCodex
		cfg.WithDDEV = m.toolDDEV
		cfg.WithGo = m.toolGo
		cfg.WithJava = m.toolJava
	}
	return cfg, nil
}

// parseLimaSize parses a Lima-style size string ("20GiB", "512MiB", "2TiB") into
// bytes using binary (1024) units, matching Lima's sizing. A bare number is
// treated as bytes. It returns false when the value is empty or unparseable, so
// callers can decide whether an unrecognisable size is an error.
func parseLimaSize(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	// Split the leading number (digits/decimal point) from the trailing unit.
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	num, err := strconv.ParseFloat(s[:i], 64)
	if err != nil || num < 0 {
		return 0, false
	}
	var mult float64
	switch strings.ToLower(strings.TrimSpace(s[i:])) {
	case "", "b":
		mult = 1
	case "k", "kib", "kb":
		mult = 1 << 10
	case "m", "mib", "mb":
		mult = 1 << 20
	case "g", "gib", "gb":
		mult = 1 << 30
	case "t", "tib", "tb":
		mult = 1 << 40
	case "p", "pib", "pb":
		mult = 1 << 50
	default:
		return 0, false
	}
	return int64(num * mult), true
}

// diskOverflowWarning returns a warning string when the disk the user asked for
// is larger than the free space sampled on the Lima volume, else "". qcow2 disks
// are sparse so this is advisory (the build still proceeds) — it flags that the
// disk can't actually grow to its requested size. Empty when free space is
// unknown (unprobed host) so we don't cry wolf.
func (m model) diskOverflowWarning() string {
	if m.hostDiskFree <= 0 {
		return ""
	}
	want, ok := parseLimaSize(m.field(fDisk))
	if !ok || want <= m.hostDiskFree {
		return ""
	}
	free := humanizeBytes(strconv.FormatInt(m.hostDiskFree, 10))
	return fmt.Sprintf("Warning: disk %s exceeds the %s free on the Lima volume; the VM may fail to grow to its full size.", m.field(fDisk), free)
}

// submitForm validates the form; on failure it keeps the form and surfaces the
// error, on success it switches to the streaming progress view and fires create
// (or, in reset mode, the reset).
func (m model) submitForm() (tea.Model, tea.Cmd) {
	cfg, err := m.buildConfig()
	if err != nil {
		m.formErr = err
		return m, nil
	}
	if m.resetMode {
		return m.submitReset(cfg)
	}
	if err := cfg.Validate(); err != nil {
		m.formErr = err
		return m, nil
	}
	if err := m.checkNotBusy(cfg.Name); err != nil {
		m.formErr = err
		return m, nil
	}
	m.formErr = nil
	// toolRebuild carries the "Rebuild base image" toggle's intent through to
	// the same code path `sand create --rebuild` uses: the rebuild happens
	// under the base lock inside CreateVMWithOptions, not as a pre-lock delete
	// here (see provision.CreateOptions.Rebuild).
	opts := provision.CreateOptions{Rebuild: m.toolRebuild}
	// Resolve the provider NOW and capture it by value: the run closure executes
	// on beginStream's goroutine, so it must not read m.members (which the Update
	// goroutine mutates). The provider itself is immutable for the session.
	prov := m.formProvider()
	if prov == nil {
		m.formErr = fmt.Errorf("this connection profile is not available")
		return m, nil
	}
	run := func(ctx context.Context, c vm.CreateConfig, out io.Writer) error {
		return prov.Create(ctx, c, opts, out)
	}
	cmd := m.beginProvision("Creating "+cfg.Name, run, cfg)
	return m, cmd
}

// checkNotBusy refuses a create/reset for a VM that already has a run in flight.
//
// It lives HERE, beside the form's other validation, rather than only deeper in
// beginJob, because this is the only place the user can still act on it: the name
// they typed is on screen and editable. vm.CreateConfig.Validate cannot make this
// check — it is a pure value validation and knows nothing about the job registry —
// which is exactly how typing the name of a VM that was already building used to
// sail through, whereupon beginStream refused the second run and the second form's
// config was stamped onto the FIRST one's build: wrong cpus, wrong memory, wrong
// clone URL, wrong token, recorded as managed when that build succeeded, and
// rebuilt from the wrong config by any later Reset.
func (m model) checkNotBusy(name string) error {
	if !m.jobs.isRunning(m.formScope, name) {
		return nil
	}
	return fmt.Errorf("%s already has a run in flight — wait for it to finish, or cancel it from its log (l)", name)
}

// submitReset validates a reset-mode form and dispatches provision.Reset. The
// Name and base image come from the locked targets (not the editable fields), and
// the disk must be at least the base floor: the base image is built at
// BaseDiskFloor and a clone's qcow2 disk can grow but not shrink.
func (m model) submitReset(cfg vm.CreateConfig) (tea.Model, tea.Cmd) {
	cfg.Name = m.resetName
	cfg.BaseName = m.resetBaseName
	if err := cfg.Validate(); err != nil {
		// Validate now enforces the base-disk floor for every entrypoint (a clone
		// cannot shrink below it), so the reset path no longer needs its own check.
		m.formErr = err
		return m, nil
	}
	// A reset of a VM a copy is still streaming into would delete the copy's target
	// out from under it; a reset of a VM that is already building is the same
	// double-run the create path refuses.
	if err := m.checkNotBusy(cfg.Name); err != nil {
		m.formErr = err
		return m, nil
	}
	m.formErr = nil
	opts := provision.ResetOptions{PreserveClaude: m.preserveClaude, PreserveProject: m.preserveProject && m.projectToggleEnabled}
	// Capture the provider by value (see submitForm): the run closure runs on
	// beginStream's goroutine and must not read the mutable m.members slice.
	prov := m.formProvider()
	run := func(ctx context.Context, c vm.CreateConfig, out io.Writer) error {
		return prov.Reset(ctx, c, opts, out)
	}
	// beginReset, not beginProvision: a reset DELETES its VM and clones it back, so
	// its VM legitimately vanishes from `limactl list` mid-run. The registry has to
	// know, or its disappeared-VM reaper would cancel the reset the moment it did
	// the deletion it exists to do.
	return m, m.beginReset("Resetting "+cfg.Name, run, cfg)
}

// updateForm handles keys while the create form is active.
func (m model) updateForm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Note: 'q' is a text character here, so only ctrl+c (handled globally) quits.
	// Only esc (not the shared Back binding) leaves the form: Back also matches
	// backspace, which here must edit the focused field, not navigate away.
	switch {
	case msg.Code == tea.KeyEsc:
		m.view = viewBoard
		m.resetMode = false // a later create form must not inherit reset state
		return m, nil
	case key.Matches(msg, m.keys.Submit): // ctrl+s — submit from any field
		return m.submitForm()
	}

	// Reset mode locks the Name field and adds two preserve toggles after the
	// inputs, so it navigates differently; create mode keeps its existing flow.
	if m.resetMode {
		return m.updateResetForm(msg)
	}

	// On a focused toggle, space/enter flips it rather than navigating, and the
	// key must NOT reach the text inputs below — mirrors updateResetForm.
	if m.toggleFocus >= 0 && (msg.Code == tea.KeySpace || msg.Code == tea.KeyEnter) {
		t := m.toggles()[m.toggleFocus]
		t.set(&m, !t.get(&m))
		return m, nil
	}

	// On the focused profile selector, left/right cycles the enabled profile
	// list rather than navigating between fields — the selector's own analogue
	// of the toggle carve-out just above. Up/Down/Tab still move focus away
	// from it as usual (handled by the switch below).
	if m.toggleFocus == -1 && m.focusIdx == fProfileSelector {
		switch msg.Code {
		case tea.KeyLeft:
			return m, m.cycleFormProfile(-1)
		case tea.KeyRight:
			return m, m.cycleFormProfile(1)
		}
	}

	switch {
	case key.Matches(msg, m.keys.ShiftTab), key.Matches(msg, m.keys.Up):
		return m, m.focusPrev()
	// Down/Tab/enter all advance to the next field; enter no longer creates.
	case key.Matches(msg, m.keys.Down), key.Matches(msg, m.keys.Tab):
		return m, m.focusNext()
	}

	// Only forward edits while a text input is focused (toggles and the profile
	// selector aren't inputs).
	if m.toggleFocus != -1 || m.focusIdx == fProfileSelector {
		return m, nil
	}

	cmds := make([]tea.Cmd, len(m.inputs))
	for i := range m.inputs {
		m.inputs[i], cmds[i] = m.inputs[i].Update(msg)
	}
	return m, tea.Batch(cmds...)
}

// updateResetForm handles keys for the reset-mode form: navigation skips the
// locked Name and extends into the two preserve toggles, and space/enter on a
// focused toggle flips it instead of moving focus.
func (m model) updateResetForm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// On a focused toggle, space/enter flips its bool rather than navigating.
	if m.toggleFocus >= 0 && (msg.Code == tea.KeySpace || msg.Code == tea.KeyEnter) {
		t := m.toggles()[m.toggleFocus]
		t.set(&m, !t.get(&m))
		return m, nil
	}

	switch {
	case key.Matches(msg, m.keys.ShiftTab), key.Matches(msg, m.keys.Up):
		return m, m.resetFocusPrev()
	case key.Matches(msg, m.keys.Down), key.Matches(msg, m.keys.Tab):
		return m, m.resetFocusNext()
	}

	// Only forward edits while a text input is focused (toggles aren't inputs).
	if m.toggleFocus == -1 {
		cmds := make([]tea.Cmd, len(m.inputs))
		for i := range m.inputs {
			m.inputs[i], cmds[i] = m.inputs[i].Update(msg)
		}
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

// toggleRow renders one reset-mode preserve toggle: a checkbox glyph and the
// label, highlighted when focused. It uses inline styles rather than
// labelStyle/focusedLabelStyle, whose fixed width would wrap these longer
// lines. Callers only render a toggle that is actually usable — a disabled,
// unreachable toggle never reaches the screen.
func toggleRow(label string, on, focused bool) string {
	box := "[ ]"
	if on {
		box = "[x]"
	}
	line := box + " " + label
	if focused {
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63")).Render(line)
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(line)
}

// profileSelectorRow renders the create form's profile selector: the
// currently selected ENABLED profile's name, bracketed with cycle hints when
// there is more than one to choose from, and highlighted like a focused label
// when the selector itself has focus. formProfiles/formProfileIdx being empty
// (a degenerate store with nothing enabled) renders an explanatory value
// rather than indexing out of range.
func (m model) profileSelectorRow() string {
	list := m.formProfiles()
	value := "(no enabled profiles)"
	if len(list) > 0 {
		i := m.formProfileIdx
		if i < 0 || i >= len(list) {
			i = 0
		}
		value = list[i].Name
		if len(list) > 1 {
			value = "< " + value + " >"
		}
	}
	ls := labelStyle
	if m.toggleFocus == -1 && m.focusIdx == fProfileSelector {
		ls = focusedLabelStyle
	}
	return ls.Render("Profile:") + " " + value
}

// formHelp returns the bindings shown in the create/reset form's help bar.
// 'q' is a text character in the form, so Quit is intentionally omitted (only
// ctrl+c quits). Up/Down/enter move between fields; ctrl+s creates.
func (m model) formHelp() []key.Binding {
	return []key.Binding{m.keys.Up, m.keys.Down, m.keys.Submit, m.keys.Back}
}

// formView renders the labelled inputs, validation error, and help. In reset mode
// the Name is shown as a locked line and two preserve toggles follow the inputs.
func (m model) formView() string {
	cw := m.layout.ContentWidth
	var b strings.Builder
	title := "New VM"
	if m.resetMode {
		title = "Reset VM"
	}
	b.WriteString(titleStyle.Render(title))
	b.WriteString("\n\n")

	// The profile selector: which connection profile's provider/scope
	// this create targets. Reset mode has none — a reset always targets its own
	// VM's already-fixed member, never a place the user picks.
	if !m.resetMode {
		b.WriteString(m.profileSelectorRow() + "\n")
	}

	for i := range m.inputs {
		// In reset mode the Name is fixed to the target VM: render it as a static,
		// dimmed line rather than an editable input box.
		if m.resetMode && i == fName {
			b.WriteString(statusStyle.Render("Name: "+m.resetName+" (locked)") + "\n")
			continue
		}
		ls := labelStyle
		// A toggle being focused blurs every text input, in both modes now that
		// create mode has toggles too.
		focused := m.toggleFocus == -1 && i == m.focusIdx
		if focused {
			ls = focusedLabelStyle
		}
		b.WriteString(ls.Render(fieldLabels[i]+":") + " " + m.inputs[i].View() + "\n")
	}

	// The mode's toggles (preserve* in reset mode, tool-set + rebuild in create
	// mode) and reset mode's compromise warning.
	toggles := m.toggles()
	if len(toggles) > 0 {
		b.WriteString("\n")
		for i, t := range toggles {
			b.WriteString(toggleRow(t.label, t.get(&m), m.toggleFocus == i) + "\n")
		}
	}
	if m.resetMode {
		if m.preserveClaude || m.preserveProject {
			b.WriteString("\n" + errStyle.Width(cw).Render("Preserving copies your Claude login and the .env token out of the VM to your host. Do NOT preserve if you suspect this VM is compromised.") + "\n")
		}
		b.WriteString("\n" + fieldInfoStyle.Width(cw-2).Render("Disk can only grow from the base floor (min "+vm.BaseDiskFloor+").") + "\n")
	}

	// Help for the focused field (where to get a GitHub token, what defaults
	// apply, which fields are required) or, when a toggle is focused, that
	// toggle's own help (e.g. the tool toggles' base-wide-effect warning).
	// Reset mode's toggles carry no help text, matching the pre-existing
	// behavior of showing nothing while one of them is focused.
	switch {
	case m.toggleFocus >= 0 && m.toggleFocus < len(toggles) && toggles[m.toggleFocus].help != "":
		b.WriteString("\n" + fieldInfoStyle.Width(cw-2).Render(toggles[m.toggleFocus].help) + "\n")
	case m.toggleFocus == -1 && m.focusIdx == fProfileSelector:
		b.WriteString("\n" + fieldInfoStyle.Width(cw-2).Render("←/→ to pick which connection profile creates this VM. The CPU/memory/user suggestions below scale to that profile's host.") + "\n")
	case m.toggleFocus == -1 && m.focusIdx >= 0 && m.focusIdx < len(fieldInfo):
		// cw-2 accounts for fieldInfoStyle's left border + left padding, so the
		// wrapped help still fits inside the content column.
		b.WriteString("\n" + fieldInfoStyle.Width(cw-2).Render(fieldInfo[m.focusIdx]) + "\n")
	}

	if m.formErr != nil {
		b.WriteString("\n" + errStyle.Width(cw).Render("Error: "+m.formErr.Error()))
	}

	if w := m.diskOverflowWarning(); w != "" {
		b.WriteString("\n" + warnStyle.Width(cw).Render(w) + "\n")
	}

	b.WriteString("\n" + m.footerView(m.formHelp()))
	return appStyle.Render(b.String())
}

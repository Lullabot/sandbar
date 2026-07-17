// Package ui holds the Bubble Tea model, views, and commands for the sand
// TUI. It is a thin interactive surface over the provider.Provider (VM
// lifecycle, guest transport, and create/reset — the local Lima backend by
// default), registry.Registry (which VMs are ours), and secrets.Store (per-VM
// host-side secrets) packages.
//
// Screens divide by responsibility: the BOARD (board.go) is the home surface and
// the only roster — a grid of tiles, one per managed clone, with a focus ring and
// the single-key verbs that act on the tile under it; the VM screen zooms one
// tile to full screen and owns the same verbs there. All blocking I/O happens in
// tea.Cmds so Update never stalls, and the long-running provisioner streams its
// output into a scrollable progress pane.
package ui

import (
	"errors"
	"strings"
	"time"

	"github.com/lullabot/sandbar/internal/browse"
	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/manage"
	"github.com/lullabot/sandbar/internal/paste"
	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/secrets"
	"github.com/lullabot/sandbar/internal/vm"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// maxFooterHelpLines is the number of rows the layout RESERVES for the wrapped help
// bar. It is a constant on purpose, and that is the whole point: this budget sets
// GridHeight, which sizes the tile grid, the progress viewport, the file browser and
// the secrets editor. Deriving it from the verbs that happen to be eligible right now
// made the layout a function of the FOCUS RING — a stopped VM offers fewer verbs than
// a running one, and the ghost fewer still — so the grid's row count changed as the
// user merely arrowed between tiles, and the panes went on rendering at a height from
// a budget that no longer existed.
//
// Two, because two is what the board's real footer needs at the narrowest supported
// terminal: at 80 columns a running VM's verbs wrap to exactly two rows and all of
// them are visible. Budgeting for the union of every verb instead would reserve three
// (it counts `s start` AND `x stop`, which can never both apply) and cost the board a
// whole tile row at the one size the plan requires to work. A footer that needs more
// rows than this is cut, and the cut is marked — see footerView.
const maxFooterHelpLines = 2

// listRaceLimit is how many consecutive refreshes may fail as an apparent clone
// window before sand stops believing it. A clone of a large base takes 40-60s and
// the refresh ticks every refreshInterval, so this is generously past any real one:
// beyond it, the instance directory is broken rather than busy, and saying so is the
// only thing that can help the user. See the vmsLoadedMsg handler.
const listRaceLimit = 24

// confirmState is a pending destructive action awaiting the user's `y`. It is
// screen-agnostic: the board raises it for stop-all and for a quit that would
// abandon a run, and the VM screen raises it for delete — neither screen needs to
// know what the other confirms. A nil *confirmState on the model means no
// confirmation is pending.
type confirmState struct {
	prompt  string  // e.g. `Delete "web"?`
	run     tea.Cmd // dispatched (via beginAction) when the user presses Confirm
	working string  // status shown (with the live spinner) while run is in flight; "" leaves the status line untouched
}

// view is the active screen the model renders and routes keys to. viewBoard is
// the default and the only roster: the table-backed list it replaced is gone,
// with no toggle back to it.
type view int

const (
	viewBoard view = iota
	viewForm
	viewProgress
	viewBrowse
	viewDest
	viewSecrets
	// viewProfiles and viewProfileForm are the profile management screen
	// (profilesview.go, task 8): a list of every connection profile, and a
	// sub-form over one profile's fields (create/edit), following the same
	// view-enum + sub-model pattern as viewForm/viewSecrets above.
	viewProfiles
	viewProfileForm
	viewHelp
)

// model is the root Bubble Tea model. It is passed by value through Update, so
// every field must be safe to copy — no strings.Builder, and nothing mutable that
// has to outlive one Update call. State that does (a running job's log, its
// cancel func, its parsed progress) lives behind the jobs POINTER below, in the
// one registry every model copy shares. That is the whole reason the registry is
// a pointer: a copied model must not fork the work it is watching.
type model struct {
	// members is the FLEET: one sub-state per ENABLED connection profile
	// (provider.BuildFleet → New), each with its own provider, scope,
	// host-access seam, last-known VM list, host sample, connection status,
	// last error and self-heal backoff (see fleetMember, fleet.go). It replaces
	// the single `p`/`scope` the model used to hold — deliberately, so any path
	// that still assumed one provider fails to compile. The board roster is the
	// UNION of every member's managed VMs (boardVMs); each member lists,
	// reconciles, heartbeats and self-heals independently, so a slow or
	// unreachable remote never blocks the UI. It is value state, updated by
	// returning copies from Update handlers exactly like m.vms was.
	members []fleetMember
	// active is the index of the member the single-band header reports and a NEW
	// create targets (task 10 makes the header per-profile; task 9 lets the
	// create form pick). Pinned by New to the Local member, or the first.
	active int

	reg  *registry.Registry
	keys keyMap
	help help.Model

	view   view
	width  int
	height int
	layout layoutMode // budgets derived from width/height by classify (see layout.go); set by applySize

	// messages is the session-only, bounded activity log (messages.go) that
	// replaced the single overwritten status string this field used to be:
	// logMsg appends, lastMessage/recentMessages read. It is a plain slice,
	// not a pointer, because — unlike jobs/heartbeats — nothing outside the
	// Update goroutine ever writes to it.
	messages []message

	// focusVM is the VM under the board's focus ring: AN IDENTITY, NEVER A SLOT
	// INDEX. A refresh, an insertion, a deletion or a filter keystroke reorders the
	// grid; the ring stays on the same VM, because a ring that tracked the index
	// would silently slide onto a different VM and hand the next destructive key to
	// it. It is a vmHandle (scope + name), not a bare name, so the ring's identity
	// can never be mistaken for "whatever VM has this name" once more than one
	// scope is in play (task 7) — renamed from focusName for exactly that reason.
	// scrollRow is the first tile ROW the grid viewport shows, and only
	// ensureFocusVisible moves it — so the viewport cannot drift away from the ring.
	focusVM   vmHandle
	scrollRow int

	// helpScroll is the `?` screen's scroll offset (help.go).
	helpScroll int

	// jobs is the job registry (jobs.go), keyed by VM AND KIND: every provision and
	// transfer in flight, plus the last run of each kind a VM retained — a failed
	// build and a later file copy are two runs, and the copy may not evict the
	// build. It is a POINTER because the model is passed by value — a registry
	// embedded here would fork on the first copy —
	// It gates Delete while a VM builds, and the reopen-log verb on a VM having a run
	// to show (see commandreg.go). A nil
	// registry is safe to call and reports "no jobs", so a model built by hand
	// behaves exactly as it did before this existed.
	jobs *jobRegistry

	// heartbeats is the VM-keyed guest heartbeat registry (heartbeat.go): one live
	// `limactl shell` per RUNNING VM, streaming real cpu and memory out of the guest.
	// A POINTER for the same reason jobs is, and nil-safe for the same reason.
	heartbeats *heartbeatRegistry

	// lastInput is when the user last touched a key. Together with the active view
	// it is the idle gate (shouldTick, heartbeat.go) that decides whether sand may
	// hold SSH connections open into the guests. Terminal focus is deliberately NOT
	// part of that gate — see shouldTick for why blur turned out to be the wrong
	// signal — but a FocusMsg still refreshes this, because returning to the terminal
	// is the user saying "I'm back".
	lastInput time.Time

	// Incremental name search. When searching is true, typed keys edit
	// searchQuery instead of firing actions; searchQuery is a case-insensitive
	// name substring filter over the board's tiles (visibleVMs, board.go). It
	// narrows what is SHOWN and nothing else: 'X' still stops every managed VM.
	searching   bool
	searchQuery string

	// acting is true while a quick lifecycle action (start/stop/restart/delete) is
	// in flight. It drives the spinner beside the status line so these blocking
	// limactl calls show live feedback, and is cleared by the matching
	// actionDoneMsg.
	acting bool

	// Pending destructive-action confirmation (nil = inactive). Raised by
	// whichever screen dispatches the action; routed and rendered from the board,
	// the VM screen and a finished progress screen via updateConfirm/confirmView
	// below.
	confirm *confirmState

	// Create form.
	inputs   []textinput.Model
	focusIdx int
	formErr  error
	// formScope is the member a create/reset targets: the ACTIVE member for a
	// new VM, or the focused VM's own member for a reset. The form's provider,
	// host-derived defaults (cpus/memory/user), managed-index bookkeeping and
	// GH_TOKEN secret all resolve through it, so a create can never land on the
	// wrong profile. Defaulted to the active scope in New, so a hand-built model
	// (tests) that never opens a form still keys begin*/checkNotBusy correctly.
	formScope    registry.Scope
	hostDiskFree int64 // free bytes on the Lima volume, sampled when the form opens (0 = unknown)
	// formProfileIdx indexes the create form's profile selector into
	// formProfiles() (the ENABLED profiles, store order) — task 9's Component 4
	// create-time half. setDefaultFormProfile picks it (last-used, else Local)
	// when the form opens; cycleFormProfile moves it (and formScope with it) as
	// the user picks a different destination. Reset mode never touches it — a
	// reset always targets its own VM's already-fixed member.
	formProfileIdx int

	// Reset mode reuses the create form to reset a managed VM: the Name is locked
	// to the target and two preserve toggles follow the inputs.
	resetMode     bool
	resetName     string // locked Name when in reset mode
	resetBaseName string // base image the reset clones from
	// The reset target's RECORDED tool-set, captured in openResetForm. The reset
	// form shows no tool toggles, so without carrying these the rebuilt config
	// would fall back to DefaultCreateConfig()'s all-on selection and a reset
	// would silently re-converge the SHARED base back to the full tool-set —
	// installing a Go toolchain and a JDK the user had explicitly opted out of.
	resetWithClaude      bool
	resetWithCodex       bool
	resetWithDDEV        bool
	resetWithGo          bool
	resetWithJava        bool
	preserveClaude       bool
	preserveProject      bool
	projectToggleEnabled bool   // false when OrgRelDir(cfg.CloneURL) has no org segment (nothing to preserve)
	projectToggleLabel   string // "Preserve ~/<org-rel-dir>", computed once in openResetForm
	toggleFocus          int    // -1 = focus is in the text inputs; index into m.toggles() otherwise

	// Create-mode tool-set + rebuild toggles (defaults set in openForm). The
	// tool toggles configure the SHARED base image, not this one VM — see
	// createToggles' help text. toolRebuild carries the same intent as
	// `sand create --rebuild` (provision.CreateOptions.Rebuild), the only way to
	// actually remove a de-selected tool (Ansible cannot uninstall).
	// toolClaude is Claude Code: a tool-set selection like the rest, so a user
	// can de-select it and install their own agent. Not to be confused with
	// preserveClaude above, which is reset mode's keep-my-~/.claude toggle.
	toolClaude  bool
	toolCodex   bool
	toolDDEV    bool
	toolGo      bool
	toolJava    bool
	toolRebuild bool

	// Progress / streaming. Everything that used to be a single job's state on the
	// model — its title, back view, output buffer, running flag, error, reader,
	// config and cancel func — now lives on that job in the registry above. What is
	// left here is which job the progress SCREEN is showing, which is a property of
	// the screen, not of the work: the job goes on whether or not anyone is looking.
	viewport viewport.Model
	spinner  spinner.Model

	// progressJob is the RUN the progress screen displays — a VM plus which of its
	// runs, because a VM can have both a build and a file copy in flight (jobs.go).
	// A zero-value vm name means none.
	progressJob jobKey

	// spinning is true while exactly one spinner.Tick loop is in flight. Jobs and
	// quick actions can now overlap (they could not while the old model-wide
	// running flag froze the keyboard), and each one kicking its own tick would
	// stack loops and spin the animation at double speed. tickSpinner owns this
	// flag.
	spinning bool

	// File transfer (Upload/Download). The browser and dest prompt are copy-safe
	// (only a list.Model / textinput.Model plus small scalars), matching the
	// value-passed model. destination is always a directory; the source is placed
	// inside it, so the result is identical across the rsync/scp copy backends.
	browser           browse.Browser
	dest              browse.DestInput
	transferVM        string         // VM the transfer targets
	transferScope     registry.Scope // the owning member's scope, captured when the transfer opens
	transferUpload    bool           // true = upload (host→guest); false = download (guest→host)
	transferSrc       string         // chosen source (absolute; a host path, or a guest path without the "vm:" prefix)
	transferRecursive bool           // the source is a directory (copied with -r)

	// Secrets editor. sec is the host-side store (a pointer, so the value-passed
	// model stays cheap to copy). secretsArea holds the KEY=VALUE buffer and
	// secretsVM the VM it belongs to.
	sec          *secrets.Store
	secretsArea  textarea.Model
	secretsVM    string
	secretsScope registry.Scope // the owning member's scope, captured when the editor opens
	secretsErr   error

	// profileStore is the persisted connection-profiles store (task 1),
	// loaded exactly like reg/sec above (see New). The profile management
	// screen (profilesview.go, task 8) creates/edits/enables/disables/deletes
	// profiles through it and applies the change LIVE, without a restart: a
	// pointer, like reg/sec, so a mutation persists across the value-passed
	// model.
	profileStore *profiles.Store

	// Profile management screen (profilesview.go). profileCursor indexes
	// profileStore.List() (its stable insertion order) for the list's ring.
	// profileMsg is the last management action's result — a validation
	// error, or an idle-gate refusal naming the blocking job — shown at the
	// top of the screen until the next action replaces or clears it.
	profileCursor int
	profileMsg    string
	// profileConfirmDeleteID is the id of a profile pending a delete
	// confirmation ("" = none pending). Deleting a profile is a synchronous
	// local store write, not an asynchronous VM lifecycle action, so it uses
	// its own tiny confirm flag rather than the board's confirmState/
	// beginAction (built for batching a spinner with a tea.Cmd).
	profileConfirmDeleteID string

	// Profile create/edit form (profilesview.go). profileFormID is "" while
	// creating a new RemoteSSH profile, else the id of the profile being
	// edited (the Local profile's rename-only form, or a RemoteSSH
	// profile's full connection-field set).
	profileFormID    string
	profileFormType  profiles.Type
	profileInputs    []textinput.Model
	profileFormFocus int
	profileFormErr   error
}

// New wires the dependencies into a ready-to-run tea.Model over a FLEET: one
// per-profile sub-state per binding in fleet (provider.BuildFleet — one enabled
// profile each). The board is the union of every member's managed VMs; each
// member connects, lists, reconciles and self-heals on its own tea.Cmd (Init),
// so a slow or unreachable remote never blocks the UI. A binding whose provider
// failed to construct (Binding.Err set, Prov nil) becomes a dormant error member
// rather than aborting the whole fleet.
//
// For the zero-config store — a single enabled Local profile — the fleet has
// exactly one member and every path resolves to registry.LocalScope, so the
// board, header and create form render bit-identically to the pre-fleet
// single-provider model.
func New(fleet provider.Fleet) tea.Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	// Load the managed-VM index. LoadFrom always returns a usable (non-nil)
	// registry; a corrupt/unreadable file surfaces as a warning rather than
	// silently demoting every managed VM.
	reg, loadErr := registry.Load()
	if reg == nil {
		reg = registry.NewEmpty()
	}

	// The secrets store gets the same tolerant posture as the registry: a
	// corrupt file surfaces as a warning rather than crashing or silently
	// discarding the VM's secrets.
	sec, secErr := secrets.Load()
	if sec == nil {
		sec = secrets.NewEmpty()
	}

	// The profiles store gets the same tolerant posture: a corrupt file is
	// quarantined and reseeded (profiles.LoadFrom's doc comment) rather than
	// failing New outright. This is a SEPARATE load from whatever main.go
	// already did to build fleet (mirroring how reg/sec above are loaded
	// fresh here rather than threaded in as constructor args) — both reads
	// see the same on-disk profiles.yaml, and main.go's own copy is never
	// touched again after BuildFleet runs, so there is nothing to drift out
	// of sync. This is what the profile management screen (profilesview.go)
	// persists every create/edit/enable/disable/delete through.
	profileStore, profErr := profiles.Load()

	members := make([]fleetMember, 0, len(fleet))
	active := 0
	for i, b := range fleet {
		mem := fleetMember{
			profile: b.Profile,
			prov:    b.Prov,
			scope:   b.Scope,
			state:   connConnecting,
			lastErr: b.Err,
		}
		// An error binding starts errored (nothing to construct a seam from);
		// a real member carries its own host-access seam and, if it is a local
		// backend, a seeded host sample so the header renders sensible numbers
		// before its first list lands. hostMemBytes reads /proc/meminfo and
		// freeDiskBytes statfs's the Lima volume; a header that fell back to
		// probing every frame would block on a stale mount, and FOREVER on a
		// host where the probe legitimately returns 0 (a zero sentinel is never
		// replaced by another zero) — so it is sampled ONCE here.
		if b.Prov != nil {
			mem.hostFiles = b.Prov.HostFiles()
			if b.Scope.RemoteTarget == "" {
				mem.host.mem = hostMemBytesFn()
				mem.host.diskFree = hostDiskFreeFn()
			}
		} else {
			mem.state = connErrored
			mem.hostFiles = lima.LocalFiles()
		}
		members = append(members, mem)
		// The single-band header and a new create target the Local member when
		// the fleet has one; otherwise the first member. Task 9 lets the create
		// form pick; task 10 makes the header per-profile.
		if b.Scope == registry.LocalScope {
			active = i
		}
	}

	m := model{
		members:      members,
		active:       active,
		reg:          reg,
		sec:          sec,
		profileStore: profileStore,
		jobs:         newJobRegistry(),
		heartbeats:   newHeartbeatsResolver(fleetShellResolver(members)),
		keys:         newKeyMap(),
		help:         help.New(),
		view:         viewBoard,
		viewport:     viewport.New(),
		spinner:      sp,
		// The session starts freshly used; anything else and the idle
		// gate would be shut before the first frame.
		lastInput: time.Now(),
	}
	// A create/reset dispatched before any form opens (tests) still keys its
	// job/registry work correctly: default the form target to the active member.
	m.formScope = m.activeScope()
	// Announce each REMOTE connection attempt in the session log. The local
	// member is deliberately silent here: it is the machine sand runs on, not a
	// connection — logging "connecting to local" at every startup would be
	// noise, and would break the zero-config board's bit-parity with the
	// pre-profiles TUI. (Deliberate user actions like disable still log for
	// every profile — see disableProfile.)
	for i := range m.members {
		if m.members[i].profile.Type == profiles.TypeRemoteSSH && m.members[i].prov != nil {
			m.logMsg("connecting to " + m.members[i].profile.Name + "…")
		}
	}
	// Seed a sane pre-resize default (mirrors the terminal's classic 80x24) so
	// the model renders sensibly before the first real WindowSizeMsg arrives.
	m.applySize(80, 24)
	// No one load failure may silently shadow another.
	var warnings []string
	for _, err := range []error{loadErr, secErr, profErr} {
		if err != nil {
			warnings = append(warnings, err.Error())
		}
	}
	if len(warnings) > 0 {
		m.logWarn(strings.Join(warnings, "; "))
	}
	return m
}

// fleetShellResolver maps a VM's scope to the guestShell that reaches its guest —
// the owning member's provider. The heartbeat registry keys every live shell by
// the full (scope, name) handle, so it holds connections into VMs across
// profiles at once; this resolver is what makes a remote VM's heartbeat shell
// into the remote host and a local VM's into local Lima. A same-named VM under
// two profiles reaches two different guests, never one. The members snapshot is
// captured at New (the fleet is fixed for this task's lifetime); a member with
// no provider (error binding) resolves to nil, so no heartbeat opens for it.
func fleetShellResolver(members []fleetMember) shellFor {
	snapshot := append([]fleetMember(nil), members...)
	return func(sc registry.Scope) guestShell {
		for i := range snapshot {
			if snapshot[i].scope == sc {
				if snapshot[i].prov == nil {
					return nil
				}
				return snapshot[i].prov
			}
		}
		return nil
	}
}

// Init kicks off every member's connect: a preflight + first list, each on its
// OWN tea.Cmd, so startup never blocks on a remote handshake. A member whose
// preflight blocks or times out marks itself an error member (its refreshCmd
// returns a failing vmsLoadedMsg) without holding up the board or any other
// member. An error binding (nil provider) has nothing to connect.
func (m model) Init() tea.Cmd {
	var cmds []tea.Cmd
	for i := range m.members {
		mem := m.members[i]
		if mem.prov == nil {
			continue
		}
		cmds = append(cmds, refreshCmd(mem.scope, mem.prov, mem.hostFiles, true))
	}
	return tea.Batch(cmds...)
}

// applySize recomputes the layout mode for a new terminal size and pushes its
// budgets onto every sub-component the active (or potentially active) view
// owns. This is the single place classify is invoked — nothing else may call
// it, so every pane's size traces back to one decision. Called both from New
// (to seed a pre-resize default) and the WindowSizeMsg handler.
func (m *model) applySize(w, h int) {
	m.width, m.height = w, h
	// Two passes, because the footer's height depends on the content width and the
	// grid's height depends on the footer's. The first pass settles the width (which
	// depends on w alone); the second buys the help bar exactly the rows it needs.
	// TWO PASSES. The first settles the content WIDTH (which depends on w alone); the
	// second buys the help bar the rows it needs at that width.
	//
	// The budget is taken from the WIDEST the footer could ever be — every verb the
	// board can offer — not from the verbs eligible right now. That is what keeps the
	// layout a pure function of the terminal size, and it has to be: this budget sets
	// GridHeight, which sizes the viewport, the file browser and the secrets editor
	// below. Re-budgeting per message (which is what it did) meant the grid's row
	// count changed as the focus ring merely MOVED — a stopped VM offers fewer verbs
	// than a running one, so tiles jumped as the user arrowed between them — and the
	// panes went on rendering at a height from a footer budget that no longer existed.
	// A row of slack in the band is a cheap price for a layout that holds still.
	// Header bands (task 10) are threaded through exactly like the help bar's
	// line count: desiredHeaderBands reads the CURRENT fleet state (members is
	// always populated before applySize runs, both at New and at every resize),
	// so classifyWithHeaderBands can budget the right number of extra header
	// rows for the fleet as it stands right now, not just the terminal size.
	m.layout = classifyWithHeaderBands(w, h, maxFooterHelpLines, m.desiredHeaderBands())

	// The help bar's OWN width-based truncation is disabled (0): bubbles'
	// ShortHelpView only stops adding items when it ALSO has room left for its
	// own ellipsis — lacking that room, it silently renders one more item
	// unclipped instead of stopping, which is what let the VM screen's longest
	// footer overflow the terminal by ~2 cells. Rather than fight that logic
	// with a width budget, ShortHelpView is left to render its full,
	// UNCLIPPED list, and footerView (model.go) does the actual, honest clip
	// itself with an ANSI-aware truncation to ContentWidth.
	m.help.SetWidth(0)
	// The log viewport is rendered INSIDE boxStyle (progressView), whose border and
	// padding are drawn around it — so the viewport gets the pane's budget MINUS
	// that chrome. Sized to the full budget it fit nowhere: the box came out
	// ContentWidth+4 wide, the terminal clipped the overrun, and the box lost its
	// right-hand border (and, vertically, its bottom one) to the clip.
	m.viewport.SetWidth(clamp(m.layout.ContentWidth-boxChromeH, minBudget))
	m.viewport.SetHeight(clamp(m.layout.GridHeight-boxChromeV, minBudget))

	// A resize changes the grid's columns and its visible rows, so the focused tile
	// can land outside the viewport without the ring moving at all. Re-park the
	// scroll on it.
	m.ensureFocusVisible()

	// Resize an active file browser too (its inner list is only initialized
	// while a transfer is in flight, so guard on the view to avoid touching a
	// zero-value browser).
	if m.view == viewBrowse {
		m.browser.SetSize(m.layout.ContentWidth, m.layout.GridHeight)
	}
	// Keep an open secrets editor sized to the terminal (its height budget
	// must match openSecrets, or the editor plus its footer would overflow and
	// scroll the title off the top).
	if m.view == viewSecrets {
		w, h := secretsEditorSize(m.layout)
		m.secretsArea.SetWidth(w)
		m.secretsArea.SetHeight(h)
	}
	// Reflow the shown job's streamed output to the new width so it stays wrapped.
	if m.progressJob.vm != "" {
		m.setOutput()
	}
}

// tickSpinner starts the spinner's tick loop, and returns nil if one is already
// in flight. Exactly one loop may exist: jobs and quick lifecycle actions can now
// overlap — two builds, or a build plus a start — and a tick loop per caller
// would animate the spinner at two, three, N times its speed. The loop ends
// itself (clearing m.spinning) when the last of them finishes; see the
// spinner.TickMsg handler in Update.
func (m *model) tickSpinner() tea.Cmd {
	if m.spinning {
		return nil
	}
	m.spinning = true
	return m.spinner.Tick
}

// footerView renders bindings through the shared help.Model and then clips the
// result HONESTLY to ContentWidth with an ANSI-aware truncation of its own — see
// the note on applySize above for why it may not lean on help.Model's own width
// truncation (SetWidth(0) deliberately turns that off, because it renders one item
// PAST the budget rather than stopping).
//
// EVERY footer goes through here. Four of the five screens used to call
// m.help.ShortHelpView directly, which — with the internal truncation disabled —
// meant they were rendering completely unclipped help bars: the overflow this
// function exists to prevent, still live on the form, the secrets editor, the
// destination prompt and the progress screen. A footer that is not clipped here is
// not clipped at all.
func (m model) footerView(bindings []key.Binding) string {
	lines := m.footerLines(bindings)
	// The cap is enforced HERE, for every screen, not in the board's band. Only the
	// board obeyed it, so the form, the secrets editor, the destination prompt and the
	// progress screen could each render more help rows than the layout had budgeted —
	// and a band that renders more rows than the layout counted is what pushed the help
	// bar off the bottom of an 80x24 terminal in the first place.
	if n := m.layout.HelpLines; n > 0 && len(lines) > n {
		lines = lines[:n]
		lines[n-1] = m.clipLine(lines[n-1] + " …")
	}
	return strings.Join(lines, "\n")
}

// footerLines packs the help bar into as many lines as it needs, WRAPPING rather
// than truncating.
//
// It used to be one clipped line, so a board with eight eligible verbs simply ended
// in "…" and the rest were unfindable: the whole point of deriving the footer from
// the command registry is that a user can see what they can do, and a verb the
// footer had no room to print may as well not exist. The rows the extra lines cost
// come out of the grid (see classify), which has them to spare — the board targets
// 1-3 VMs.
//
// Items are packed greedily at their own boundaries, never mid-item: a wrapped
// "u upl / oad" would be worse than the truncation it replaced. Each item is
// rendered through the shared help.Model, so the styling is bubbles' own.
func (m model) footerLines(bindings []key.Binding) []string {
	width := m.layout.ContentWidth
	if width < 1 {
		width = 1
	}
	sep := m.help.Styles.ShortSeparator.Render(m.help.ShortSeparator)

	var lines []string
	cur := ""
	for _, b := range bindings {
		if !b.Enabled() {
			continue
		}
		item := m.help.ShortHelpView([]key.Binding{b})
		switch {
		case cur == "":
			cur = item
		case ansi.StringWidth(cur)+ansi.StringWidth(sep)+ansi.StringWidth(item) <= width:
			cur += sep + item
		default:
			lines = append(lines, cur)
			cur = item
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	// A single item wider than the terminal still has to be cut somewhere.
	for i, l := range lines {
		lines[i] = m.clipLine(l)
	}
	return lines
}

// clipLine truncates one rendered line to ContentWidth, ANSI-aware. Every line a
// screen spends goes through here (or through a pane that already does it): a
// board whose height fits but whose status line runs off the right edge is the
// same clipping bug with the axes swapped.
func (m model) clipLine(s string) string {
	return ansi.Truncate(s, m.layout.ContentWidth, "…")
}

// vmHandle is a VM's full identity: which connection scope it lives under,
// plus its name. It is the composite key every per-VM in-memory store
// (heartbeatRegistry, jobRegistry via jobKey, and the board's focus ring)
// keys on, in place of a bare name.
//
// Why this exists: a bare name is not a VM's identity, it is a VM's LABEL —
// and two profiles (task 7's fleet) can label two entirely different VMs
// "web". Keying (or pruning) by name alone means a reconcile or delete
// running against one profile can silently act on another profile's VM that
// happens to share the name: stop its heartbeat, evict its retained job, or —
// the HIGH-severity case — delete its host secrets. registry.Scope is a small
// comparable struct (Provider + RemoteTarget), so vmHandle is itself a valid
// map key.
//
// With the fleet (task 7) a model holds one scope PER MEMBER, so a running board
// genuinely carries vmHandles under several scopes at once — which is exactly
// what makes these composite keys load-bearing rather than a type-safety
// harness: a reconcile, a heartbeat teardown, or a delete for one profile's
// "web" keys on (its scope, "web") and cannot reach another profile's.
type vmHandle struct {
	Scope registry.Scope
	Name  string
}

// present indexes a freshly loaded VM list by name, for jobRegistry.reconcile.
func present(vms []vm.VM) map[string]bool {
	set := make(map[string]bool, len(vms))
	for _, v := range vms {
		set[v.Name] = true
	}
	return set
}

// Update dispatches the message, then reconciles two things against whatever
// state that left behind: the board's focus ring, and the guest heartbeats.
//
// Both reconcile after EVERY message rather than at the few places that obviously
// matter, and that is deliberate.
//
// The heartbeat's gate (shouldTick) turns on the active view, the terminal's
// focus, and how long ago the user last typed — three things that change under a
// dozen different messages, in five different files. A gate re-checked at "the
// places that change it" is a gate that is one forgotten `m.view = …` away from
// holding an SSH connection open into every guest on the machine, forever, with
// nobody watching.
//
// The board's roster changes under exactly as many messages: a refresh, a delete,
// a build starting on a VM Lima has never heard of, a filter keystroke. syncBoard
// is what guarantees the ring is always on a VM that is actually on the board —
// and a ring pointing anywhere else is a destructive key pointed at the wrong VM.
// Both cost a map walk over at most a handful of VMs, and neither can be forgotten.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.dispatch(msg)
	nm, ok := next.(model)
	if !ok {
		return next, cmd
	}
	nm.syncBoard()
	// The batch is built into a LOCAL before nm is returned. tickRefresh takes a
	// POINTER receiver and sets nm.refreshing = true; Go orders the function calls in
	// a return statement but not the copy of the plain `nm` operand sitting beside
	// them, so a compiler free to copy nm first would return a model with
	// refreshing=false while a tea.Tick was already armed — and every later message
	// would arm another, stacking refresh loops until the list call rate ran away.
	// gc happens to copy last today. This is the same hazard updateBoard already
	// hoists around (see the note under Quit there); not leaning on it is free.
	cmds := tea.Batch(cmd, nm.syncHeartbeats(), nm.tickRefresh())
	return nm, cmds
}

// dispatch is the single message-handling point. Key messages route by active view;
// all other messages (async results, ticks, blinks) are handled or forwarded to the
// active sub-component.
func (m model) dispatch(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.applySize(msg.Width, msg.Height)
		return m, nil

	case tea.FocusMsg:
		// The terminal came back to the foreground: the user is here, so the idle
		// window restarts and syncHeartbeats (above) reopens the connections without
		// waiting for a keypress.
		//
		// There is no BlurMsg case. Blur used to close the gate, on the theory that a
		// blurred terminal is a backgrounded one — but a terminal beside an editor is
		// blurred and fully visible, so every alt-tab blanked the gauges of a user who
		// was still watching them. See shouldTick.
		m.lastInput = time.Now()
		return m, nil

	case heartbeatSampleMsg:
		// The channel closed: this VM's stream ended. Against a real VM that is what a
		// `limactl stop` looks like from here (the shell dies within ~300ms with `exit
		// status 255`), so it is the ordinary path for a VM going down, not an
		// exceptional one. ended() drops the reading, so the gauge goes with the VM
		// instead of freezing at whatever it last said.
		if !msg.ok {
			m.heartbeats.ended(msg.scope, msg.vm, msg.epoch)
			return m, nil
		}
		// fold returns nil for a sample from a connection that has since been replaced,
		// which ends that stale read loop rather than letting it double up on the live
		// one. Routed by the sample's own scope, so a fleet's same-named VMs never
		// cross streams.
		return m, heartbeatReadCmd(msg.scope, msg.vm, msg.epoch, m.heartbeats.fold(msg.scope, msg.vm, msg.epoch, msg.sample))

	case refreshTickMsg:
		// This member's loop iteration is done; tickRefresh (called centrally after
		// every message — see Update) re-arms the next one at THIS member's cadence
		// iff shouldTick still allows it, which is what makes each member's loop stop
		// on its own once the board is no longer the active, recently-used screen.
		i, ok := m.routeIndex(msg.scope)
		if !ok {
			return m, nil
		}
		m.members[i].arming = false
		if !m.shouldTick() {
			return m, nil
		}
		mem := m.members[i]
		if mem.prov == nil {
			return m, nil
		}
		// Re-preflight only while this member is NOT already connected — its
		// errored-self-heal retry re-runs the handshake, a healthy member just
		// re-lists (the pre-fleet local behaviour).
		return m, refreshCmd(mem.scope, mem.prov, mem.hostFiles, mem.state != connConnected)

	case spinner.TickMsg:
		// The spinner animates for any job in flight (on any VM, whether or not its
		// screen is showing) and for a quick lifecycle action; when neither is
		// happening, the loop ends and spinning goes false so the next action can
		// start a fresh one.
		if !m.acting && !m.jobs.anyRunning() {
			m.spinning = false
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case vmsLoadedMsg:
		// Route the result to the member that produced it (a fleet lists every
		// member on its own tea.Cmd). A zero-scope message — a hand-built test —
		// routes to the active member.
		mi, ok := m.routeIndex(msg.scope)
		if !ok {
			return m, nil
		}
		mem := &m.members[mi]
		sc := mem.scope

		// A member the user just DISABLED (task 8's live mutation) has no live
		// binding any more — its provider is nil'd out precisely so nothing new
		// gets kicked, but a refresh that was ALREADY in flight when the disable
		// landed can still deliver its result afterward. That stale result — success
		// or failure alike — must not resurrect the member: every branch below
		// would otherwise flip it back to connConnected/connErrored the instant it
		// arrives, silently undoing the disable the user just asked for.
		if mem.state == connDisabled {
			return m, nil
		}

		// The host capacity is sampled whether or not the LISTING succeeded, so
		// adopt it before the error branches. It used to be adopted only on
		// success — so during a clone window (lima#5236), which is exactly when a
		// 20GiB base image is being copied onto the disk, the header's "free disk"
		// figure froze at its startup value and told the user nothing was consumed.
		if msg.hostMem > 0 {
			mem.host.mem = msg.hostMem
		}
		if msg.hostDiskFree > 0 {
			mem.host.diskFree = msg.hostDiskFree
		}
		if msg.hostCPUs > 0 {
			mem.host.cpus = msg.hostCPUs
		}
		if msg.hostUser != "" {
			mem.host.user = msg.hostUser
		}
		if msg.hostMemAvail > 0 {
			mem.host.memAvail = msg.hostMemAvail
		}
		if msg.hostDiskTotal > 0 {
			mem.host.diskTotal = msg.hostDiskTotal
		}
		if msg.err != nil {
			// A list that failed ONLY because another instance is being cloned or
			// deleted is not a failure — it is lima-vm/lima#5236, and it is the normal
			// state of the world for most of a create or a reset.
			//
			// `limactl clone` creates the instance directory before writing its
			// lima.yaml, and `limactl list` aborts on the first instance it cannot load
			// instead of skipping it, so EVERY listing fails for the 40–60s the clone
			// takes. (`limactl delete` opens the same window, briefly.) Reporting that
			// ten times over would bury the build the user is actually watching under
			// errors about a VM that is coming up exactly as intended.
			//
			// The listing sand already has stays on screen — the other VMs have not
			// changed, and the building VM's own tile comes from the job registry, not
			// from Lima — and the condition is stated ONCE, when it starts. The clone
			// window is per MEMBER: a clone in flight on one profile must not suppress
			// another's errors, so the counter lives on the member.
			// …but the suppression is BOUNDED, and it has to be. The signature is an
			// error string, and it cannot tell a clone in flight from an instance
			// directory that is permanently broken — a clone that was killed leaves
			// exactly the same half-written directory, and `limactl list` then fails
			// FOREVER. A clone window is 40-60s; anything still failing after
			// listRaceLimit is not a window, and the real error is surfaced instead.
			//
			// A clone window does NOT flip the member to errored (it is the normal
			// state of a create, and its last-known list is still valid): the member
			// keeps its current state and cadence.
			if errors.Is(msg.err, lima.ErrListRacedInstanceDir) {
				mem.listRace++
				switch {
				case mem.listRace == 1:
					m.logMsg("VM list paused while another instance is cloned or deleted (lima#5236)")
				case mem.listRace == listRaceLimit:
					m.logMsg("VM list STILL failing — this is no longer a clone window. " +
						"An instance directory is broken; remove it and sand will recover: " + msg.err.Error())
				}
				return m, nil
			}
			// A REAL failure: mark the member errored and advance its backoff so the
			// next refresh tick retries at a longer interval (self-heal). Its
			// last-known VM list stays DORMANT — rendered, but not reconciled or
			// pruned, since a failed list is no evidence a VM was deleted.
			// An INTERRUPTION (a member that was connected going dark) logs
			// "reconnecting" — the backoff loop below is already doing exactly
			// that. A first-connect failure is not a REconnect and stays with
			// the plain "list failed" line.
			if mem.state == connConnected && mem.profile.Type == profiles.TypeRemoteSSH {
				m.logMsg("reconnecting to " + mem.profile.Name + "…")
			}
			mem.listRace = 0
			mem.state = connErrored
			mem.lastErr = msg.err
			mem.backoff++
			m.logMsg("list failed: " + msg.err.Error())
			// A member turning errored can change how many header bands the fleet
			// wants (task 10) — re-run the same budgeting a resize would, at the
			// terminal's CURRENT size, so the errored banner gets a row without
			// waiting for the user to resize the terminal.
			m.applySize(m.width, m.height)
			return m, nil
		}
		// SUCCESS: the member is connected; reset its self-heal cadence and adopt
		// the fresh list. Log the TRANSITION into connected (never the steady
		// state — this handler runs every refresh): a first connect says
		// "connected", a recovery from an interruption says "reconnected",
		// matching the "reconnecting" line the failure path logged. The local
		// member stays silent (see New).
		if mem.state != connConnected && mem.profile.Type == profiles.TypeRemoteSSH {
			if mem.state == connErrored {
				m.logMsg("reconnected to " + mem.profile.Name)
			} else {
				m.logMsg("connected to " + mem.profile.Name)
			}
		}
		mem.listRace = 0
		mem.state = connConnected
		mem.lastErr = nil
		mem.backoff = 0
		mem.vms = msg.vms // DiskUsed / UpSince / LastUsed sampled in refreshCmd, off the Update goroutine
		// everListed latches on the FIRST success and never clears — see its
		// doc comment (fleet.go) and boardReady.
		mem.everListed = true

		// Rules 1+2 of the low-capacity-warning feature: a CONNECTED member (which
		// this now is) whose host memory or disk has crossed below 5% free gets
		// ONE warning in the session's Messages log, edge-triggered so this
		// running on every refresh cannot spam it — see hostwarn.go.
		m.checkHostCapacityWarn(mem)

		// ORDER MATTERS, and it is enforced by the data, not by this comment.
		//
		// The job registry goes FIRST. It knows which absences are legitimate — a
		// build whose clone has not landed, a reset that deleted its own VM — and it
		// hands those names back as `protected`. Everything downstream then treats
		// them as present. All of it is SCOPED to this member (sc): a listing for one
		// profile has no opinion about another profile's same-named VM, and reconcile
		// feeds straight into a host-secrets deletion below — so a cross-scope prune
		// here would delete the wrong VM's GH_TOKEN.
		listed := present(msg.vms)
		reaped, protected := m.jobs.reconcile(sc, listed)
		if len(reaped) > 0 {
			m.logMsg("canceled the run for " + strings.Join(reaped, ", ") + ": the VM disappeared")
			// Nothing left to show for the run the user was watching.
			if m.view == viewProgress && !m.jobs.exists(m.progressJob) {
				m.view = viewBoard
			}
		}

		// Reconcile this member's managed index against reality so a VM deleted
		// outside the TUI stops being flagged managed (and recreate-able) — but a VM
		// the registry just vouched for counts as present. Shared with the headless
		// `sand create` path (internal/manage) so the two entrypoints cannot drift.
		live := msg.vms
		for _, name := range protected {
			live = append(live, vm.VM{Name: name})
		}
		dropped, err := manage.Reconcile(m.reg, live, sc)
		if err != nil {
			m.logWarn("could not update managed index: " + err.Error())
		}
		// A VM that vanished outside the TUI (and so got dropped above) also loses
		// its host-stored secrets — there is no guest left to apply them to, and
		// keeping them around risks silently reattaching stale secrets to an
		// unrelated VM that later reuses the name. Best-effort. Scoped to sc — this
		// reconcile only reasoned about VMs in this member's scope, so the secret it
		// prunes must be scoped identically, never a bare name matching another
		// profile's same-named VM.
		for _, name := range dropped {
			_ = m.sec.Remove(name, sc)
		}
		// The board's tiles come straight off the members' vms + the registry + the
		// job registry, so there is nothing to rebuild — only the focus ring has to
		// be re-pinned against a fleet that may have gained or lost VMs, which Update
		// does centrally (syncBoard) after this returns.
		//
		// A member turning connected can also change how many header bands the
		// fleet wants (task 10's per-profile stats bands) — re-run the same
		// budgeting a resize would, so the fleet's FIRST successful list already
		// gets its band without waiting on a resize.
		m.applySize(m.width, m.height)
		return m, nil

	case actionDoneMsg:
		m.acting = false // the action finished; stop the list spinner
		// The action carries the OWNING member's scope, so a delete prunes — and the
		// follow-up refresh re-lists — the right profile. Only a ZERO-VALUE scope (a
		// hand-built test action, which never tags one) falls back to the active
		// member — mirroring routeIndex's own hardening (fleet.go) for vmsLoadedMsg.
		//
		// A genuinely-tagged scope that no longer matches any CURRENT member (the
		// profile was deleted, or connection-edited and rebuilt, while this action —
		// a lifecycle action, not a job, so the idle gate never blocked it — was
		// still in flight) must be used AS-IS for every prune below, never
		// substituted for the active member's scope: falling back here would prune
		// (RemoveScoped / secrets.Remove) whatever profile happens to be active right
		// now, which is a different VM's registry entry and host secrets than the one
		// this action actually acted on. refreshMemberCmd already reports "nothing to
		// refresh" for such an orphaned scope via routeIndex, so no extra guard is
		// needed there.
		sc := msg.scope
		if sc == (registry.Scope{}) {
			sc = m.activeScope()
		}
		label := msg.action + " " + msg.name
		var text string // built below, then logged ONCE at the end (see the warn append)
		switch {
		case msg.err != nil:
			text = label + " failed: " + msg.err.Error()
		case msg.action == "shell":
			// returned from the interactive shell; nothing to report — logMsg("")
			// below is a deliberate no-op, not a clear (a log has nothing to clear).
		case msg.action == "delete":
			// Nothing to navigate away from: the board is the only screen a delete can
			// be fired from, and syncBoard hands the ring to a neighbour once the
			// refresh drops the tile.
			//
			// The user acted on this VM in the most final way there is, so its
			// retained run goes with it: a failed build's sticky Failed status exists
			// to be acted on, and deleting the VM is acting on it. (A run still in
			// flight is cancelled by remove — Delete is gated on !vmBuilding, so that
			// only happens if the VM was deleted from outside sand.)
			m.jobs.remove(sc, msg.name)
			// A deleted VM is no longer managed, and its host-stored secrets no
			// longer have a guest to apply to; drop it from both indexes. Neither
			// failure may silently shadow the other. Scoped to sc (the deleted VM's
			// own member) — an unscoped Remove would target LocalScope and leave a
			// remote VM's real entry dangling in the index.
			regErr := m.reg.RemoveScoped(sc, msg.name)
			// Scoped to the deleted VM's own member: never a bare name that could
			// match a same-named VM under a different profile.
			secErr := m.sec.Remove(msg.name, sc)
			switch {
			case regErr != nil && secErr != nil:
				text = label + " ok (warning: managed index not updated: " + regErr.Error() + "; secrets not pruned: " + secErr.Error() + ")"
			case regErr != nil:
				text = label + " ok (warning: managed index not updated: " + regErr.Error() + ")"
			case secErr != nil:
				text = label + " ok (warning: secrets not pruned: " + secErr.Error() + ")"
			default:
				text = label + " ok"
			}
		default:
			text = label + " ok"
		}
		// A non-fatal ApplySecrets failure (start/restart/apply-secrets) is
		// appended to whatever success text the switch above built; it must never
		// run on the failure branch, which already carries its own error message.
		if msg.err == nil && msg.warn != "" {
			text += " (warning: " + msg.warn + ")"
		}
		m.logMsg(text)
		return m, m.refreshMemberCmd(sc) // refresh the acted member after every action

	case pasteResultMsg:
		// A plain status-line result (task 5): no spinner to clear (pasteCmd never
		// goes through beginAction — see its doc comment), no view change, no
		// refresh — a clipboard write changes nothing the board's tiles render.
		switch {
		case msg.err != nil:
			m.logMsg(msg.err.Error())
		case msg.result.Status == paste.Staged:
			m.logMsg("staged image on " + msg.name + " — press S then Ctrl-V")
		default: // paste.NoImage
			m.logMsg("no image on clipboard")
		}
		return m, nil

	case provisionOutputMsg:
		// The chunk is keyed by RUN — the VM and which of its runs — so N jobs can
		// stream at once without their output crossing streams, including a VM's
		// build and a copy against that same VM. addOutput reports false for a job
		// that was reaped mid-flight: its late chunks are dropped and its reader is
		// not re-issued, which is what ends that job's read loop.
		if !m.jobs.addOutput(msg.job, msg.chunk) {
			return m, nil
		}
		if m.view == viewProgress && m.progressJob == msg.job {
			m.setOutput()
		}
		return m, readNextCmd(msg.job, m.jobs.reader(msg.job))

	case provisionDoneMsg:
		// finish retains the job — with its log — whether it succeeded or failed:
		// that is what makes the tile's Failed status sticky and its log
		// reopenable. It reports false only for a job already reaped, whose done
		// message is stale.
		job, ok := m.jobs.finish(msg.job, msg.err)
		if !ok {
			return m, nil
		}
		if m.view == viewProgress && m.progressJob == msg.job {
			m.setOutput()
		}
		// A user-canceled run leaves partial state behind; don't record it as
		// managed and don't surface its (kill-induced) error as a failure. Refresh
		// the OWNING member (the build's scope), not the active one.
		if job.Canceled {
			return m, m.refreshMemberCmd(msg.job.scope)
		}
		// A successful create/recreate yields a sand-managed VM; record it
		// (with its config, for a faithful future recreate) so the list marks it
		// and recreate stays available for it. Shared with the headless
		// `sand create` path (internal/manage) so the two entrypoints cannot drift.
		//
		// A COPY IS NOT A BUILD, and the KIND is what says so — not the presence of a
		// config, which is a property of the VM's build and outlives it. A copy
		// finishing against a VM that also holds a retained build would otherwise
		// re-record that build's config (and re-seed its GH_TOKEN) as if the copy had
		// produced it.
		cfg, isProvision := m.jobs.config(msg.job.scope, msg.job.vm)
		var applyCmd tea.Cmd
		if msg.err == nil && msg.job.kind == kindProvision && isProvision && cfg.Name != "" {
			if err := manage.RecordSuccess(m.reg, cfg, msg.job.scope); err != nil {
				m.logMsg("VM ready, but recording it as managed failed: " + err.Error())
			}
			// Task 9: a successful CREATE (never a Reset — job.Recreates is what
			// tells the two apart; a reset targets its VM's own already-fixed
			// member, not a profile the user picked from the create form's
			// selector) persists the profile it targeted as last-used, so the
			// create form defaults to it next time it opens.
			if !job.Recreates && m.profileStore != nil {
				if mem, ok := m.memberByScope(msg.job.scope); ok {
					if err := m.profileStore.SetLastUsed(mem.profile.ID); err != nil {
						m.logMsg("VM ready, but could not record its profile as last-used: " + err.Error())
					}
				}
			}
			// The create form's token becomes the VM's GH_TOKEN secret, so it can
			// be edited later without a rebuild. It never enters the managed
			// registry, which strips CloneToken by design (registry.Add). Seeding
			// it here — in the TUI, on provisionDoneMsg — rather than inside
			// internal/provision keeps the host secrets store a TUI concern and
			// needs no new provision→secrets import; the headless `sand create`
			// path has no store to seed from, so doing this in the provisioner
			// would silently diverge the two entrypoints (Option A, rejected).
			// m.sec.Get already returns a defensive copy, so mutating pairs here
			// cannot corrupt the store ahead of Set validating it.
			if cfg.CloneToken != "" {
				pairs := m.sec.Get(cfg.Name, msg.job.scope)
				pairs["GH_TOKEN"] = cfg.CloneToken
				if err := m.sec.Set(cfg.Name, msg.job.scope, pairs); err != nil {
					m.logMsg("VM ready, but the token could not be saved as a secret: " + err.Error())
				}
			}
			// createVM/Reset each end with their own StartStreaming, which runs
			// BEFORE this handler and before GH_TOKEN lands in the store above —
			// so the guest would have no secrets.env until the VM's *next* start.
			// Dispatch the apply now (batched with the list refresh) so a user who
			// creates a VM and immediately shells in finds GH_TOKEN already set.
			user, scopes := m.secretsFor(msg.job.scope, cfg.Name)
			applyCmd = applySecretsCmd(m.provFor(msg.job.scope), msg.job.scope, cfg.Name, user, scopes)
		}
		refresh := m.refreshMemberCmd(msg.job.scope) // refresh the build's own member
		if applyCmd != nil {
			return m, tea.Batch(refresh, applyCmd)
		}
		return m, refresh

	case toolsetLoadedMsg:
		// The read was kicked (openForm/cycleFormProfile, form.go) for the scope
		// the form was targeting AT THE TIME — never the Update goroutine, since
		// resolving it reads a remote profile's HostFiles over SSH (see
		// formToolsetCmd). By the time it comes back the user may have closed the
		// form, switched to reset mode (which has no tool toggles of its own), or
		// cycled the selector on to a DIFFERENT profile — any of which makes this
		// result stale. Applying it anyway would clobber the CURRENTLY selected
		// profile's toggles with a read that belongs to a profile the user has
		// since left.
		if m.view != viewForm || m.resetMode || m.formScope != msg.scope {
			return m, nil
		}
		if msg.ok {
			cfg := vm.DefaultCreateConfig()
			cfg.ApplyToolset(msg.toolset)
			m.toolClaude = cfg.WithClaude
			m.toolDDEV = cfg.WithDDEV
			m.toolGo = cfg.WithGo
			m.toolJava = cfg.WithJava
		}
		return m, nil

	case tea.KeyPressMsg:
		// Any key is proof someone is still there, which is half of the idle gate (see
		// shouldTick). It is also what WAKES a session that went idle: no timer runs
		// while sand is idle — that is the point of being idle — so the keypress that
		// says "I'm back" is the message that reopens the heartbeats.
		m.lastInput = time.Now()

		if msg.String() == "ctrl+c" {
			// On the progress screen, ctrl+c cancels the job being SHOWN — and only
			// that one, killing the limactl subprocess it is blocked on — rather than
			// quitting the whole TUI and orphaning a half-built VM. Cancellation is
			// per-job now: with several builds in flight, the one on screen is the one
			// the user is aiming at. Everywhere else — including a finished progress
			// screen — ctrl+c quits.
			if m.view == viewProgress && m.jobs.cancelJob(m.progressJob) {
				m.setOutput()
				return m, nil
			}
			return m, tea.Quit
		}
		switch m.view {
		case viewBoard:
			return m.updateBoard(msg)
		case viewForm:
			return m.updateForm(msg)
		case viewProgress:
			return m.updateProgress(msg)
		case viewBrowse:
			return m.updateBrowse(msg)
		case viewDest:
			return m.updateDest(msg)
		case viewSecrets:
			return m.updateSecrets(msg)
		case viewProfiles:
			return m.updateProfiles(msg)
		case viewProfileForm:
			return m.updateProfileForm(msg)
		case viewHelp:
			return m.updateHelp(msg)
		}
	}

	return m.forward(msg)
}

// setOutput wraps the SHOWN job's accumulated output to the viewport width and
// pins the view to the bottom. The buffer comes from the job registry, not from
// the model, which is what lets the user leave a build and come back to a log
// that kept filling while they were away. The bubbles viewport truncates lines
// wider than its width; wrapping first keeps long lines — notably Ansible error
// paths — fully readable as the user scrolls. ansi.Wrap breaks over-long
// unbreakable tokens (e.g. file paths) and preserves the output's ANSI colour
// codes.
//
// Security note (terminal escape injection): job.Output is untrusted guest
// output stored verbatim (see jobRegistry.addOutput) — the guest runs untrusted
// code, so this buffer can contain arbitrary terminal control sequences, and
// ansi.Wrap preserves them rather than stripping them. That is safe here only
// because Bubble Tea v2 renders through a cell-diffing compositor (ultraviolet):
// it re-emits solely printable content, SGR styling, and OSC 8 hyperlinks, and
// discards everything else — OSC 52 (clipboard write), window-title, cursor, and
// DCS sequences never reach the terminal. The protection is incidental to the
// renderer, NOT a sanitization step on this path. If this buffer is ever written
// to the terminal another way (tea.Println, a debug dump, or a non-cell
// renderer), strip it first with ansi.Strip — as feed()'s Ansible-progress parser
// already does in ansible.go — or a malicious guest can inject those sequences.
func (m *model) setOutput() {
	w := m.viewport.Width()
	if w < 1 {
		w = 80
	}
	out := ""
	if job, ok := m.shownJob(); ok {
		out = job.Output
	}
	m.viewport.SetContent(ansi.Wrap(out, w, ""))
	m.viewport.GotoBottom()
}

// forward delegates non-key, non-handled messages (blinks, internal ticks) to
// whichever sub-component the active view owns. The board owns none: its tiles are
// rendered from model state on every frame, not by a stateful sub-component with
// its own message loop — which is exactly what the table it replaced was.
func (m model) forward(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.view {
	case viewForm:
		cmds := make([]tea.Cmd, len(m.inputs))
		for i := range m.inputs {
			m.inputs[i], cmds[i] = m.inputs[i].Update(msg)
		}
		return m, tea.Batch(cmds...)
	case viewProgress:
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	case viewBrowse:
		// Deliver the browser's own async dirLoadedMsg (an internal browse type)
		// and any list ticks by forwarding every non-key message to it.
		m.browser, cmd = m.browser.Update(msg)
		return m, cmd
	case viewDest:
		m.dest, cmd = m.dest.Update(msg)
		return m, cmd
	case viewSecrets:
		m.secretsArea, cmd = m.secretsArea.Update(msg)
		return m, cmd
	case viewProfileForm:
		cmds := make([]tea.Cmd, len(m.profileInputs))
		for i := range m.profileInputs {
			m.profileInputs[i], cmds[i] = m.profileInputs[i].Update(msg)
		}
		return m, tea.Batch(cmds...)
	default:
		return m, nil
	}
}

// updateConfirm routes keys while a destructive-action confirmation is
// pending. updateBoard, updateDetail and updateProgress each hand off to it first
// whenever m.confirm != nil, so no view carries its own overlay key-handling.
// Only the bound Confirm key ('y') dispatches the pending run; every other
// key — including a stray repeat of whatever key raised the overlay — is
// swallowed, so an accidental double-tap can never fire the action twice.
func (m model) updateConfirm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Confirm): // y
		run := m.confirm.run
		// Log the in-flight label so the spinner (raised by beginAction) has a
		// message to sit beside — otherwise a confirmed stop-all/delete would spin
		// against an empty or stale status line.
		if m.confirm.working != "" {
			m.logMsg(m.confirm.working)
		}
		m.confirm = nil
		return m, m.beginAction(run)
	case key.Matches(msg, m.keys.Cancel): // n / esc
		m.confirm = nil
		return m, nil
	}
	return m, nil
}

// confirmView renders the pending confirmation prompt. Shared by boardView,
// detailView and progressView so no screen formats its own overlay text — and
// clipped to ContentWidth like every other line, since a prompt that wrapped
// would cost the screen a row it never budgeted.
func (m model) confirmView() string {
	return m.clipLine(errStyle.Render(m.confirm.prompt + "  [y] yes   [n] cancel"))
}

// View renders the active screen. v2 moved the alt-screen toggle from a
// program option (tea.WithAltScreen(), the v1 entrypoint) into this View
// field, so it is set here instead of in cmd/sand/main.go.
func (m model) View() tea.View {
	var content string
	switch m.view {
	case viewForm:
		content = m.formView()
	case viewProgress:
		content = m.progressView()
	case viewBrowse:
		content = m.browser.View()
	case viewDest:
		content = m.destView()
	case viewSecrets:
		content = m.secretsView()
	case viewProfiles:
		content = m.profilesView()
	case viewProfileForm:
		content = m.profileFormView()
	case viewHelp:
		content = m.helpView()
	default:
		content = m.boardView()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	// Ask the terminal to report focus. The FocusMsg it delivers is what lets a user
	// returning to a long-idle window see live gauges again without pressing a key
	// (see shouldTick — blur is NOT part of the gate). A terminal that does not
	// support focus reporting simply never sends one, and nothing depends on it: the
	// gate is then driven by keypresses alone.
	v.ReportFocus = true
	return v
}

package ui

// board.go is sand's HOME SURFACE and its ONLY roster: a grid of tiles (task
// 07 renders one; this file composes them), a focus ring that moves in two
// dimensions, and the single-key verbs that act on the tile under the ring.
// There is no second render path, no table, and no toggle back to one.
//
// Two properties are non-negotiable, and they are why this is a file of its own
// rather than "render some cards":
//
//   - STABLE ORDER. Tiles sort ALPHABETICALLY, and a VM changing state does not
//     move it. Grouping running-first is rejected on purpose: at ≤10 VMs the
//     whole fleet is on screen, so grouping saves no scanning — while re-sorting
//     on a state transition makes pressing `x` teleport the focused tile across
//     the board as a DIRECT SIDE EFFECT OF THE VERB THE USER JUST PRESSED, at
//     exactly the moment they are most likely to press another key. If you find
//     yourself sorting by status, stop.
//
//   - IDENTITY-PINNED FOCUS. The ring tracks the VM's NAME, never the slot
//     index. This is the difference between a board that is safe to hold a
//     destructive key on and one that is not, and the failure is silent and
//     severe: the user arrows to prod-box, a refresh tick lands, the fleet
//     reorders, and `d` deletes dev-box. Every action key's old contract —
//     "table.SelectedRow()[0] is the name" — is replaced by exactly one thing:
//     focusedVM(), the VM under the ring.
//
// The roster is MANAGED CLONES ONLY, ALWAYS: no unmanaged VM, no base image, and
// no `f` toggle to bring either back. That has a real cost, and it is now UNMITIGATED:
// base images and foreign VMs are invisible and unmanageable from the TUI, and the
// header no longer even counts them. The header band used to carry a "1 base, 2
// external hidden" clause for exactly that reason; it was removed on request in
// favour of a live host readout. Manage base images with `limactl` — and if the
// invisibility ever bites, the honest fix is to bring that count back, not to add a
// second roster surface.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lullabot/sandbar/internal/vm"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// boardMove is the help-bar entry for the four arrow keys. Navigation is arrows
// ONLY, and deliberately not vim keys: `l` is the reopen-log verb and `g` is
// download (see commandreg.go), so h/j/k/l cannot all mean movement here. A
// half-vim map that moved vertically but not horizontally would be a trap, so
// the board offers none of it.
var boardMove = key.NewBinding(key.WithKeys("up", "down", "left", "right"), key.WithHelp("↑↓←→", "move"))

// ghostEnter is what the footer advertises for enter while the ring is on the
// empty slot. Same key, different verb — see boardHelp.
var ghostEnter = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "new VM"))

// ghostTileText is the empty-slot affordance. It is retained BECAUSE a 1–3 VM
// board is mostly empty: the dominant state of the target user's board becomes a
// call to action instead of dead space.
const ghostTileText = "press enter to add a VM"

// ghostFocusName is the focus ring's sentinel for the ghost cell.
//
// The ring is pinned to a VM's NAME, not to a grid slot — that is the identity
// pin the whole board rests on (see syncBoard) — so making the empty slot
// selectable means giving it a name. This one contains a NUL byte, which Lima
// cannot produce and will not accept in an instance name, so it can never collide
// with a real VM: focusedVM() looks it up among the VMs, fails to find it, and
// every per-VM verb correctly declines to fire on an empty slot. Ask
// focusIsGhost, never `focusName == ""` — an empty focusName means the ring is on
// NOTHING, which is a different state and still reachable (a filtered board with
// no matches has no ghost either).
const ghostFocusName = "\x00new"

// isBaseImage reports whether name is a sand base image: a clone source for a
// managed VM, or the default base name even before any clone exists. Base images
// are the heavy, identity-free images each VM is cloned from — they are NOT
// workspaces, they get no tile (see boardVMs), and stop-all skips them.
func (m model) isBaseImage(name string) bool {
	return m.reg.IsBase(name) || name == vm.DefaultCreateConfig().BaseName
}

// lookupVM looks up a loaded VM record by name, reporting whether it was found.
// The miss case is distinguishable from a real zero-value record, and both
// callers need that: the VM screen's re-seed must route back to the board when
// its VM is gone, and the board itself must be able to raise a tile for a VM
// that does not exist YET (a create's clone does not land in `limactl list`
// until minutes into its own build — the vm.VM{Name: name} returned here is that
// tile's record).
func (m model) lookupVM(name string) (vm.VM, bool) {
	for _, v := range m.vms {
		if v.Name == name {
			return v, true
		}
	}
	return vm.VM{Name: name}, false
}

// boardVMs is THE ROSTER, alphabetically: every VM that gets a tile, and nothing
// else.
//
// A tile exists iff the VM is a sand-managed clone — with one exception that is
// not a loophole but the point: a VM with a PROVISION JOB gets a tile too.
// A create's VM is absent from `limactl list` for the first minutes of its own
// build, and it is not recorded managed until that build SUCCEEDS, so a roster
// walking only Lima and the registry would show nothing at all for exactly the
// span the user is waiting on — and a FAILED build's VM (never recorded managed)
// would have no tile to report the failure on, or to delete it from.
func (m model) boardVMs() []vm.VM {
	on := make(map[string]bool, len(m.vms))
	for _, v := range m.vms {
		if m.reg.IsManaged(v.Name) {
			on[v.Name] = true
		}
	}
	for _, name := range m.jobs.names() {
		if m.hasProvisionJob(name) {
			on[name] = true
		}
	}

	out := make([]vm.VM, 0, len(on))
	for name := range on {
		v, _ := m.lookupVM(name) // a miss is a VM being built: its record is its name
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// visibleVMs is the roster narrowed by the live name search ('/'). It is what the
// grid renders and what the focus ring moves over — and it is deliberately NOT
// what stop-all acts on (see stopAllTargets).
func (m model) visibleVMs() []vm.VM {
	vms := m.boardVMs()
	if m.searchQuery == "" {
		return vms
	}
	q := strings.ToLower(m.searchQuery)
	out := vms[:0:0]
	for _, v := range vms {
		if strings.Contains(strings.ToLower(v.Name), q) {
			out = append(out, v)
		}
	}
	return out
}

// vmIndex is the slot a name occupies in vms, or -1.
func vmIndex(vms []vm.VM, name string) int {
	for i, v := range vms {
		if v.Name == name {
			return i
		}
	}
	return -1
}

// focusedVM is THE single-sourced contract every verb on this board depends on:
// the VM under the ring. It reports false when the board is empty (or the ring's
// VM is not on it), and every caller must respect that rather than acting on a
// zero-value VM.
func (m model) focusedVM() (vm.VM, bool) {
	vms := m.visibleVMs()
	i := vmIndex(vms, m.focusName)
	if i < 0 {
		return vm.VM{}, false
	}
	return vms[i], true
}

// syncBoard re-establishes the focus invariant — THE RING IS ALWAYS ON A VM THAT
// IS ACTUALLY ON THE BOARD — and is called after EVERY message (see Update), for
// the same reason syncHeartbeats is: the roster changes under a refresh, a
// delete, a build starting, and a filter keystroke, in four different files, and
// a rule re-checked only "where it obviously matters" is one forgotten call away
// from a ring pointing at a VM the user cannot see.
//
// A VM that is still on the board KEEPS the ring, whatever slot it now occupies.
// That is the identity pin, and it is the whole point.
func (m *model) syncBoard() {
	vms := m.visibleVMs()
	if len(vms) == 0 {
		// No tiles. The ring goes to the ghost when there is one — an empty board is
		// exactly when the invitation to create a VM should be the selected thing —
		// and to nothing at all when there is not (a filter that matches no VM shows
		// no ghost: the tiles are hidden, not absent, so there is nothing to invite).
		//
		// But only once the FIRST list has landed. Before that the board is empty
		// because nothing is loaded, not because the host is bare, and adopting the
		// ghost here would stick: the identity pin would then hold the ring on it as
		// the real tiles arrived, so sand would open with the empty slot selected and
		// enter would create a VM rather than open the first one.
		if m.showsGhost() && m.vmsLoaded {
			m.focusName = ghostFocusName
		} else {
			m.focusName = ""
		}
		m.scrollRow = 0
		return
	}
	if m.focusIsGhost() {
		m.ensureFocusVisible()
		return
	}
	if vmIndex(vms, m.focusName) < 0 {
		m.focusName = focusNeighbour(vms, m.focusName)
	}
	m.ensureFocusVisible()
}

// focusIsGhost reports whether the ring is on the empty slot. It insists the ghost
// is actually being SHOWN, so a stale sentinel left behind by a filter (which
// hides the ghost) can never be mistaken for a live selection.
func (m model) focusIsGhost() bool {
	return m.focusName == ghostFocusName && m.showsGhost()
}

// focusIndex is the ring's slot in the GRID — the cells gridView actually lays
// out, which is the visible VMs plus the ghost, in that order — or -1 for a ring
// on nothing. Focus movement and scrolling both go through this, so neither can
// disagree with what is on screen about where the ring is.
func (m model) focusIndex() int {
	if m.focusIsGhost() {
		return len(m.visibleVMs())
	}
	return vmIndex(m.visibleVMs(), m.focusName)
}

// focusCellName is focusIndex's inverse: the focus name for a grid slot.
func (m model) focusCellName(i int) string {
	vms := m.visibleVMs()
	if i < len(vms) {
		return vms[i].Name
	}
	return ghostFocusName
}

// focusNeighbour picks where the ring lands when the VM it was on LEAVES the
// board — deleted, or hidden by the search filter. It takes the nearest tile
// alphabetically BEFORE the departed VM, and only when there is none (it was the
// first) the one that is now first. What it never does is leave the ring sitting
// on "whatever now occupies the old slot index": the ring moves to a specific,
// predictable identity, chosen from the board as it is now.
//
// vms is the sorted roster, so the last name that still sorts before `gone` is
// that neighbour.
func focusNeighbour(vms []vm.VM, gone string) string {
	if len(vms) == 0 {
		return ""
	}
	pick := vms[0].Name
	for _, v := range vms {
		if v.Name >= gone {
			break
		}
		pick = v.Name
	}
	return pick
}

// gridColumns is how many tiles fit side by side — from the LAYOUT MODE (task
// 03), never from an offset computed here.
func (m model) gridColumns() int {
	if m.layout.Columns < 1 {
		return 1
	}
	return m.layout.Columns
}

// visibleTileRows is how many rows of tiles the grid viewport shows at once. At
// 80x24 that is two, which is why the grid has to scroll: a power user with ten
// VMs will run past the edge of it.
func (m model) visibleTileRows() int {
	rows := m.layout.GridHeight / tileHeight
	if rows < 1 {
		rows = 1
	}
	return rows
}

// moveFocus walks the ring one tile in the grid's own two dimensions, clamping at
// the board's edges (it never wraps: a ring that wrapped from the last tile to
// the first would put a destructive key over a VM at the opposite end of the
// board). Moving past the viewport's edge SCROLLS — see ensureFocusVisible —
// rather than trapping the ring at the last visible row.
// It walks CELLS, not VMs: the ghost is one of them (gridCells), so the empty slot
// is arrowed onto like any tile and `enter` on it opens the create form. That is
// what makes the invitation reachable rather than merely visible — the affordance
// used to be a printed instruction the ring could never land on.
func (m *model) moveFocus(dx, dy int) {
	n := m.gridCells()
	if n == 0 {
		return
	}
	i := m.focusIndex()
	if i < 0 {
		// The ring is on nothing (a board that just gained its first cell): the first
		// arrow key adopts the first one rather than doing nothing.
		m.focusName = m.focusCellName(0)
		m.ensureFocusVisible()
		return
	}

	cols := m.gridColumns()
	col, row := i%cols, i/cols
	lastRow := (n - 1) / cols
	target := i
	switch {
	case dx < 0 && col > 0:
		target = i - 1
	case dx > 0 && col < cols-1 && i+1 < n:
		target = i + 1
	case dy < 0 && row > 0:
		target = i - cols
	case dy > 0 && row < lastRow:
		if next := i + cols; next < n {
			target = next
		} else {
			// The row below exists but is SHORT (a partial last row): land on its
			// final cell instead of refusing to move.
			target = n - 1
		}
	}
	if target == i {
		return
	}
	m.focusName = m.focusCellName(target)
	m.ensureFocusVisible()
}

// showsGhost reports whether the grid is rendering the empty-slot invitation. It
// is NOT offered while a filter is narrowing the board (the tiles missing there
// are hidden, not absent) — and gridView and ensureFocusVisible both ask here, so
// the cell the grid draws and the cell the scroll accounts for cannot disagree.
// They did, and the affordance paid for it: see ensureFocusVisible.
func (m model) showsGhost() bool { return m.searchQuery == "" }

// gridCells is how many cells the grid lays out: one per visible VM, plus the
// ghost.
func (m model) gridCells() int {
	n := len(m.visibleVMs())
	if m.showsGhost() {
		n++
	}
	return n
}

// ensureFocusVisible scrolls the grid so the focused tile is on screen. This is
// the whole of "focus follows scroll": nothing else moves scrollRow, so the
// viewport cannot drift away from the ring.
//
// THE GHOST CELL IS PART OF THE GRID, and the scroll has to count it. It used to
// count VMs only, so its clamp pinned scrollRow to the last VM's row: at 80x24
// (one column, two visible rows) a board with two VMs filled the viewport and the
// ghost's row could never come on screen. The "press n to add a VM" affordance —
// whose entire rationale is that a 1–3 VM board is mostly empty, so the empty
// space becomes the call to action — was reachable with exactly one VM and never
// again.
//
// Counting it in the clamp is necessary but not sufficient: nothing would ever
// SCROLL there, because the ring only ever moves between VMs and the scroll only
// ever moves to follow the ring. So when the ring lands on the last VM, the grid
// scrolls to the BOTTOM of its content — which is the ghost's row, sitting
// immediately after that VM, and therefore never at the cost of hiding the tile
// the ring is on.
func (m *model) ensureFocusVisible() {
	i := m.focusIndex()
	if i < 0 {
		m.scrollRow = 0
		return
	}
	cols := m.gridColumns()
	rows := m.visibleTileRows()
	row := i / cols

	if row < m.scrollRow {
		m.scrollRow = row
	}
	if row >= m.scrollRow+rows {
		m.scrollRow = row - rows + 1
	}

	// Never scroll past the bottom of the content (a resize that grows the viewport
	// would otherwise leave blank rows above a board pinned to it). The ghost is one
	// of those cells, which is what keeps its row scrollable — and now that the ring
	// can LAND on the ghost, following the ring is all it takes to reach it. This
	// used to need a special case that scrolled to the bottom whenever focus reached
	// the last VM, purely so the unreachable invitation could at least be seen.
	bottom := (m.gridCells()-1)/cols - rows + 1
	if m.scrollRow > bottom {
		m.scrollRow = bottom
	}
	if m.scrollRow < 0 {
		m.scrollRow = 0
	}
}

// stopAllTargets returns the sand-managed VMs that are currently running.
// Base images are excluded: they are kept stopped by design and are a clone
// source, not a workspace — though a base mid-build is running, which is
// exactly why the exclusion is explicit rather than incidental.
//
// This walks m.vms (every loaded VM), NOT the visible tiles: a managed VM hidden
// by an active name filter ('/') must still be stopped. That is a deliberate
// choice — 'X' means "stop all", not "stop what I can currently see" — because
// the opposite reading is defensible and a future reader will wonder.
func (m model) stopAllTargets() []string {
	var names []string
	for _, v := range m.vms {
		if v.Status != limaRunning || !m.reg.IsManaged(v.Name) || m.isBaseImage(v.Name) {
			continue
		}
		names = append(names, v.Name)
	}
	return names
}

// busyVMs names every VM with work still in flight — a build, a file transfer, or
// (in the narrow window where a copy is running against a VM a reset is about to
// rebuild) both — sorted, so the quit confirmation below reads the same way twice.
// A VM is named ONCE however many runs it has: the name is what the user acts on,
// and quitting abandons everything on it.
func (m model) busyVMs() []string {
	var names []string
	for _, name := range m.jobs.names() {
		if m.jobs.isRunning(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// summarizeNames renders up to a width-appropriate number of names for
// display in a confirm prompt, summarizing any remainder as "and N more".
// Display only: every target in names is still acted on regardless of how the
// list is truncated here. Falls back to a sane budget when the terminal size
// has not been reported yet (width == 0).
func summarizeNames(names []string, width int) string {
	budget := width - 40
	if budget < 20 {
		budget = 20
	}
	var b strings.Builder
	shown := 0
	for i, n := range names {
		sep := ""
		if i > 0 {
			sep = ", "
		}
		if b.Len()+len(sep)+len(n) > budget && shown > 0 {
			break
		}
		b.WriteString(sep)
		b.WriteString(n)
		shown++
	}
	if shown < len(names) {
		fmt.Fprintf(&b, " and %d more", len(names)-shown)
	}
	return b.String()
}

// beginAction marks a quick lifecycle action (start/stop/restart/delete) as in
// flight and batches its command with the spinner tick, so the screen shows a
// live spinner beside the status line until the matching actionDoneMsg clears it.
// tickSpinner is what keeps a second key press — or a build already running on
// another VM — from stacking tick loops and spinning the animation at double
// speed.
func (m *model) beginAction(cmd tea.Cmd) tea.Cmd {
	m.acting = true
	return tea.Batch(cmd, m.tickSpinner())
}

// requestQuit is the ONE quit path, and the BOARD IS THE ONLY SCREEN THAT OFFERS
// 'q' AT ALL. Child screens leave with `esc`; a quit key sitting beside them turns
// one mistyped keystroke into the end of the session rather than the end of the
// screen.
//
// Quitting with a build in flight orphans a half-built VM — the job registry (task
// 04) made concurrent background builds possible and left this hole open — so when
// work is in flight, 'q' CONFIRMS instead of quitting. ctrl+c is deliberately left
// as the unconditional escape hatch: it is the key users press when they want out
// regardless, and on the progress screen it cancels the run it is showing.
func (m *model) requestQuit() tea.Cmd {
	busy := m.busyVMs()
	if len(busy) == 0 {
		return tea.Quit
	}
	noun := "VM"
	if len(busy) > 1 {
		noun = "VMs"
	}
	// The count is of VMs, not runs, and the noun says so: a VM can hold two runs at
	// once (jobs.go), and "abandon 1 run" while abandoning two would be a small lie
	// told at the exact moment the user is deciding whether to walk away from work.
	m.confirm = &confirmState{
		prompt: fmt.Sprintf("Quit and abandon work in flight on %d %s (%s)?",
			len(busy), noun, summarizeNames(busy, m.width)),
		run: tea.Quit,
	}
	return nil
}

// updateBoard handles keys on the board. Order matters: a pending confirmation
// owns the keyboard, then the live search owns it (a typing mode and single-key
// verbs compete for the same keystrokes, and the search must win while it is
// open), then navigation and the board's own chrome, and only then the per-VM
// verbs — which come from the COMMAND REGISTRY (commandreg.go), so a verb fires
// iff enabledFor says it applies to the VM under the ring, exactly as it does on
// the VM screen.
func (m model) updateBoard(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.confirm != nil {
		return m.updateConfirm(msg)
	}
	if m.searching {
		return m.updateBoardSearch(msg)
	}

	switch msg.Code {
	case tea.KeyUp:
		m.moveFocus(0, -1)
		return m, nil
	case tea.KeyDown:
		m.moveFocus(0, 1)
		return m, nil
	case tea.KeyLeft:
		m.moveFocus(-1, 0)
		return m, nil
	case tea.KeyRight:
		m.moveFocus(1, 0)
		return m, nil
	}

	switch {
	case key.Matches(msg, m.keys.Back):
		// esc clears a committed name filter — the only thing esc means on the
		// board. With no filter it is a no-op (there is nowhere to go back to from
		// the home surface).
		if m.searchQuery != "" {
			m.searchQuery = ""
			m.syncBoard()
		}
		return m, nil

	case key.Matches(msg, m.keys.Quit):
		// The command goes into a local, and only then is m returned. requestQuit
		// (like openForm and every registry action below) takes a POINTER receiver
		// and mutates m — and Go does not specify whether the bare `m` operand of a
		// return statement is copied before or after a call sitting beside it. Not
		// leaning on that ordering is free; the failure it would buy is a confirm
		// overlay that silently vanishes on the one path guarding a running build.
		cmd := m.requestQuit()
		return m, cmd

	case key.Matches(msg, m.keys.Search):
		m.searching = true
		return m, nil

	case key.Matches(msg, m.keys.New):
		cmd := m.openForm()
		return m, cmd

	case key.Matches(msg, m.keys.Enter):
		// Enter on the empty slot creates a VM. `n` still does it from anywhere on
		// the board — the shortcut is not replaced, it is just no longer the ONLY way
		// in, which is what the ghost's own text used to have to explain.
		if m.focusIsGhost() {
			cmd := m.openForm()
			return m, cmd
		}
		// On a VM tile, enter does NOTHING. It used to open a full-screen VM screen;
		// that screen is gone, because the tile already shows everything it did — the
		// one fact it had that the tile did not, the allocated core count, now rides on
		// the cpu gauge's own label (cpuLabel, tile.go). Every verb it offered fires
		// straight from the board, on the tile under the ring.
		return m, nil

	case key.Matches(msg, m.keys.StopAll):
		targets := m.stopAllTargets()
		if len(targets) == 0 {
			m.logMsg("no running sand VMs to stop")
			return m, nil
		}
		m.confirm = &confirmState{
			prompt:  fmt.Sprintf("Stop %d running sand VMs (%s)?", len(targets), summarizeNames(targets, m.width)),
			run:     stopAllCmd(m.cli, targets),
			working: fmt.Sprintf("stopping %d sand VMs…", len(targets)),
		}
		return m, nil
	}

	v, ok := m.focusedVM()
	if !ok {
		return m, nil
	}
	for _, c := range vmCommands {
		if key.Matches(msg, c.binding) && c.enabledFor(m, v) {
			// There is exactly one source for "which VM is this verb acting on": the
			// registry's own argument, the tile under the ring. Nothing else.
			cmd := c.action(&m, v) // mutates m; see the note under Quit above
			return m, cmd
		}
	}
	return m, nil
}

// updateBoardSearch routes keys while the live name search is open. Typed keys
// build the query and narrow the tiles as they land, taking priority over every
// verb binding and the focus ring's own navigation — otherwise typing "stop-box"
// would start, stop and delete VMs on the way through. ctrl+c never reaches here
// (Update intercepts it), so it still quits.
func (m model) updateBoardSearch(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Code == tea.KeyEsc:
		m.searching = false
		m.searchQuery = ""
	case msg.Code == tea.KeyEnter:
		m.searching = false // keep the query; return to normal navigation
	case msg.Code == tea.KeyBackspace:
		if m.searchQuery != "" {
			r := []rune(m.searchQuery)
			m.searchQuery = string(r[:len(r)-1])
		}
	case msg.Text != "":
		m.searchQuery += msg.Text
	}
	// Every other key (arrows, tab, …) is swallowed: while the search is open it
	// neither navigates nor acts. The focus ring is re-pinned against whatever the
	// query now hides — by identity, exactly as it is across a refresh.
	m.syncBoard()
	return m, nil
}

// boardHelp is the board's footer, derived from THE SAME command registry
// (commandreg.go) the VM screen's footer (detailHelp, detail.go) is, filtered
// by the SAME enabledFor(model, vm.VM) predicate against the tile under the
// focus ring. That is the whole payoff of task 02's registry: the footer
// cannot advertise a verb that would do nothing to the focused tile, and it
// cannot drift from updateBoard's own dispatch loop above (both walk
// vmCommands), because there is exactly one list to walk.
func (m model) boardHelp() []key.Binding {
	if m.confirm != nil {
		return []key.Binding{m.keys.Confirm, m.keys.Cancel}
	}
	if m.searching {
		return []key.Binding{m.keys.Back, m.keys.Enter}
	}
	// Enter is a verb ONLY on the empty slot, where it creates a VM. On a VM tile it
	// does nothing — the screen it used to open is gone — so it is not advertised
	// there. A help bar offering a key that does nothing is exactly the drift the
	// command registry exists to prevent; it read "enter detail" for a while after
	// the VM screen was deleted, which is how this was caught.
	bindings := []key.Binding{boardMove}
	if m.focusIsGhost() {
		bindings = append(bindings, ghostEnter)
	}
	bindings = append(bindings, m.keys.New, m.keys.Search)
	if v, ok := m.focusedVM(); ok {
		for _, c := range vmCommands {
			if c.enabledFor(m, v) {
				bindings = append(bindings, c.binding)
			}
		}
	}
	return append(bindings, m.keys.StopAll, m.keys.Quit)
}

// boardView renders the board top to bottom: the pinned header band, the docked
// messages strip (when the terminal has room for it — see messagesStripView), the
// tile grid, and the footer band (the status line, the search indicator and the
// help bar).
//
// EVERY ROW IT SPENDS IS A ROW CLASSIFY BUDGETED. That is not a nicety: this view
// used to emit two blank separator rows nobody had budgeted for, so at 80x24 it
// rendered 26 lines into a 24-row terminal, bubbletea's alt-screen clipped the
// bottom two, and the entire help bar was invisible at the one size the plan calls
// out as must-work. Each band below renders EXACTLY its budget — HeaderHeight,
// MessagesHeight, GridHeight, FooterHeight — or fewer rows, never more.
func (m model) boardView() string {
	var b strings.Builder
	b.WriteString(m.headerView())
	b.WriteString("\n")
	if strip := m.messagesStripView(); strip != "" {
		b.WriteString(strip)
		b.WriteString("\n")
	}
	b.WriteString(m.gridView())
	b.WriteString("\n")
	b.WriteString(m.footerBandView())
	return appStyle.Render(b.String())
}

// footerBandView renders the board's closing band in EXACTLY FooterHeight rows:
// the activity line (a pending confirmation, the acting spinner, or the last
// logged message), the name-filter indicator, and the help bar — with blank lines
// taking up whatever slack the optional two leave, so the band's height is a
// constant classify can budget and the help bar can never be pushed off the bottom
// of the terminal by a status line appearing.
//
// The help bar goes LAST and the band is clipped from the front, so if a budget
// ever came in short it is the breathing room that goes, never the row that tells
// the user which keys exist.
func (m model) footerBandView() string {
	var lines []string
	if s := m.activityLineView(); s != "" {
		lines = append(lines, s)
	}
	// Surface the name filter so it never hides tiles invisibly: a live prompt
	// while typing, and a persistent indicator (with the key to clear it) once
	// committed with enter.
	switch {
	case m.searching:
		lines = append(lines, m.clipLine(statusStyle.Render("/"+m.searchQuery+"   enter: apply · esc: clear")))
	case m.searchQuery != "":
		lines = append(lines, m.clipLine(statusStyle.Render(fmt.Sprintf("name filter: %q   / edit · esc clear", m.searchQuery))))
	}
	lines = append(lines, m.footerView(m.boardHelp()))

	height := m.layout.FooterHeight
	if height < 1 {
		height = 1
	}
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	// Pad the slack ABOVE the band's lines: the help bar stays at the bottom of the
	// band and the blank rows read as separation from the grid.
	if pad := height - len(lines); pad > 0 {
		lines = append(make([]string, pad), lines...)
	}
	return strings.Join(lines, "\n")
}

// gridView lays the tiles out in the grid the LAYOUT MODE budgets (columns and
// tile width from classify, rows from GridHeight) and scrolls it to the row
// ensureFocusVisible parked it on.
func (m model) gridView() string {
	vms := m.visibleVMs()
	if len(vms) == 0 && m.searchQuery != "" {
		// An empty board because of the FILTER is not an empty board: inviting the
		// user to create a VM here would be a lie about why they see nothing.
		return statusStyle.Render(fmt.Sprintf("no VMs match %q — esc clears the filter", m.searchQuery))
	}

	// The exception-only badge rule (task 07) is a genuine equality test across the
	// fleet ON THE BOARD: a badge answers "is this VM the odd one out", and the
	// tiles beside it are what it is odd against.
	//
	// A VM mid-create does not VOTE in that test. Lima has not reported it yet, so
	// its architecture is not DIFFERENT, it is UNKNOWN — and letting an unknown
	// value disagree with the rest would sprout an arch badge on every tile on the
	// board the moment a build starts. That is the same "a zero standing in for no
	// reading yet" failure the tile's gauges exist to avoid, and it is why the
	// voters are the tiles whose facts Lima has actually reported.
	traits := make([]vmTraits, len(vms))
	voters := make([]vmTraits, 0, len(vms))
	for i, v := range vms {
		traits[i] = m.traitsOf(v)
		if _, known := m.lookupVM(v.Name); known {
			voters = append(voters, traits[i])
		}
	}
	uniform := computeFleetUniformity(voters)

	now := time.Now()
	frame := m.spinner.View()
	cells := make([]string, 0, len(vms)+1)
	for i, v := range vms {
		// The tile reads the VM's BUILD — never "whatever run this VM happens to
		// have". A file copy against a running VM is not a build and must not be able
		// to become one by occupying the same slot (jobs.go).
		job, hasJob := m.jobs.snapshot(provisionKey(v.Name))
		sample, hasSample := m.sampleOf(v.Name)
		cells = append(cells, renderTile(tileInput{
			VM:        v,
			Job:       job,
			HasJob:    hasJob,
			Sample:    sample,
			HasSample: hasSample,
			Traits:    traits[i],
			Uniform:   uniform,
			Focused:   v.Name == m.focusName,
			Width:     m.layout.TileWidth,
			Spinner:   frame,
			Now:       now,
		}))
	}
	// The empty slot after the last tile carries the call to action, and
	// ensureFocusVisible counts it in the scroll so it can actually be reached.
	if m.showsGhost() {
		cells = append(cells, renderGhostTile(m.layout.TileWidth, m.focusIsGhost()))
	}

	cols := m.gridColumns()
	gap := tileGapBlock()
	rows := make([]string, 0, (len(cells)+cols-1)/cols)
	for i := 0; i < len(cells); i += cols {
		end := i + cols
		if end > len(cells) {
			end = len(cells)
		}
		blocks := make([]string, 0, 2*(end-i)-1)
		for j := i; j < end; j++ {
			if j > i {
				blocks = append(blocks, gap)
			}
			blocks = append(blocks, cells[j])
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, blocks...))
	}

	start := m.scrollRow
	if start > len(rows)-1 {
		start = len(rows) - 1
	}
	if start < 0 {
		start = 0
	}
	end := start + m.visibleTileRows()
	if end > len(rows) {
		end = len(rows)
	}
	return clipBlock(strings.Join(rows[start:end], "\n"), m.layout.GridHeight, m.layout.ContentWidth)
}

// traitsOf gathers one VM's exception-only field values for the fleet-uniformity
// rule (task 07).
//
// Base and Managed are resolved the same way the ROSTER resolves them, not
// straight off the registry, because a VM mid-create is not in the registry yet —
// it is not recorded managed until its build SUCCEEDS. Reading the registry
// blindly would paint the one tile the user is actually watching as "base none ·
// external": a VM sand is building this second, labelled as somebody else's. Its
// base comes from the job that is building it (BaseName only — the config also
// carries the clone token, and nothing that renders may reach one), and it is
// sand's by definition, because that is why it has a tile at all.
//
// Managed therefore ends up TRUE for every tile on the board, which makes it
// uniform by construction and the managed/external badge unreachable — exactly
// what tile.go predicts once the board filters to sand's own VMs.
func (m model) traitsOf(v vm.VM) vmTraits {
	base := m.reg.Base(v.Name)
	if base == "" {
		if cfg, ok := m.jobs.config(v.Name); ok {
			base = cfg.BaseName
		}
	}
	return vmTraits{
		Arch:    v.Arch,
		Base:    base,
		Managed: m.reg.IsManaged(v.Name) || m.hasProvisionJob(v.Name),
	}
}

// hasProvisionJob reports whether name has a build — running or retained — and so
// is a VM of sand's whether or not the managed index knows it yet. It is one half
// of the roster's membership rule (see boardVMs).
func (m model) hasProvisionJob(name string) bool {
	return m.jobs.exists(provisionKey(name))
}

// tileGapBlock is the blank column between two adjacent tiles: as many lines as a
// tile is tall, so lipgloss joins them without padding one of them out of shape.
func tileGapBlock() string {
	line := strings.Repeat(" ", tileGap)
	lines := make([]string, tileHeight)
	for i := range lines {
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

// renderGhostTile draws the empty slot's call to action: a tile-shaped, dimmed
// outline of a VM that does not exist yet. It takes the focus ring like any tile
// — the same thick border a focused VM gets, so focus survives NO_COLOR — because
// it is now a cell the ring can land on and press enter against.
func renderGhostTile(width int, focused bool) string {
	inner := tileInnerWidth(width)
	lines := make([]string, tileContentRows)
	for i := range lines {
		lines[i] = tilePad("", inner)
	}
	lines[2] = tileChromeStyle.Render(tilePad(centerText(ghostTileText, inner), inner))

	border := lipgloss.RoundedBorder()
	borderColor := tileGhostBorderColor
	if focused {
		border = lipgloss.ThickBorder()
		borderColor = tileFocusedBorderColor
	}
	style := lipgloss.NewStyle().
		Border(border).
		BorderForeground(borderColor).
		Padding(0, 1)
	return style.Render(strings.Join(lines, "\n"))
}

// centerText left-pads s so it sits centered in width cells (tilePad trims or
// right-pads it to exactly that afterwards).
func centerText(s string, width int) string {
	pad := (width - ansi.StringWidth(s)) / 2
	if pad < 1 {
		return s
	}
	return strings.Repeat(" ", pad) + s
}

// clipBlock truncates a rendered block to at most height lines and width display
// cells, so the grid can never spend more rows OR columns than the layout mode
// budgeted it. Both clips are last-resort honesty rather than routine work: the
// grid slices whole tile rows (so it is already within its row budget) and
// classify's TileWidth already fits its columns — but a terminal narrower than a
// tile's own border-plus-padding floor would otherwise push the board's right edge
// past the terminal's, and lipgloss would happily render it.
func clipBlock(s string, height, width int) string {
	if height < 1 {
		height = 1
	}
	if width < 1 {
		width = 1
	}
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, width, "")
	}
	return strings.Join(lines, "\n")
}

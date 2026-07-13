package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// errAnsibleBoom stands in for a provision that died inside Ansible.
var errAnsibleBoom = errors.New("ansible: task failed")

// The board is managed-clones-only, always, so every test VM has to be recorded
// as managed before it is loaded — otherwise it gets no tile, which is the whole
// point of the filter. Registering BEFORE the load also matters: the
// vmsLoadedMsg handler reconciles the managed index against the list it is
// given, so a managed VM missing from that list is dropped from the index (which
// is how a VM deleted outside sand stops being managed).
func loadManaged(t *testing.T, m model, vms ...vm.VM) model {
	t.Helper()
	for _, v := range vms {
		if err := m.reg.Add(vm.CreateConfig{Name: v.Name, BaseName: "claude-base"}); err != nil {
			t.Fatalf("seed registry with %s: %v", v.Name, err)
		}
	}
	next, _ := m.Update(vmsLoadedMsg{vms: vms})
	return next.(model)
}

// boardNames is the names of the tiles currently on the board, in render order.
func boardNames(m model) []string {
	vms := m.visibleVMs()
	names := make([]string, 0, len(vms))
	for _, v := range vms {
		names = append(names, v.Name)
	}
	return names
}

// indexOf is the slot a name occupies in the rendered order, or -1.
func indexOf(names []string, want string) int {
	for i, n := range names {
		if n == want {
			return i
		}
	}
	return -1
}

// press drives one key through the whole Update path (not updateBoard directly),
// so the test exercises the real dispatch.
func press(t *testing.T, m model, msg tea.Msg) (model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	return next.(model), cmd
}

// actionDone drives a dispatched command to the concrete actionDoneMsg it
// produces. beginAction batches the action with the spinner tick, so the
// possible tea.BatchMsg has to be unwrapped.
func actionDone(t *testing.T, cmd tea.Cmd) actionDoneMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a dispatched command, got nil")
	}
	msg := cmd()
	cmds := []tea.Cmd{func() tea.Msg { return msg }}
	if batch, ok := msg.(tea.BatchMsg); ok {
		cmds = batch
	}
	for _, c := range cmds {
		if c == nil {
			continue
		}
		if done, ok := c().(actionDoneMsg); ok {
			return done
		}
	}
	t.Fatalf("no actionDoneMsg came out of the dispatched command (got %T)", msg)
	return actionDoneMsg{}
}

// NON-NEGOTIABLE PROPERTY 1: STABLE ORDER. Tiles sort alphabetically, and a VM
// changing state does NOT move it. Grouping running-first is rejected: at ≤10
// VMs the whole fleet is on screen so grouping saves no scanning, while
// re-sorting on a state transition makes pressing 'x' teleport the focused tile
// across the board as a direct side effect of the verb the user just pressed —
// at exactly the moment they are most likely to press another key.
func TestBoardOrderIsAlphabeticalAndStableAcrossAStateChange(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40) // wide/tall enough that all three tiles render at once
	m = loadManaged(t, m,
		vm.VM{Name: "web", Status: "Running"},
		vm.VM{Name: "api", Status: "Running"},
		vm.VM{Name: "db", Status: "Running"},
	)

	want := []string{"api", "db", "web"}
	if got := boardNames(m); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("board order = %v, want alphabetical %v", got, want)
	}
	before := indexOf(boardNames(m), "db")

	// Focus the middle tile and stop it — the verb whose side effect would
	// teleport the tile if the board sorted by status.
	m.focusName = "db"
	m, cmd := press(t, m, runeKey('x'))
	if done := actionDone(t, cmd); done.name != "db" {
		t.Fatalf("'x' acted on %q, want the focused VM db", done.name)
	}
	// The refresh that follows the stop reports db's new state.
	m = loadManaged(t, m,
		vm.VM{Name: "web", Status: "Running"},
		vm.VM{Name: "api", Status: "Running"},
		vm.VM{Name: "db", Status: "Stopped"},
	)

	if got := indexOf(boardNames(m), "db"); got != before {
		t.Fatalf("stopping the focused VM moved its tile from slot %d to %d — the order must not depend on status", before, got)
	}
	// And the rendered board agrees: still api, db, web, left-to-right, top-to-bottom.
	view := ansi.Strip(m.boardView())
	ai, di, wi := strings.Index(view, "api"), strings.Index(view, "db"), strings.Index(view, "web")
	if ai < 0 || di < 0 || wi < 0 {
		t.Fatalf("every managed VM should have a tile, got:\n%s", view)
	}
	if !(ai < di && di < wi) {
		t.Fatalf("rendered order is not alphabetical (api@%d db@%d web@%d):\n%s", ai, di, wi, view)
	}
}

// NON-NEGOTIABLE PROPERTY 2 (a): IDENTITY-PINNED FOCUS. A refresh that inserts a
// VM alphabetically BEFORE the focused one leaves the ring on the same NAME —
// even though its slot index has moved. The failure this prevents is silent and
// severe: the user arrows to prod-box, a refresh lands, the list reorders, and
// 'd' deletes dev-box.
func TestFocusIsPinnedToIdentityAcrossARefreshThatReorders(t *testing.T) {
	m := newTestModel(t)
	m = loadManaged(t, m,
		vm.VM{Name: "db", Status: "Running"},
		vm.VM{Name: "web", Status: "Running"},
	)
	m.focusName = "web"
	if got := indexOf(boardNames(m), "web"); got != 1 {
		t.Fatalf("precondition: web should start in slot 1, got %d", got)
	}

	// A refresh brings in a VM that sorts first, sliding every later slot down.
	m = loadManaged(t, m,
		vm.VM{Name: "db", Status: "Running"},
		vm.VM{Name: "web", Status: "Running"},
		vm.VM{Name: "api", Status: "Running"},
	)

	if m.focusName != "web" {
		t.Fatalf("focus = %q after an insertion before it, want it pinned to web", m.focusName)
	}
	if got := indexOf(boardNames(m), "web"); got != 2 {
		t.Fatalf("precondition: web's SLOT should have moved to 2, got %d (the test proves nothing otherwise)", got)
	}
	v, ok := m.focusedVM()
	if !ok || v.Name != "web" {
		t.Fatalf("focusedVM() = (%+v, %v), want web", v, ok)
	}
}

// NON-NEGOTIABLE PROPERTY 2 (b): the focused VM disappearing moves the ring to a
// PREDICTABLE NEIGHBOUR — the nearest tile alphabetically before it — rather
// than leaving the ring on whatever now occupies the old slot index.
func TestFocusFallsToANeighbourWhenTheFocusedVMDisappears(t *testing.T) {
	base := newTestModel(t)
	base = loadManaged(t, base,
		vm.VM{Name: "api", Status: "Running"},
		vm.VM{Name: "db", Status: "Running"},
		vm.VM{Name: "web", Status: "Running"},
	)

	// The middle tile is deleted: the ring falls BACK to api, not forward onto
	// web (which is what now sits in db's old slot).
	m := base
	m.focusName = "db"
	m = loadManaged(t, m,
		vm.VM{Name: "api", Status: "Running"},
		vm.VM{Name: "web", Status: "Running"},
	)
	if m.focusName != "api" {
		t.Fatalf("focus = %q after deleting db, want its neighbour api (web now occupies db's old slot)", m.focusName)
	}

	// The FIRST tile is deleted: there is no preceding neighbour, so the ring
	// takes the one that is now first.
	m2 := base
	m2.focusName = "api"
	m2 = loadManaged(t, m2,
		vm.VM{Name: "db", Status: "Running"},
		vm.VM{Name: "web", Status: "Running"},
	)
	if m2.focusName != "db" {
		t.Fatalf("focus = %q after deleting the first tile, want db", m2.focusName)
	}

	// The last VM goes: nothing is focused, and nothing panics.
	m3 := base
	m3.focusName = "web"
	m3 = loadManaged(t, m3)
	if m3.focusName != "" {
		t.Fatalf("an empty board should focus nothing, got %q", m3.focusName)
	}
	if _, ok := m3.focusedVM(); ok {
		t.Fatal("an empty board must report no focused VM")
	}
}

// NON-NEGOTIABLE PROPERTY 2 (c): a verb dispatched AFTER a refresh reaches the VM
// under the ring — not the one that used to occupy that slot. This is the
// destructive bug the identity pin exists to prevent, asserted on the command
// the key actually dispatches.
func TestVerbAfterARefreshReachesTheVMUnderTheRing(t *testing.T) {
	m := newTestModel(t)
	m = loadManaged(t, m,
		vm.VM{Name: "db", Status: "Running"},
		vm.VM{Name: "web", Status: "Running"},
	)
	m.focusName = "web" // slot 1

	// api slides in first; "slot 1" is now db, but the ring is on web.
	m = loadManaged(t, m,
		vm.VM{Name: "api", Status: "Running"},
		vm.VM{Name: "db", Status: "Running"},
		vm.VM{Name: "web", Status: "Running"},
	)

	m, cmd := press(t, m, runeKey('x')) // stop
	done := actionDone(t, cmd)
	if done.action != "stop" || done.name != "web" {
		t.Fatalf("'x' after a refresh dispatched %s on %q, want stop on web (the VM under the ring)", done.action, done.name)
	}
	if !strings.Contains(m.status, "web") {
		t.Fatalf("status = %q, want it to name the VM the verb reached", m.status)
	}
}

// The board shows managed clones and NOTHING else, always: no unmanaged VM, and
// no base image either. The filter is unconditional — there is no 'f' toggle to
// bring them back. (The cost is accepted deliberately: base images become
// invisible from the TUI, and the mitigation is one string in the header — task
// 09's hidden count — not a second surface.)
func TestBoardShowsManagedClonesOnly(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40)
	if err := m.reg.Add(vm.CreateConfig{Name: "web", BaseName: "claude-base"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "web", Status: "Running"},
		{Name: "web-stray", Status: "Running"},   // someone else's VM
		{Name: "claude-base", Status: "Stopped"}, // the base image every clone comes from
	}})
	m = loaded.(model)

	if got := boardNames(m); len(got) != 1 || got[0] != "web" {
		t.Fatalf("board = %v, want only the managed clone [web]", got)
	}
	view := ansi.Strip(m.boardView())
	if strings.Contains(view, "web-stray") {
		t.Fatalf("an unmanaged VM must get no tile, got:\n%s", view)
	}
	if strings.Contains(view, "claude-base") {
		t.Fatalf("a base image must get no tile, got:\n%s", view)
	}

	// The name search composes with the managed filter — it can only ever narrow
	// it. "web" matches the unmanaged web-stray too, and it still gets no tile.
	m, _ = press(t, m, runeKey('/'))
	for _, r := range []rune{'w', 'e', 'b'} {
		m, _ = press(t, m, runeKey(r))
	}
	if got := boardNames(m); len(got) != 1 || got[0] != "web" {
		t.Fatalf("search = %v, want the managed filter to still hold [web]", got)
	}
}

// A VM being CREATED does not appear in `limactl list` until its clone lands,
// minutes into its own build — and it is not recorded managed until the build
// SUCCEEDS. It must still get a tile the moment the build starts: that (press n,
// a building tile appears) is the signature moment of the whole board.
func TestBuildingVMGetsATileBeforeLimaKnowsIt(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40)
	seedJob(t, &m, "newvm", vm.CreateConfig{Name: "newvm", BaseName: "claude-base"})
	m.view = viewBoard

	// A refresh that knows nothing about newvm (it does not exist yet).
	loaded, _ := m.Update(vmsLoadedMsg{vms: nil})
	m = loaded.(model)

	if got := boardNames(m); len(got) != 1 || got[0] != "newvm" {
		t.Fatalf("board = %v, want a tile for the VM being built [newvm]", got)
	}
	view := ansi.Strip(m.boardView())
	if !strings.Contains(view, "newvm") || !strings.Contains(view, "Building") {
		t.Fatalf("the tile should show newvm as Building, got:\n%s", view)
	}
}

// The exception-only badges (task 07) must not fire on facts a VM does not have
// YET. A VM mid-create is absent from `limactl list` and from the managed index,
// so reading its arch/base/managed straight off those sources reports an unknown
// as a DISAGREEMENT — and the whole board sprouts "arch … · base … · managed"
// badges the moment a build starts, with the building tile itself labelled
// "external": sand calling the VM it is building somebody else's.
func TestABuildInFlightDoesNotSproutBadgesOnEveryTile(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40)
	m = loadManaged(t, m,
		vm.VM{Name: "api", Status: "Running", Arch: "x86_64"},
		vm.VM{Name: "web", Status: "Running", Arch: "x86_64"},
	)
	// The GRID, not the whole screen: the help bar's "/ search" contains "arch".
	before := ansi.Strip(m.gridView())
	if strings.Contains(before, "arch") || strings.Contains(before, "base ") || strings.Contains(before, "managed") {
		t.Fatalf("a uniform fleet must show no badges at all, got:\n%s", before)
	}

	// A create starts. Its VM exists nowhere yet — not in Lima, not in the index.
	seedJob(t, &m, "newvm", vm.CreateConfig{Name: "newvm", BaseName: "claude-base"})
	m.view = viewBoard
	after := ansi.Strip(m.gridView())

	if !strings.Contains(after, "newvm") {
		t.Fatalf("precondition: the VM being built should have a tile, got:\n%s", after)
	}
	if strings.Contains(after, "arch") {
		t.Fatalf("a VM whose arch is UNKNOWN must not read as a fleet that disagrees about arch, got:\n%s", after)
	}
	if strings.Contains(after, "external") {
		t.Fatalf("the VM sand is building right now is not external, got:\n%s", after)
	}
	if strings.Contains(after, "base ") {
		t.Fatalf("the build clones the same base as the rest of the fleet, so no base badge is warranted, got:\n%s", after)
	}
}

// A VM sand built is sand's, whether or not the managed index has caught up.
// A FAILED build is the sharp case: Lima knows its half-built VM (so it VOTES in
// the fleet-uniformity test) while the index never records it — RecordSuccess only
// runs on success. Read its traits blindly off the index and it votes "not
// managed, no base", which makes the whole board disagree with itself: every tile
// grows managed/base badges, and the failed tile — the one the user has to act on
// — is labelled "external", sand calling its own wreckage somebody else's.
func TestASandBuiltVMIsNeverLabelledExternal(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40)
	m = loadManaged(t, m, vm.VM{Name: "api", Status: "Running", Arch: "x86_64"})

	seedJob(t, &m, "newvm", vm.CreateConfig{Name: "newvm", BaseName: "claude-base"})
	m.view = viewBoard
	if _, ok := m.jobs.finish("newvm", errAnsibleBoom); !ok {
		t.Fatal("precondition: the seeded build should finish")
	}
	// The refresh now reports the half-built VM Lima was left holding.
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "api", Status: "Running", Arch: "x86_64"},
		{Name: "newvm", Status: "Stopped", Arch: "x86_64"},
	}})
	m = loaded.(model)

	grid := ansi.Strip(m.gridView())
	if !strings.Contains(grid, "Failed") {
		t.Fatalf("precondition: the failed build should have a Failed tile, got:\n%s", grid)
	}
	if m.reg.IsManaged("newvm") {
		t.Fatal("precondition: a failed build must NOT be recorded managed (the test proves nothing otherwise)")
	}
	if strings.Contains(grid, "external") {
		t.Fatalf("the VM sand built is not external, whatever the index says, got:\n%s", grid)
	}
	if strings.Contains(grid, "managed") {
		t.Fatalf("every tile on this board is sand's, so the managed field is uniform and must not badge, got:\n%s", grid)
	}
	if strings.Contains(grid, "base ") {
		t.Fatalf("the failed build cloned the fleet's base, so no base badge is warranted, got:\n%s", grid)
	}
}

// A failed build's VM is never recorded managed (RecordSuccess only runs on
// success), so without the job-derived roster its red tile would vanish and the
// failure would be unreportable and un-cleanable.
func TestFailedBuildKeepsItsTile(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40)
	seedJob(t, &m, "newvm", vm.CreateConfig{Name: "newvm", BaseName: "claude-base"})
	m.view = viewBoard
	if _, ok := m.jobs.finish("newvm", errAnsibleBoom); !ok {
		t.Fatal("precondition: the seeded job should finish")
	}

	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{{Name: "newvm", Status: "Stopped"}}})
	m = loaded.(model)

	if got := boardNames(m); len(got) != 1 || got[0] != "newvm" {
		t.Fatalf("board = %v, want the failed build to keep its tile", got)
	}
	if view := ansi.Strip(m.boardView()); !strings.Contains(view, "Failed") {
		t.Fatalf("a failed build's tile must say so, got:\n%s", view)
	}
}

// While searching, every action-letter key must edit the query rather than fire
// its binding: single-key verbs and a typing mode compete for the same
// keystrokes, and the search must win while it is open. (Ported from the list's
// TestSearchCapturesActionKeys — the behaviour survives the surface.)
func TestSearchCapturesActionKeys(t *testing.T) {
	m := newTestModel(t)
	m = loadManaged(t, m, vm.VM{Name: "claude", Status: "Running"})

	mi, _ := m.Update(runeKey('/'))
	m = mi.(model)
	if !m.searching {
		t.Fatal("expected searching mode after '/'")
	}

	for _, r := range []rune{'s', 'x', 'd', 'r', 'S', 'X', 'n', 'q'} {
		mi, cmd := m.Update(runeKey(r))
		m = mi.(model)
		if cmd != nil {
			t.Fatalf("%q fired a command while searching", r)
		}
	}

	if m.searchQuery != "sxdrSXnq" {
		t.Fatalf("query = %q, want the typed action letters %q", m.searchQuery, "sxdrSXnq")
	}
	if m.confirm != nil {
		t.Fatal("an action fired while searching (a confirm overlay opened)")
	}
	if m.acting {
		t.Fatal("a lifecycle action fired while searching")
	}
	if m.view != viewBoard {
		t.Fatalf("searching must stay on the board, view = %v", m.view)
	}

	// esc clears the query and exits search.
	mi, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = mi.(model)
	if m.searching || m.searchQuery != "" {
		t.Fatalf("esc should clear the query and exit search (searching=%v query=%q)", m.searching, m.searchQuery)
	}
}

// '/' narrows the visible tiles live, enter commits the query (leaving search
// mode with the filter still on), and esc clears it.
func TestSearchNarrowsTilesEnterCommitsEscClears(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40)
	m = loadManaged(t, m,
		vm.VM{Name: "api", Status: "Running"},
		vm.VM{Name: "db", Status: "Running"},
		vm.VM{Name: "web", Status: "Running"},
	)

	m, _ = press(t, m, runeKey('/'))
	for _, r := range []rune{'W', 'E'} { // upper case: the match is case-insensitive
		m, _ = press(t, m, runeKey(r))
	}
	if got := boardNames(m); len(got) != 1 || got[0] != "web" {
		t.Fatalf("live search should narrow the board to [web], got %v", got)
	}
	if view := ansi.Strip(m.boardView()); strings.Contains(view, "api") {
		t.Fatalf("a filtered-out VM must not render a tile, got:\n%s", view)
	}

	// enter commits: search mode ends, the filter stays.
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.searching || m.searchQuery != "WE" {
		t.Fatalf("enter should commit the query (searching=%v query=%q)", m.searching, m.searchQuery)
	}
	if got := boardNames(m); len(got) != 1 || got[0] != "web" {
		t.Fatalf("a committed filter should stay applied, got %v", got)
	}

	// esc clears the committed filter and restores every tile.
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.searchQuery != "" {
		t.Fatalf("esc should clear the committed filter, query = %q", m.searchQuery)
	}
	if got := boardNames(m); len(got) != 3 {
		t.Fatalf("clearing the filter should restore every tile, got %v", got)
	}
}

// Focus is pinned to VM identity across a FILTER change, exactly as it is across
// a refresh: the surviving VM keeps the ring, and a focused VM the filter hides
// hands it to a predictable neighbour.
func TestFocusIsPinnedAcrossAFilterChange(t *testing.T) {
	m := newTestModel(t)
	m = loadManaged(t, m,
		vm.VM{Name: "api", Status: "Running"},
		vm.VM{Name: "db", Status: "Running"},
		vm.VM{Name: "web", Status: "Running"},
	)

	// The focused VM survives the filter: it keeps the ring, at a new slot.
	m.focusName = "web"
	m, _ = press(t, m, runeKey('/'))
	for _, r := range []rune{'w', 'e', 'b'} {
		m, _ = press(t, m, runeKey(r))
	}
	if m.focusName != "web" {
		t.Fatalf("focus = %q, want it pinned to the still-visible web", m.focusName)
	}
	if got := indexOf(boardNames(m), "web"); got != 0 {
		t.Fatalf("precondition: web's slot should have moved to 0, got %d", got)
	}

	// Clearing the filter must not silently slide the ring onto slot 0's VM.
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.focusName != "web" {
		t.Fatalf("focus = %q after clearing the filter, want web", m.focusName)
	}

	// A filter that HIDES the focused VM hands the ring to a neighbour that is
	// actually on the board.
	m.focusName = "db"
	m, _ = press(t, m, runeKey('/'))
	for _, r := range []rune{'w', 'e', 'b'} {
		m, _ = press(t, m, runeKey(r))
	}
	if m.focusName != "web" {
		t.Fatalf("focus = %q after the filter hid db, want the only visible tile web", m.focusName)
	}
}

// 'X' means "stop every managed VM", not "stop what I can currently see": an
// active '/' filter does NOT narrow it. The opposite reading is defensible
// enough that this is asserted, not assumed.
func TestStopAllIgnoresTheSearchFilter(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40)
	m = loadManaged(t, m,
		vm.VM{Name: "api", Status: "Running"},
		vm.VM{Name: "web", Status: "Running"},
	)

	// Commit a filter that hides api (X while searching would be captured by the
	// search, which is the other half of the contract).
	m, _ = press(t, m, runeKey('/'))
	for _, r := range []rune{'w', 'e', 'b'} {
		m, _ = press(t, m, runeKey(r))
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := boardNames(m); len(got) != 1 {
		t.Fatalf("precondition: the filter should hide api, board = %v", got)
	}

	if got := m.stopAllTargets(); len(got) != 2 {
		t.Fatalf("stopAllTargets() = %v, want BOTH running managed VMs despite the filter", got)
	}
	m, cmd := press(t, m, runeKey('X'))
	if cmd != nil {
		t.Fatal("X must confirm before stopping anything")
	}
	if m.confirm == nil {
		t.Fatal("X with running managed VMs should raise the confirm overlay")
	}
	if !strings.Contains(m.confirm.prompt, "2") || !strings.Contains(m.confirm.prompt, "api") {
		t.Fatalf("the stop-all prompt %q should cover the filtered-out api too", m.confirm.prompt)
	}
}

// THE GRID SCROLLS, and focus follows it. At 80x24 a single tile column holds
// two tiles, so a user with more VMs than that will scroll: moving focus past
// the viewport's edge must scroll rather than trap the ring, and the focused
// tile must always be on screen.
func TestBoardScrollsToKeepTheFocusedTileVisible(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 80, 24)
	m = loadManaged(t, m,
		vm.VM{Name: "vm-a", Status: "Running"},
		vm.VM{Name: "vm-b", Status: "Running"},
		vm.VM{Name: "vm-c", Status: "Running"},
		vm.VM{Name: "vm-d", Status: "Running"},
		vm.VM{Name: "vm-e", Status: "Running"},
	)
	if m.layout.Columns != 1 {
		t.Fatalf("precondition: 80x24 should give a single tile column, got %d", m.layout.Columns)
	}
	if rows := m.visibleTileRows(); rows != 2 {
		t.Fatalf("precondition: 80x24 should show two tile rows, got %d", rows)
	}

	m.focusName = "vm-a"
	m.scrollRow = 0
	if view := ansi.Strip(m.boardView()); !strings.Contains(view, "vm-a") || strings.Contains(view, "vm-c") {
		t.Fatalf("the first screenful should hold vm-a and vm-b only, got:\n%s", view)
	}

	// Arrow down past the viewport's edge: the ring keeps moving and the grid
	// scrolls under it.
	for i := 0; i < 3; i++ {
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if m.focusName != "vm-d" {
		t.Fatalf("three downs from vm-a should focus vm-d, got %q (the ring is trapped)", m.focusName)
	}
	if m.scrollRow != 2 {
		t.Fatalf("scrollRow = %d, want 2 (the grid must scroll to keep vm-d visible)", m.scrollRow)
	}
	view := ansi.Strip(m.boardView())
	if !strings.Contains(view, "vm-d") {
		t.Fatalf("the focused tile must stay visible, got:\n%s", view)
	}
	if strings.Contains(view, "vm-a") || strings.Contains(view, "vm-b") {
		t.Fatalf("scrolled-off tiles must not render, got:\n%s", view)
	}

	// And back up: the grid scrolls the other way.
	for i := 0; i < 3; i++ {
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	}
	if m.focusName != "vm-a" || m.scrollRow != 0 {
		t.Fatalf("arrowing back should return to vm-a at the top (focus=%q scrollRow=%d)", m.focusName, m.scrollRow)
	}
	// The ring never falls off the end of the board.
	for i := 0; i < 10; i++ {
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if m.focusName != "vm-e" {
		t.Fatalf("focus = %q, want the last tile vm-e (the ring must clamp, not wrap or vanish)", m.focusName)
	}
}

// Left/right move the ring across a multi-column grid, and never off the edge.
func TestBoardFocusMovesInTwoDimensions(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40)
	m = loadManaged(t, m,
		vm.VM{Name: "vm-a", Status: "Running"},
		vm.VM{Name: "vm-b", Status: "Running"},
		vm.VM{Name: "vm-c", Status: "Running"},
	)
	if m.layout.Columns != 2 {
		t.Fatalf("precondition: 120x40 should give two tile columns, got %d", m.layout.Columns)
	}
	m.focusName = "vm-a"

	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	if m.focusName != "vm-b" {
		t.Fatalf("right from vm-a should focus vm-b, got %q", m.focusName)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	if m.focusName != "vm-b" {
		t.Fatalf("right at the row's edge must not move (or wrap), got %q", m.focusName)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.focusName != "vm-c" {
		t.Fatalf("down from vm-b (slot 1) should clamp onto the last tile vm-c, got %q", m.focusName)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.focusName != "vm-c" {
		t.Fatalf("left at the row's first column must not move, got %q", m.focusName)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.focusName != "vm-a" {
		t.Fatalf("up from vm-c should focus vm-a, got %q", m.focusName)
	}
}

// enter opens the EXISTING full-screen VM screen for the focused tile, and esc
// comes back to the board with the ring still on the same VM.
func TestEnterOpensTheVMScreenAndEscReturnsWithFocus(t *testing.T) {
	m := newTestModel(t)
	m = loadManaged(t, m,
		vm.VM{Name: "api", Status: "Running"},
		vm.VM{Name: "web", Status: "Running"},
	)
	m.focusName = "web"

	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.view != viewDetail {
		t.Fatalf("enter should open the VM screen, view = %v", m.view)
	}
	if m.detail.Name != "web" {
		t.Fatalf("the VM screen shows %q, want the focused web", m.detail.Name)
	}

	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.view != viewBoard {
		t.Fatalf("esc should return to the board, view = %v", m.view)
	}
	if m.focusName != "web" {
		t.Fatalf("focus = %q after returning from the VM screen, want web", m.focusName)
	}
}

// The board's key dispatch derives from the command registry: a verb fires iff
// enabledFor reports it applies to the FOCUSED VM. A stopped VM offers no stop,
// so 'x' on it is a silent no-op — the same contract the VM screen already has,
// now reached through the tile under the ring.
func TestBoardVerbsFireOnlyWhenEnabledForTheFocusedVM(t *testing.T) {
	m := newTestModel(t)
	m = loadManaged(t, m,
		vm.VM{Name: "off", Status: "Stopped"},
		vm.VM{Name: "on", Status: "Running"},
	)

	m.focusName = "off"
	after, cmd := press(t, m, runeKey('x')) // stop is disabled for a stopped VM
	if cmd != nil || after.acting {
		t.Fatal("'x' on a stopped focused VM must be a silent no-op")
	}
	after, cmd = press(t, m, runeKey('s')) // start is enabled
	if cmd == nil || !after.acting {
		t.Fatal("'s' on a stopped focused VM should dispatch a start")
	}
	if done := actionDone(t, cmd); done.name != "off" {
		t.Fatalf("'s' started %q, want the focused off", done.name)
	}

	m.focusName = "on"
	after, cmd = press(t, m, runeKey('x'))
	if cmd == nil || !after.acting {
		t.Fatal("'x' on a running focused VM should dispatch a stop")
	}
	if done := actionDone(t, cmd); done.name != "on" {
		t.Fatalf("'x' stopped %q, want the focused on", done.name)
	}

	// 'd' raises the confirm for the focused VM — the destructive key must name
	// the VM under the ring and nothing else.
	m.focusName = "off"
	after, cmd = press(t, m, runeKey('d'))
	if cmd != nil {
		t.Fatal("'d' must confirm before deleting anything")
	}
	if after.confirm == nil || !strings.Contains(after.confirm.prompt, `"off"`) {
		t.Fatalf("'d' should raise a confirm naming the focused VM, got %+v", after.confirm)
	}
}

// A verb must read its VM from the COMMAND REGISTRY's argument and nowhere else.
// The transfer verbs used to read m.detail — the VM screen's own record — which
// was harmless while the VM screen was the only place they could fire from. Fired
// from the board, that is a wrong-VM bug with a file copy on the end of it: the
// user focuses `web`, presses 'u', and uploads into whichever VM they last
// zoomed into.
func TestTransferFromTheBoardTargetsTheFocusedTileNotTheLastZoomedVM(t *testing.T) {
	m := newTestModel(t)
	m = loadManaged(t, m,
		vm.VM{Name: "api", Status: "Running", Dir: "/nonexistent/api"},
		vm.VM{Name: "web", Status: "Running", Dir: "/nonexistent/web"},
	)

	// The user zooms into api, then comes back to the board and focuses web.
	m.focusName = "api"
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.detail.Name != "api" {
		t.Fatalf("precondition: the VM screen should be showing api, got %q", m.detail.Name)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m.focusName = "web"

	m, _ = press(t, m, runeKey('u')) // upload
	if m.view != viewBrowse {
		t.Fatalf("'u' on a running focused VM should open the browser, view = %v", m.view)
	}
	if m.transferVM != "web" {
		t.Fatalf("the transfer targets %q — the VM last zoomed into — want the focused web", m.transferVM)
	}
}

// An empty board is the dominant state of a 1–3 VM fleet, so it is a call to
// action rather than dead space: the empty slot carries a ghost tile.
func TestGhostTileInvitesTheFirstVM(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40)
	loaded, _ := m.Update(vmsLoadedMsg{vms: nil})
	m = loaded.(model)

	view := ansi.Strip(m.boardView())
	if !strings.Contains(view, "press n to add a VM") {
		t.Fatalf("an empty board should invite the first VM, got:\n%s", view)
	}

	// The invitation stays put in the next empty slot once a VM exists.
	m = loadManaged(t, m, vm.VM{Name: "web", Status: "Running"})
	view = ansi.Strip(m.boardView())
	if !strings.Contains(view, "web") || !strings.Contains(view, "press n to add a VM") {
		t.Fatalf("a nearly-empty board should still invite another VM, got:\n%s", view)
	}
}

// 'q' must not orphan work. Task 04 left the board's quit unconditional, so a
// user with a background build in flight could end the session — and the
// half-built VM — with the reflex that ends every other TUI. It now confirms.
func TestQuitConfirmsWhileAJobIsInFlight(t *testing.T) {
	m := newTestModel(t)
	m = loadManaged(t, m, vm.VM{Name: "web", Status: "Running"})

	// Nothing in flight: q quits, as it always did.
	if _, cmd := press(t, m, runeKey('q')); !isQuitCmd(cmd) {
		t.Fatal("q on an idle board should quit")
	}

	seedJob(t, &m, "newvm", vm.CreateConfig{Name: "newvm", BaseName: "claude-base"})
	m.view = viewBoard

	quit, cmd := press(t, m, runeKey('q'))
	if isQuitCmd(cmd) {
		t.Fatal("q with a build in flight must not quit outright — it would orphan the build")
	}
	if quit.confirm == nil {
		t.Fatal("q with a build in flight should raise the confirm overlay")
	}
	if !strings.Contains(quit.confirm.prompt, "newvm") {
		t.Fatalf("the quit prompt %q should name the run it would abandon", quit.confirm.prompt)
	}
	// Confirming it really does quit.
	confirmed, cmd := press(t, quit, runeKey('y'))
	if !isQuitCmd(cmd) && !isQuitCmd(batchQuit(cmd)) {
		t.Fatal("confirming the quit should quit")
	}
	if confirmed.confirm != nil {
		t.Fatal("confirming should clear the overlay")
	}
	// And declining leaves the session — and the build — alone.
	declined, cmd := press(t, quit, runeKey('n'))
	if cmd != nil || declined.confirm != nil {
		t.Fatal("declining the quit should dismiss the overlay and do nothing else")
	}
	if !declined.jobs.isRunning("newvm") {
		t.Fatal("declining the quit must not touch the build")
	}
}

// batchQuit unwraps a tea.Batch (beginAction batches the confirmed action with
// the spinner tick) far enough to find a tea.Quit inside it.
func batchQuit(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		return nil
	}
	for _, c := range batch {
		if isQuitCmd(c) {
			return c
		}
	}
	return nil
}

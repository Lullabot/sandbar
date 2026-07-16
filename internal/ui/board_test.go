package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"

	"charm.land/bubbles/v2/key"
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
		if err := m.reg.Add(vm.CreateConfig{Name: v.Name, BaseName: "sandbar-base"}); err != nil {
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
	m.focusVM.Name = "db"
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
	// The GRID specifically, not the whole screen: task 09's messages strip just
	// logged "stopping db…" above it, and that "db" substring would otherwise be
	// mistaken for the tile's.
	view := ansi.Strip(m.gridView())
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
	m.focusVM.Name = "web"
	if got := indexOf(boardNames(m), "web"); got != 1 {
		t.Fatalf("precondition: web should start in slot 1, got %d", got)
	}

	// A refresh brings in a VM that sorts first, sliding every later slot down.
	m = loadManaged(t, m,
		vm.VM{Name: "db", Status: "Running"},
		vm.VM{Name: "web", Status: "Running"},
		vm.VM{Name: "api", Status: "Running"},
	)

	if m.focusVM.Name != "web" {
		t.Fatalf("focus = %q after an insertion before it, want it pinned to web", m.focusVM.Name)
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
	m.focusVM.Name = "db"
	m = loadManaged(t, m,
		vm.VM{Name: "api", Status: "Running"},
		vm.VM{Name: "web", Status: "Running"},
	)
	if m.focusVM.Name != "api" {
		t.Fatalf("focus = %q after deleting db, want its neighbour api (web now occupies db's old slot)", m.focusVM.Name)
	}

	// The FIRST tile is deleted: there is no preceding neighbour, so the ring
	// takes the one that is now first.
	m2 := base
	m2.focusVM.Name = "api"
	m2 = loadManaged(t, m2,
		vm.VM{Name: "db", Status: "Running"},
		vm.VM{Name: "web", Status: "Running"},
	)
	if m2.focusVM.Name != "db" {
		t.Fatalf("focus = %q after deleting the first tile, want db", m2.focusVM.Name)
	}

	// The last VM goes: the ring lands on the ghost — the only cell left, and the
	// one thing worth doing on an empty board — and nothing panics. It is still NOT
	// a focused VM, so no per-VM verb can fire on it.
	m3 := base
	m3.focusVM.Name = "web"
	m3 = loadManaged(t, m3)
	if !m3.focusIsGhost() {
		t.Fatalf("an emptied board should focus the ghost, got %q", m3.focusVM.Name)
	}
	if _, ok := m3.focusedVM(); ok {
		t.Fatal("the ghost is not a VM: an empty board must report no focused VM")
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
	m.focusVM.Name = "web" // slot 1

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
	if !strings.Contains(m.lastMessage(), "web") {
		t.Fatalf("status = %q, want it to name the VM the verb reached", m.lastMessage())
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
	if err := m.reg.Add(vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "web", Status: "Running"},
		{Name: "web-stray", Status: "Running"},    // someone else's VM
		{Name: "sandbar-base", Status: "Stopped"}, // the base image every clone comes from
	}})
	m = loaded.(model)

	if got := boardNames(m); len(got) != 1 || got[0] != "web" {
		t.Fatalf("board = %v, want only the managed clone [web]", got)
	}
	view := ansi.Strip(m.boardView())
	if strings.Contains(view, "web-stray") {
		t.Fatalf("an unmanaged VM must get no tile, got:\n%s", view)
	}
	if strings.Contains(view, "sandbar-base") {
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
	seedJob(t, &m, "newvm", vm.CreateConfig{Name: "newvm", BaseName: "sandbar-base"})
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
	seedJob(t, &m, "newvm", vm.CreateConfig{Name: "newvm", BaseName: "sandbar-base"})
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

	seedJob(t, &m, "newvm", vm.CreateConfig{Name: "newvm", BaseName: "sandbar-base"})
	m.view = viewBoard
	if _, ok := m.jobs.finish(provisionKey(registry.LocalScope, "newvm"), errAnsibleBoom); !ok {
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
	seedJob(t, &m, "newvm", vm.CreateConfig{Name: "newvm", BaseName: "sandbar-base"})
	m.view = viewBoard
	if _, ok := m.jobs.finish(provisionKey(registry.LocalScope, "newvm"), errAnsibleBoom); !ok {
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
	m.focusVM.Name = "web"
	m, _ = press(t, m, runeKey('/'))
	for _, r := range []rune{'w', 'e', 'b'} {
		m, _ = press(t, m, runeKey(r))
	}
	if m.focusVM.Name != "web" {
		t.Fatalf("focus = %q, want it pinned to the still-visible web", m.focusVM.Name)
	}
	if got := indexOf(boardNames(m), "web"); got != 0 {
		t.Fatalf("precondition: web's slot should have moved to 0, got %d", got)
	}

	// Clearing the filter must not silently slide the ring onto slot 0's VM.
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.focusVM.Name != "web" {
		t.Fatalf("focus = %q after clearing the filter, want web", m.focusVM.Name)
	}

	// A filter that HIDES the focused VM hands the ring to a neighbour that is
	// actually on the board.
	m.focusVM.Name = "db"
	m, _ = press(t, m, runeKey('/'))
	for _, r := range []rune{'w', 'e', 'b'} {
		m, _ = press(t, m, runeKey(r))
	}
	if m.focusVM.Name != "web" {
		t.Fatalf("focus = %q after the filter hid db, want the only visible tile web", m.focusVM.Name)
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

	m.focusVM.Name = "vm-a"
	m.scrollRow = 0
	if view := ansi.Strip(m.boardView()); !strings.Contains(view, "vm-a") || strings.Contains(view, "vm-c") {
		t.Fatalf("the first screenful should hold vm-a and vm-b only, got:\n%s", view)
	}

	// Arrow down past the viewport's edge: the ring keeps moving and the grid
	// scrolls under it.
	for i := 0; i < 3; i++ {
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if m.focusVM.Name != "vm-d" {
		t.Fatalf("three downs from vm-a should focus vm-d, got %q (the ring is trapped)", m.focusVM.Name)
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
	if m.focusVM.Name != "vm-a" || m.scrollRow != 0 {
		t.Fatalf("arrowing back should return to vm-a at the top (focus=%q scrollRow=%d)", m.focusVM.Name, m.scrollRow)
	}
	// The ring never falls off the end of the board. The last CELL is the ghost —
	// it sits after the final tile — so that is where a run of downs comes to rest.
	for i := 0; i < 10; i++ {
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if !m.focusIsGhost() {
		t.Fatalf("focus = %q, want the ghost, the last cell (the ring must clamp, not wrap or vanish)", m.focusVM.Name)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.focusVM.Name != "vm-e" {
		t.Fatalf("up from the ghost should return to the last tile vm-e, got %q", m.focusVM.Name)
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
	m.focusVM.Name = "vm-a"

	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	if m.focusVM.Name != "vm-b" {
		t.Fatalf("right from vm-a should focus vm-b, got %q", m.focusVM.Name)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	if m.focusVM.Name != "vm-b" {
		t.Fatalf("right at the row's edge must not move (or wrap), got %q", m.focusVM.Name)
	}
	// Down from vm-b lands on the GHOST: three VMs in two columns put the empty
	// slot directly beneath vm-b, and the ghost is a cell the ring moves over like
	// any other. It used to clamp back onto vm-c, because the invitation was
	// something the ring could not land on.
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if !m.focusIsGhost() {
		t.Fatalf("down from vm-b (slot 1) should land on the ghost directly below it, got %q", m.focusVM.Name)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.focusVM.Name != "vm-c" {
		t.Fatalf("left from the ghost should focus vm-c beside it, got %q", m.focusVM.Name)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.focusVM.Name != "vm-c" {
		t.Fatalf("left at the row's first column must not move, got %q", m.focusVM.Name)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.focusVM.Name != "vm-a" {
		t.Fatalf("up from vm-c should focus vm-a, got %q", m.focusVM.Name)
	}
}

// Enter on a VM tile does the one obvious thing for the state that tile is in:
// shell into a running VM. It carries no action of its own — it routes to an
// existing registry verb (enterTarget) — so these tests pin the ROUTING, and the
// verbs' own tests still cover what each one does.
func TestEnterOnARunningVMShellsIn(t *testing.T) {
	m := newTestModel(t)
	m = loadManaged(t, m,
		vm.VM{Name: "api", Status: "Running"},
		vm.VM{Name: "web", Status: "Running"},
	)
	m.focusVM.Name = "web"

	after, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on a running VM must shell in, got no command")
	}
	if after.focusVM.Name != "web" {
		t.Fatalf("enter must not move the ring, got %q", after.focusVM.Name)
	}
	// The shell verb announces itself on the status line, and it is the only verb
	// Enter can route a running VM to.
	if !strings.Contains(after.lastMessage(), "web") {
		t.Fatalf("enter on a running VM should report attaching to it, got status %q", after.lastMessage())
	}
}

// A STOPPED VM starts. This is the case that makes Enter worth having: the tile
// says "Stopped" and the obvious next thing is to wake it up.
func TestEnterOnAStoppedVMStartsIt(t *testing.T) {
	m := newTestModel(t)
	m = loadManaged(t, m, vm.VM{Name: "web", Status: "Stopped"})
	m.focusVM.Name = "web"

	after, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on a stopped VM must start it, got no command")
	}
	if !strings.Contains(after.lastMessage(), "starting web") {
		t.Fatalf("enter on a stopped VM should report starting it, got status %q", after.lastMessage())
	}
}

// A BUILDING VM shows its log — and must NOT be shelled into or started. A VM
// mid-provision is `Running` to Lima (Ansible runs inside a booted guest), so
// routing on Status alone would drop the user into a guest its own build owns.
// enterTarget checks "building" first, and this is the test that says so.
func TestEnterOnABuildingVMShowsTheLog(t *testing.T) {
	m := newTestModel(t)
	l := newTeaLoop(t, m)

	job := newFakeJob()
	l.exec(l.m.beginProvision("Creating web", job.run, vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}))
	job.write(l, provisionKey(registry.LocalScope, "web"), "TASK [base : Install]\n")

	// Back to the board, where the tile for the in-flight build sits under the ring.
	l.send(tea.KeyPressMsg{Code: tea.KeyEsc})
	if l.m.view != viewBoard {
		t.Fatalf("esc during a build should return to the board, got view %v", l.m.view)
	}
	l.m.focusVM.Name = "web"
	if !l.m.vmBuilding(registry.LocalScope, "web") {
		t.Fatal("precondition: web must be mid-build")
	}

	l.send(tea.KeyPressMsg{Code: tea.KeyEnter})

	if l.m.view != viewProgress {
		t.Fatalf("enter on a building VM must reopen its log (viewProgress), got view %v", l.m.view)
	}
	if l.m.jobs.isRunning(registry.LocalScope, "web") != true {
		t.Fatal("showing the log must not disturb the build")
	}
}

// THE FOOTER IS THE PAYOFF FOR THE COMMAND REGISTRY (task 02): boardHelp
// derives from the exact same detailCommands list and the exact same
// enabledFor(model, vm.VM) predicate the dispatcher above uses, so it cannot
// advertise a verb that would do nothing to the tile under the ring. Proven
// here the way the plan's self-validation step 3f demands: focus a RUNNING
// VM (footer offers Stop, not Start), stop it for real through the
// dispatcher, and watch the footer flip — Stop disappears, Start appears —
// once the refresh that follows the action reports the new state.
func TestBoardFooterUpdatesAsTheFocusedVMsStateChanges(t *testing.T) {
	m := newTestModel(t)
	m = loadManaged(t, m, vm.VM{Name: "web", Status: "Running"})
	m.focusVM.Name = "web"

	rendered := plainHelp(m.boardView())
	if !strings.Contains(rendered, "x stop") {
		t.Fatalf("a running focused VM's footer should offer stop, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "s start") {
		t.Fatalf("a running focused VM's footer must not offer start, got:\n%s", rendered)
	}

	// Stop it for real, through the dispatcher — not by hand-setting a field.
	m, cmd := press(t, m, runeKey('x'))
	done := actionDone(t, cmd)
	if done.action != "stop" || done.name != "web" {
		t.Fatalf("'x' dispatched %s on %q, want stop on web", done.action, done.name)
	}
	// The refresh that follows every action (actionDoneMsg's handler,
	// model.go) reports web's new state — loadManaged stands in for it here,
	// the same way TestBoardOrderIsAlphabeticalAndStableAcrossAStateChange
	// (above) uses it to simulate the post-action refresh.
	m = loadManaged(t, m, vm.VM{Name: "web", Status: "Stopped"})

	rendered = plainHelp(m.boardView())
	if strings.Contains(rendered, "x stop") {
		t.Fatalf("the footer must drop stop once web is stopped, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "s start") {
		t.Fatalf("the footer must offer start once web is stopped, got:\n%s", rendered)
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

	m.focusVM.Name = "off"
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

	m.focusVM.Name = "on"
	after, cmd = press(t, m, runeKey('x'))
	if cmd == nil || !after.acting {
		t.Fatal("'x' on a running focused VM should dispatch a stop")
	}
	if done := actionDone(t, cmd); done.name != "on" {
		t.Fatalf("'x' stopped %q, want the focused on", done.name)
	}

	// 'd' raises the confirm for the focused VM — the destructive key must name
	// the VM under the ring and nothing else.
	m.focusVM.Name = "off"
	after, cmd = press(t, m, runeKey('d'))
	if cmd != nil {
		t.Fatal("'d' must confirm before deleting anything")
	}
	if after.confirm == nil || !strings.Contains(after.confirm.prompt, `"off"`) {
		t.Fatalf("'d' should raise a confirm naming the focused VM, got %+v", after.confirm)
	}
}

// A verb reads its VM from the COMMAND REGISTRY's argument and nowhere else. The
// transfer verbs used to read m.detail — the VM screen's own record — which was
// harmless while that screen was the only place they could fire from, and a
// wrong-VM bug with a file copy on the end of it once the board fired them too.
// The record is gone with the screen; this pins that the verb follows the RING.
func TestTransferFromTheBoardTargetsTheFocusedTile(t *testing.T) {
	m := newTestModel(t)
	m = loadManaged(t, m,
		vm.VM{Name: "api", Status: "Running", Dir: "/nonexistent/api"},
		vm.VM{Name: "web", Status: "Running", Dir: "/nonexistent/web"},
	)

	// The ring visits api first, then settles on web. Only web may be uploaded into.
	m.focusVM.Name = "api"
	m.focusVM.Name = "web"

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
	if !strings.Contains(view, ghostTileText) {
		t.Fatalf("an empty board should invite the first VM, got:\n%s", view)
	}

	// And the invitation is SELECTED: on an empty board the ghost is the only cell,
	// so the ring is already on it and enter creates a VM. The invitation used to be
	// an instruction the ring could never land on.
	if !m.focusIsGhost() {
		t.Fatalf("an empty board should focus the ghost, got focusName %q", m.focusVM.Name)
	}
	entered, _ := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if entered.view != viewForm {
		t.Fatalf("enter on the ghost should open the create form, got view %v", entered.view)
	}

	// The invitation stays put in the next empty slot once a VM exists.
	m = loadManaged(t, m, vm.VM{Name: "web", Status: "Running"})
	view = ansi.Strip(m.boardView())
	if !strings.Contains(view, "web") || !strings.Contains(view, ghostTileText) {
		t.Fatalf("a nearly-empty board should still invite another VM, got:\n%s", view)
	}
	// The ring STAYS on the ghost — the identity pin applies to the empty slot too.
	// It must: the ring is on the ghost whenever the user deliberately arrowed there,
	// and this same path runs on every refresh tick, so a rule that handed the ring
	// to a VM whenever one existed would yank focus off the empty slot every few
	// seconds. Nothing destructive can fire on the ghost, so leaving it there is
	// safe; arrowing to the new tile is one keypress.
	if !m.focusIsGhost() {
		t.Fatalf("the ring should stay on the ghost the user is on, got %q", m.focusVM.Name)
	}
}

// 'q' QUITS FROM THE BOARD ONLY. On a child screen the key that leaves is `esc`,
// and a quit key sitting beside it turns one mistyped keystroke into the end of
// the session instead of the end of the screen. The VM screen and the progress
// screen both used to offer it, and neither advertised nor tested the difference —
// which is exactly how it stayed there.
func TestQuitIsOfferedOnTheBoardAndNowhereElse(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40) // the footer band needs a real terminal to render into
	m = loadManaged(t, m, vm.VM{Name: "web", Status: "Running"})

	// The board: q quits, and says so.
	if _, cmd := press(t, m, runeKey('q')); !isQuitCmd(cmd) {
		t.Fatal("q on the board should quit")
	}
	if !offersQuit(m.boardHelp()) {
		t.Fatal("the board should advertise quit in its help bindings")
	}

	// The secrets editor: q types a letter, it does not end the session.
	e := openSecretsViaKey(t, m, "web", "Running")
	if e.view != viewSecrets {
		t.Fatalf("precondition: 'e' should open the secrets editor, got view %v", e.view)
	}
	after, cmd := press(t, e, runeKey('q'))
	if isQuitCmd(cmd) {
		t.Fatal("q in the secrets editor must not quit — it is a character")
	}
	if after.view != viewSecrets {
		t.Fatalf("q in the secrets editor must not navigate, got view %v", after.view)
	}

	// The progress screen, with its job finished (the case that used to quit).
	p := m
	job := newFakeJob()
	l := newTeaLoop(t, p)
	l.exec(l.m.beginProvision("Creating web2", job.run, vm.CreateConfig{Name: "web2", BaseName: "sandbar-base"}))
	job.done <- nil
	l.pump("web2 to finish", func(m model) bool { return !m.jobs.isRunning(registry.LocalScope, "web2") })
	l.exec(l.m.showJobLog(registry.LocalScope, "web2"))
	if l.m.view != viewProgress {
		t.Fatalf("precondition: the log should be on screen, got view %v", l.m.view)
	}
	l.send(runeKey('q'))
	if l.m.view != viewProgress {
		t.Fatalf("q on a finished progress screen must not quit or navigate, got view %v", l.m.view)
	}
	if offersQuit(l.m.progressHelp()) {
		t.Fatal("the progress screen must not advertise quit")
	}
}

// offersQuit reports whether a screen's help bindings include the quit key. It
// asks the REGISTRY, not the rendered footer: the footer truncates on a narrow
// terminal, so a screen could still be dispatching a key it had no room to print.
func offersQuit(bindings []key.Binding) bool {
	for _, b := range bindings {
		for _, k := range b.Keys() {
			if k == "q" {
				return true
			}
		}
	}
	return false
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

	seedJob(t, &m, "newvm", vm.CreateConfig{Name: "newvm", BaseName: "sandbar-base"})
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
	if !declined.jobs.isRunning(registry.LocalScope, "newvm") {
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

// THE GHOST TILE IS THE BOARD'S CALL TO ACTION, AND IT HAS TO BE REACHABLE.
//
// The empty-slot invitation is retained BECAUSE a 1–3 VM board is mostly empty:
// the dominant state of the target user's board becomes "press n" instead of dead
// space. But it is a CELL IN THE GRID, and the grid's scroll clamp counted only
// the VMs — so at 80x24 (one column, two visible tile rows) a board with two VMs
// filled the viewport and the ghost's row could never come on screen. The
// affordance was visible with exactly one VM, and never again.
func TestGhostTileIsReachableWithTwoVMsInOneColumn(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 80, 24)
	m = loadManaged(t, m,
		vm.VM{Name: "vm-a", Status: "Running"},
		vm.VM{Name: "vm-b", Status: "Running"},
	)
	if m.layout.Columns != 1 || m.visibleTileRows() != 2 {
		t.Fatalf("precondition: 80x24 should give one column and two tile rows, got %d column(s), %d row(s)",
			m.layout.Columns, m.visibleTileRows())
	}
	m.focusVM.Name = "vm-a"

	// Arrowing down walks vm-a → vm-b → the ghost, scrolling as it goes. The ghost
	// is a cell the ring lands on, so reaching it is just movement.
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.focusVM.Name != "vm-b" {
		t.Fatalf("down from vm-a should focus vm-b, got %q", m.focusVM.Name)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if !m.focusIsGhost() {
		t.Fatalf("down from vm-b should reach the ghost, got %q", m.focusVM.Name)
	}
	view := ansi.Strip(m.boardView())
	if !strings.Contains(view, ghostTileText) {
		t.Fatalf("the %q affordance is unreachable with two VMs in one column:\n%s", ghostTileText, view)
	}
	// And enter on it creates a VM — the affordance is not just visible, it works.
	entered, _ := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if entered.view != viewForm {
		t.Fatalf("enter on the reached ghost should open the create form, got view %v", entered.view)
	}
}

// The footer must NAME the verb enter would run, because enter means something
// different on every tile — "enter" alone would tell the user nothing, and a
// footer that named the wrong verb would be worse. Both the footer and the
// dispatcher read enterTarget, so they cannot disagree.
func TestEnterIsAdvertisedWithTheVerbItRuns(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40)
	m = loadManaged(t, m, vm.VM{Name: "web", Status: "Running"}, vm.VM{Name: "api", Status: "Stopped"})

	// The verb enter routes to carries BOTH keys, rather than enter getting a line
	// of its own that repeats the same word.
	m.focusVM.Name = "web" // running -> shell
	if !strings.Contains(boardVerbs(m), "enter/S shell") {
		t.Fatalf("a running tile must advertise enter alongside S on the shell verb:\n%s", boardVerbs(m))
	}
	if strings.Contains(boardVerbs(m), "enter shell") {
		t.Fatalf("enter must not get a duplicate line of its own:\n%s", boardVerbs(m))
	}

	m.focusVM.Name = "api" // stopped -> start
	if !strings.Contains(boardVerbs(m), "enter/s start") {
		t.Fatalf("a stopped tile must advertise enter alongside s on the start verb:\n%s", boardVerbs(m))
	}

	m.focusVM.Name = ghostFocusName
	if !strings.Contains(boardVerbs(m), "enter new VM") {
		t.Fatalf("the ghost must advertise enter as the create verb:\n%s", boardVerbs(m))
	}
}

// TestFocusRingDisambiguatesSameNameAcrossScopes pins finding 2 from the
// plan-16 code review: the focus ring used to resolve by BARE NAME
// (vmIndex/focusIndex/focusedVM/renderCell), so with a local "web" and a
// remote "web" on the board at once, the ring could never land on the second
// one — focusedVM() kept resolving to whichever one sorted first, BOTH tiles
// rendered focused, and a destructive verb always targeted the first-sorted
// scope's VM regardless of which one the user actually navigated to.
func TestFocusRingDisambiguatesSameNameAcrossScopes(t *testing.T) {
	isolateHostState(t)
	fleet := provider.Fleet{
		{Profile: profiles.Profile{ID: profiles.LocalProfileID, Type: profiles.TypeLocal, Enabled: true}, Prov: &providerfake.Provider{}, Scope: registry.LocalScope},
		{Profile: profiles.Profile{ID: "remote", Type: profiles.TypeRemoteSSH, Enabled: true}, Prov: &providerfake.Provider{}, Scope: foreignScope},
	}
	m := New(fleet).(model)
	m = resized(m, 80, 24)
	if m.layout.Columns != 1 {
		t.Fatalf("precondition: 80x24 should give a single tile column, got %d", m.layout.Columns)
	}

	if err := m.reg.AddScoped(vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}, registry.LocalScope); err != nil {
		t.Fatalf("seed local web: %v", err)
	}
	if err := m.reg.AddScoped(vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}, foreignScope); err != nil {
		t.Fatalf("seed remote web: %v", err)
	}
	// Load the FOREIGN scope first, while it is the board's only tile — the
	// ring latches onto it (both scope and name) before the local "web" ever
	// appears, so the initial focus is deterministic rather than an artifact
	// of load order.
	next, _ := m.Update(vmsLoadedMsg{scope: foreignScope, vms: []vm.VM{{Name: "web", Status: "Running"}}})
	m = next.(model)
	next, _ = m.Update(vmsLoadedMsg{scope: registry.LocalScope, vms: []vm.VM{{Name: "web", Status: "Running"}}})
	m = next.(model)

	vms := m.visibleVMs()
	if len(vms) != 2 || vms[0].Name != "web" || vms[1].Name != "web" {
		t.Fatalf("expected two same-named tiles, got %+v", vms)
	}

	first, ok := m.focusedVM()
	if !ok || first.scope != foreignScope {
		t.Fatalf("expected the ring to start on the foreign scope's web (loaded first), got ok=%v scope=%v", ok, first.scope)
	}

	// Move the ring up: registry.Scope sorts "lima" (LocalScope's Provider)
	// before "lima-remote" (foreignScope's), so once both tiles are on the
	// board the local one sits in the row ABOVE the foreign one the ring is
	// still pinned to — the only other cell at one column.
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})

	second, ok := m.focusedVM()
	if !ok || second.scope != registry.LocalScope {
		t.Fatalf("moving focus up should land on the LOCAL scope's web, got ok=%v scope=%v", ok, second.scope)
	}

	// THE FIX: exactly ONE tile renders focused — the ring landing on the
	// second same-named VM must not leave the first one ALSO painted focused.
	focusedCount := 0
	for _, v := range vms {
		if focusMatches(v, m.focusVM) {
			focusedCount++
		}
	}
	if focusedCount != 1 {
		t.Fatalf("exactly one tile should render focused, got %d for focusVM=%+v", focusedCount, m.focusVM)
	}

	// And the delete verb's target — focusedVM(), exactly what updateBoard
	// hands to every vmCommands action — is the FOCUSED scope's VM, not the
	// first-sorted one.
	target, ok := m.focusedVM()
	if !ok || target.scope != registry.LocalScope {
		t.Fatalf("the delete verb's target must be the focused (local) scope's VM, got ok=%v scope=%v", ok, target.scope)
	}
}

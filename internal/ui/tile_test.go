package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/vm"

	"github.com/charmbracelet/x/ansi"
)

// tileTitleLine (task 10): the profile label rides the title row instead of
// growing the tile's fixed six-line budget. The name stays exactly where it
// was, and the label appears only when there is genuine room for it.
func TestTileTitleLineFoldsProfileLabelInWithoutGrowingTheRow(t *testing.T) {
	got := tileTitleLine("claude", "local", 40)
	if !strings.HasPrefix(ansi.Strip(got), "claude") {
		t.Fatalf("tileTitleLine = %q, want the VM name to stay first", got)
	}
	if !strings.Contains(got, "[local]") {
		t.Fatalf("tileTitleLine = %q, want the profile label in brackets", got)
	}
	if w := ansi.StringWidth(got); w != 40 {
		t.Fatalf("tileTitleLine width = %d, want exactly the budget (40): a single line, never two", w)
	}
}

// An empty profile label (a caller that has none to report) must render
// exactly the bare title — no stray bracket pair around nothing.
func TestTileTitleLineEmptyLabelRendersBareTitle(t *testing.T) {
	got := tileTitleLine("claude", "", 40)
	if strings.Contains(ansi.Strip(got), "[") {
		t.Fatalf("tileTitleLine with an empty label = %q, want no brackets at all", got)
	}
}

// The VM's NAME is the tile's identity and must never be truncated to make
// room for the label — the label shrinks and then disappears first.
func TestTileTitleLineNeverTruncatesTheNameForTheLabel(t *testing.T) {
	longName := "a-very-long-sandbox-name-indeed"
	got := tileTitleLine(longName, "a-very-long-remote-profile-name", 36)
	if !strings.Contains(ansi.Strip(got), longName) {
		t.Fatalf("tileTitleLine = %q, want the full VM name %q kept intact", got, longName)
	}
}

// baseTileInput returns a minimal, deterministic tileInput a test can tweak.
func baseTileInput() tileInput {
	return tileInput{
		VM:    vm.VM{Name: "claude", Status: "Running", Dir: ""},
		Width: 44,
		Now:   time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC),
	}
}

// --- Status must always be DERIVED, never vm.Status read directly. ---

// grep -n "vm.Status" internal/ui/tile.go must find nothing: nothing in this
// file may read a VM's Lima status as the rendered status directly. The tile
// renderer must route every status through deriveStatus (jobs.go).
func TestTileNeverReadsVMStatusDirectly(t *testing.T) {
	src, err := os.ReadFile("tile.go")
	if err != nil {
		t.Fatalf("read tile.go: %v", err)
	}
	if strings.Contains(string(src), "vm.Status") {
		t.Fatalf("tile.go must never read vm.Status directly; found the literal substring")
	}
}

// The single most dangerous failure mode in the plan: Lima calls a mid-build
// VM "Running" (Ansible is just a process inside it), so a tile that trusted
// vm.Status would show a build in flight — or worse, a FAILED provision — as
// a healthy green "Running" tile. Rendering must go through deriveStatus and
// show Building/Failed instead.
func TestRenderTileDerivesStatusNotVMStatus(t *testing.T) {
	building := baseTileInput()
	building.VM = vm.VM{Name: "web", Status: "Running"} // Lima's view: just running
	building.Job = jobSnapshot{State: jobRunning, Provision: true}
	building.HasJob = true
	building.Sample = guestSample{HasCPU: true, CPUPct: 50, MemTotal: 100, MemUsed: 50}
	building.HasSample = true

	got := ansi.Strip(renderTile(building))
	if !strings.Contains(got, "Building") {
		t.Fatalf("a VM with a live provision job must render Building, got:\n%s", got)
	}
	if strings.Contains(got, "Running") {
		t.Fatalf("a building VM must NOT render as Running, got:\n%s", got)
	}
	// A building tile replaces its gauges with the progress bar — even though
	// a live heartbeat sample exists here, cpu/mem must not leak through.
	if strings.Contains(strings.ToLower(got), "cpu") || strings.Contains(strings.ToLower(got), "mem") {
		t.Fatalf("a building tile must not render cpu/mem gauges, got:\n%s", got)
	}

	failed := baseTileInput()
	failed.VM = vm.VM{Name: "web", Status: "Running"} // Lima still calls it Running
	failed.Job = jobSnapshot{State: jobFailed, Provision: true}
	failed.HasJob = true

	got2 := ansi.Strip(renderTile(failed))
	if !strings.Contains(got2, "Failed") {
		t.Fatalf("a VM whose last provision failed must render Failed, got:\n%s", got2)
	}
	if strings.Contains(got2, "Running") {
		t.Fatalf("a FAILED provision must NOT leave a reassuring green Running tile, got:\n%s", got2)
	}
}

// The plain (non-building/failed) cases: Lima Running/Stopped falls through
// deriveStatus unchanged when there is no job in the way.
func TestRenderTileRunningAndStoppedFallThrough(t *testing.T) {
	running := baseTileInput()
	running.VM = vm.VM{Name: "web", Status: "Running"}
	got := ansi.Strip(renderTile(running))
	if !strings.Contains(got, "Running") {
		t.Fatalf("plain running VM should render Running, got:\n%s", got)
	}

	stopped := baseTileInput()
	stopped.VM = vm.VM{Name: "web", Status: "Stopped"}
	got2 := ansi.Strip(renderTile(stopped))
	if !strings.Contains(got2, "Stopped") {
		t.Fatalf("plain stopped VM should render Stopped, got:\n%s", got2)
	}
}

// --- Honest absence: a stopped VM shows no cpu/mem gauge, not a zeroed one. ---

func TestStoppedTileHasNoCPUOrMemGauge(t *testing.T) {
	in := baseTileInput()
	in.VM = vm.VM{Name: "web", Status: "Stopped"}
	// Even if a stale sample were somehow present, a stopped VM must not
	// render it: the tile only draws gauges for the DERIVED running state.
	in.Sample = guestSample{HasCPU: true, CPUPct: 99, MemTotal: 100, MemUsed: 99}
	in.HasSample = true

	got := strings.ToLower(ansi.Strip(renderTile(in)))
	if strings.Contains(got, "cpu") {
		t.Fatalf("a stopped tile must not mention cpu at all, got:\n%s", got)
	}
	if strings.Contains(got, "mem") {
		t.Fatalf("a stopped tile must not mention mem at all, got:\n%s", got)
	}
}

// A running VM's first heartbeat sample has HasCPU==false (the counters need
// two readings for a delta) but HasMem()==true from the very first record: the
// tile must draw the real mem gauge while REFUSING to state a cpu number — and
// it must do so without dropping the cpu row, because a row that comes and goes
// drags every row beneath it up and down (see the disk gauge, which used to
// jump). The row holds; the value is an em dash until the second reading lands.
func TestRunningTileFirstSampleShowsMemAndAnUnreadCPU(t *testing.T) {
	in := baseTileInput()
	in.VM = vm.VM{Name: "web", Status: "Running"}
	in.Sample = guestSample{HasCPU: false, MemTotal: 100, MemUsed: 40}
	in.HasSample = true

	got := strings.ToLower(ansi.Strip(renderTile(in)))
	if !strings.Contains(got, "cpu") {
		t.Fatalf("the cpu row must hold its place even with no reading, got:\n%s", got)
	}
	if !strings.Contains(got, "—") {
		t.Fatalf("an unread cpu must render as an em dash, not a number, got:\n%s", got)
	}
	if strings.Contains(got, "0%") {
		t.Fatalf("an unread cpu must NOT be rendered as 0%% — that invents an idle VM, got:\n%s", got)
	}
	if !strings.Contains(got, "mem") {
		t.Fatalf("a first sample with mem data should still render the mem gauge, got:\n%s", got)
	}
}

// The gauges of a running VM never move. A heartbeat that has not reported yet —
// or one the idle gate tore down while the user was on another screen — leaves cpu
// and mem unread, and the tile must hold both rows and keep disk exactly where it
// was. This is the bug the fixed rows exist to close: leaving the board and coming
// back made two gauges vanish and disk jump up into their slot, so a tile appeared
// to lose data it had never lost.
func TestRunningTileKeepsItsRowsWhenTheHeartbeatGoesQuiet(t *testing.T) {
	in := baseTileInput()
	in.VM = vm.VM{Name: "web", Status: "Running", Disk: "107374182400", DiskUsed: "10737418240"}

	in.Sample = guestSample{HasCPU: true, CPUPct: 42, MemTotal: 100, MemUsed: 40}
	in.HasSample = true
	live := strings.Split(ansi.Strip(renderTile(in)), "\n")

	in.Sample = guestSample{}
	in.HasSample = false // the heartbeat is gone: no reading at all
	quiet := strings.Split(ansi.Strip(renderTile(in)), "\n")

	if len(live) != len(quiet) {
		t.Fatalf("a tile must not change height when its heartbeat goes quiet (%d vs %d lines)", len(live), len(quiet))
	}
	diskRow := func(lines []string) int {
		for i, l := range lines {
			if strings.Contains(l, "disk") {
				return i
			}
		}
		return -1
	}
	if a, b := diskRow(live), diskRow(quiet); a < 0 || a != b {
		t.Fatalf("disk must stay on the same row whether or not there is a reading (row %d live, %d quiet)", a, b)
	}
	q := strings.ToLower(strings.Join(quiet, "\n"))
	if !strings.Contains(q, "cpu") || !strings.Contains(q, "mem") {
		t.Fatalf("cpu and mem must keep their rows with no reading, got:\n%s", q)
	}
	if strings.Contains(q, "42%") {
		t.Fatalf("a torn-down heartbeat must not keep showing its last reading, got:\n%s", q)
	}
}

// A running VM with a full sample renders both gauges plus the always-on
// disk gauge.
func TestRunningTileFullSampleDrawsBothGauges(t *testing.T) {
	in := baseTileInput()
	in.VM = vm.VM{Name: "web", Status: "Running", Disk: "107374182400", DiskUsed: "10737418240"}
	in.Sample = guestSample{HasCPU: true, CPUPct: 33, MemTotal: 100, MemUsed: 50}
	in.HasSample = true

	got := strings.ToLower(ansi.Strip(renderTile(in)))
	if !strings.Contains(got, "cpu") {
		t.Fatalf("a full sample should render the cpu gauge, got:\n%s", got)
	}
	if !strings.Contains(got, "mem") {
		t.Fatalf("a full sample should render the mem gauge, got:\n%s", got)
	}
	if !strings.Contains(got, "disk") {
		t.Fatalf("disk should always render, got:\n%s", got)
	}
}

// Disk is real data today and always renders, even for a stopped VM, even
// when DiskUsed is unmeasurable ("" — never a fabricated zero).
func TestDiskGaugeAlwaysRendersEvenUnmeasurable(t *testing.T) {
	in := baseTileInput()
	in.VM = vm.VM{Name: "web", Status: "Stopped", Disk: "107374182400", DiskUsed: ""}
	got := strings.ToLower(ansi.Strip(renderTile(in)))
	if !strings.Contains(got, "disk") {
		t.Fatalf("disk gauge should always render, got:\n%s", got)
	}
	if strings.Contains(got, "0 b/") || strings.Contains(got, "0b/") {
		t.Fatalf("an unmeasurable disk usage must not render as a fabricated zero, got:\n%s", got)
	}
}

// --- Exception-only fields: a genuine fleet-uniformity test. ---

// A fleet where every VM shares an architecture hides the field entirely.
// (The zero value of fleetUniformity is itself "hide everything" — the safe
// default if a caller forgets to populate it — so this also doubles as that
// check: a fresh computeFleetUniformity result over a uniform fleet must
// come back indistinguishable from never having called it at all.)
func TestFleetUniformityHidesUniformArch(t *testing.T) {
	fleet := []vmTraits{{Arch: "x86_64"}, {Arch: "x86_64"}, {Arch: "x86_64"}}
	u := computeFleetUniformity(fleet)
	if u.ShowArch {
		t.Fatalf("a fleet with one shared arch should not show the arch badge")
	}

	in := baseTileInput()
	in.VM = vm.VM{Name: "web", Status: "Running", Arch: "x86_64"}
	in.Traits = vmTraits{Arch: "x86_64"}
	in.Uniform = u

	got := strings.ToLower(ansi.Strip(renderTile(in)))
	if strings.Contains(got, "arch") {
		t.Fatalf("a uniform-arch fleet must not show the arch badge, got:\n%s", got)
	}
}

// A fleet with one aarch64 VM among x86_64 ones shows arch on the differing
// tile, computed from a real equality test over the fleet's values — not a
// hardcoded field name.
func TestFleetUniformityShowsDifferingArch(t *testing.T) {
	fleet := []vmTraits{{Arch: "x86_64"}, {Arch: "x86_64"}, {Arch: "aarch64"}}
	u := computeFleetUniformity(fleet)
	if !u.ShowArch {
		t.Fatalf("a fleet with a differing arch must show the arch badge")
	}

	in := baseTileInput()
	in.VM = vm.VM{Name: "arm-box", Status: "Running", Arch: "aarch64"}
	in.Traits = vmTraits{Arch: "aarch64"}
	in.Uniform = u

	got := strings.ToLower(ansi.Strip(renderTile(in)))
	if !strings.Contains(got, "arch") || !strings.Contains(got, "aarch64") {
		t.Fatalf("the differing tile should show its arch badge, got:\n%s", got)
	}
}

// Same rule, same both-directions test, for the base-image field.
func TestFleetUniformityBaseImageBothDirections(t *testing.T) {
	uniform := computeFleetUniformity([]vmTraits{{Base: "sandbar-base"}, {Base: "sandbar-base"}})
	if uniform.ShowBase {
		t.Fatalf("a fleet cloned from one base should not show the base badge")
	}

	varied := computeFleetUniformity([]vmTraits{{Base: "sandbar-base"}, {Base: "custom-base"}})
	if !varied.ShowBase {
		t.Fatalf("a fleet with two base images should show the base badge")
	}

	in := baseTileInput()
	in.VM = vm.VM{Name: "custom-vm", Status: "Running"}
	in.Traits = vmTraits{Base: "custom-base"}
	in.Uniform = varied
	got := strings.ToLower(ansi.Strip(renderTile(in)))
	if !strings.Contains(got, "custom-base") {
		t.Fatalf("the differing tile should show its own base image, got:\n%s", got)
	}

	in2 := baseTileInput()
	in2.VM = vm.VM{Name: "web", Status: "Running"}
	in2.Traits = vmTraits{Base: "sandbar-base"}
	in2.Uniform = uniform
	got2 := strings.ToLower(ansi.Strip(renderTile(in2)))
	if strings.Contains(got2, "base") {
		t.Fatalf("a uniform base image must not render a badge, got:\n%s", got2)
	}
}

// The managed/external badge goes through the exact same generic rule as
// arch and base image — it is never special-cased away. A fleet of all
// managed (or all external) VMs hides it; a mixed fleet shows each tile's
// own value. (The board, task 08, is what makes this uniform in practice by
// filtering to managed clones only — nothing here needs to know that.)
func TestFleetUniformityManagedBothDirections(t *testing.T) {
	uniform := computeFleetUniformity([]vmTraits{{Managed: true}, {Managed: true}})
	if uniform.ShowManaged {
		t.Fatalf("an all-managed fleet should not show the managed badge")
	}
	in := baseTileInput()
	in.VM = vm.VM{Name: "web", Status: "Running"}
	in.Traits = vmTraits{Managed: true}
	in.Uniform = uniform
	if got := strings.ToLower(ansi.Strip(renderTile(in))); strings.Contains(got, "managed") || strings.Contains(got, "external") {
		t.Fatalf("a uniform managed fleet must not show the badge, got:\n%s", got)
	}

	mixed := computeFleetUniformity([]vmTraits{{Managed: true}, {Managed: false}})
	if !mixed.ShowManaged {
		t.Fatalf("a mixed fleet should show the managed badge")
	}
	in2 := baseTileInput()
	in2.VM = vm.VM{Name: "outside", Status: "Running"}
	in2.Traits = vmTraits{Managed: false}
	in2.Uniform = mixed
	got2 := strings.ToLower(ansi.Strip(renderTile(in2)))
	if !strings.Contains(got2, "external") {
		t.Fatalf("the unmanaged tile in a mixed fleet should show external, got:\n%s", got2)
	}
}

// computeFleetUniformity is a real equality test, not two hardcoded field
// checks: an empty fleet is vacuously uniform (nothing yet to disagree
// with), and a lone VM is trivially uniform with itself — both hide every
// badge. This also pins the safe zero-value: a tileInput that never sets
// Uniform at all renders identically to one explicitly computed over a
// uniform fleet (see TestRenderTileZeroValueDoesNotPanic's sibling checks
// below for the no-invented-badges case).
func TestComputeFleetUniformityEdgeCases(t *testing.T) {
	if u := computeFleetUniformity(nil); u.ShowArch || u.ShowBase || u.ShowManaged {
		t.Fatalf("an empty fleet should show no badges, got %+v", u)
	}
	if u := computeFleetUniformity([]vmTraits{{Arch: "x86_64", Base: "b", Managed: true}}); u.ShowArch || u.ShowBase || u.ShowManaged {
		t.Fatalf("a single-VM fleet should show no badges, got %+v", u)
	}
	if u := (fleetUniformity{}); u.ShowArch || u.ShowBase || u.ShowManaged {
		t.Fatalf("the zero value of fleetUniformity must hide every badge (the safe default), got %+v", u)
	}
}

// --- last used: the mtime probe. ---

func TestLastUsedNeverStartedReadsAsNeverUsed(t *testing.T) {
	dir := t.TempDir() // no ha.stderr.log, no disk file: a VM that was created but never started
	if _, ok := lastUsed(lima.LocalFiles(), dir); ok {
		t.Fatalf("a VM with no ha.stderr.log must read as never used")
	}
}

func TestLastUsedSourcesHaStderrLogMtime(t *testing.T) {
	dir := t.TempDir()
	want := time.Date(2026, 7, 9, 8, 0, 0, 0, time.UTC)
	logPath := filepath.Join(dir, "ha.stderr.log")
	if err := os.WriteFile(logPath, []byte("shutdown"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(logPath, want, want); err != nil {
		t.Fatal(err)
	}
	// A newer disk mtime must not override the real ha.stderr.log signal.
	diskPath := filepath.Join(dir, "disk")
	if err := os.WriteFile(diskPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := lastUsed(lima.LocalFiles(), dir)
	if !ok {
		t.Fatal("expected a last-used time")
	}
	if !got.Equal(want) {
		t.Fatalf("lastUsed = %v, want %v", got, want)
	}
}

func TestLastUsedEmptyDirIsNeverUsed(t *testing.T) {
	if _, ok := lastUsed(lima.LocalFiles(), ""); ok {
		t.Fatal("an empty dir must report never used")
	}
}

// --- up <duration>: the running-tile mirror of last used. ---

func TestUpSinceSourcesHaPidMtime(t *testing.T) {
	dir := t.TempDir()
	want := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	pidPath := filepath.Join(dir, "ha.pid")
	if err := os.WriteFile(pidPath, []byte("1234"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(pidPath, want, want); err != nil {
		t.Fatal(err)
	}

	got, ok := upSince(lima.LocalFiles(), dir)
	if !ok {
		t.Fatal("expected an up-since time")
	}
	if !got.Equal(want) {
		t.Fatalf("upSince = %v, want %v", got, want)
	}
}

func TestUpSinceNoPidFileIsUnknown(t *testing.T) {
	dir := t.TempDir()
	if _, ok := upSince(lima.LocalFiles(), dir); ok {
		t.Fatal("a dir with no pid file should report unknown, not a fabricated time")
	}
}

// --- Duration formatting. ---

func TestFormatUptime(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0m"},
		{14 * time.Minute, "14m"},
		{2*time.Hour + 14*time.Minute, "2h14m"},
		{25 * time.Hour, "1d1h"},
	}
	for _, c := range cases {
		if got := formatUptime(c.d); got != c.want {
			t.Errorf("formatUptime(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestFormatAgo(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{3 * 24 * time.Hour, "3d ago"},
	}
	for _, c := range cases {
		if got := formatAgo(c.d); got != c.want {
			t.Errorf("formatAgo(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// The tile's closing line is symmetric and always populated: `up <duration>`
// when running, `last used <duration> ago` when stopped, `never used` for a
// stopped VM that never started.
func TestTileFooterLineRunningVsStopped(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

	start := now.Add(-2*time.Hour - 14*time.Minute)
	if err := os.WriteFile(filepath.Join(dir, "ha.pid"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, "ha.pid"), start, start); err != nil {
		t.Fatal(err)
	}

	// The tile RENDERS a sampled time; it does not stat for one. listCmd does the
	// stat, off the Bubble Tea goroutine (see its doc) — so the test samples the same
	// way and hands the result to the tile, exactly as the real flow does.
	upAt, ok := upSince(lima.LocalFiles(), dir)
	if !ok {
		t.Fatal("upSince should read the boot time from ha.pid")
	}
	running := baseTileInput()
	running.VM = vm.VM{Name: "web", Status: "Running", Dir: dir, UpSince: upAt}
	running.Now = now
	got := ansi.Strip(renderTile(running))
	if !strings.Contains(got, "up 2h14m") {
		t.Fatalf("running tile should show up 2h14m, got:\n%s", got)
	}

	stoppedDir := t.TempDir()
	stopAt := now.Add(-3 * 24 * time.Hour)
	if err := os.WriteFile(filepath.Join(stoppedDir, "ha.stderr.log"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(stoppedDir, "ha.stderr.log"), stopAt, stopAt); err != nil {
		t.Fatal(err)
	}
	usedAt, ok := lastUsed(lima.LocalFiles(), stoppedDir)
	if !ok {
		t.Fatal("lastUsed should read the shutdown time from ha.stderr.log")
	}
	stopped := baseTileInput()
	stopped.VM = vm.VM{Name: "web", Status: "Stopped", Dir: stoppedDir, LastUsed: usedAt}
	stopped.Now = now
	got2 := ansi.Strip(renderTile(stopped))
	if !strings.Contains(got2, "last used 3d ago") {
		t.Fatalf("stopped tile should show last used 3d ago, got:\n%s", got2)
	}

	neverDir := t.TempDir()
	if _, ok := lastUsed(lima.LocalFiles(), neverDir); ok {
		t.Fatal("a never-started VM has no ha.stderr.log, so lastUsed must report nothing")
	}
	never := baseTileInput()
	never.VM = vm.VM{Name: "fresh", Status: "Stopped", Dir: neverDir} // zero LastUsed = never
	never.Now = now
	got3 := ansi.Strip(renderTile(never))
	if !strings.Contains(got3, "never used") {
		t.Fatalf("a never-started stopped VM should read never used, got:\n%s", got3)
	}
}

// --- Building tile: progress bar + role/task count, not gauges. ---

func TestBuildingTileShowsProgressAndRoleTaskCount(t *testing.T) {
	in := baseTileInput()
	in.VM = vm.VM{Name: "web", Status: "Running"}
	in.Job = jobSnapshot{
		State:     jobRunning,
		Provision: true,
		Progress:  ansibleProgress{Role: "docker", Task: "Install Docker", Index: 7, Total: 19},
	}
	in.HasJob = true

	got := ansi.Strip(renderTile(in))
	if !strings.Contains(got, "docker") || !strings.Contains(got, "7/19") {
		t.Fatalf("building tile should show the role and task count, got:\n%s", got)
	}
}

// Before Ansible starts (the long silent clone/boot stretch), the tile falls
// back to sand's own phase banner (Step) rather than showing a blank role
// line.
func TestBuildingTileFallsBackToStepBeforeAnsibleStarts(t *testing.T) {
	in := baseTileInput()
	in.VM = vm.VM{Name: "web", Status: "Running"}
	in.Job = jobSnapshot{
		State:     jobRunning,
		Provision: true,
		Progress:  ansibleProgress{Step: `Cloning "web" from base image…`},
	}
	in.HasJob = true

	got := ansi.Strip(renderTile(in))
	if !strings.Contains(got, "Cloning") {
		t.Fatalf("building tile should fall back to the Step banner, got:\n%s", got)
	}
}

// --- Colour is never the only carrier of meaning. ---

// ansi.Strip of each of the four status renderings must still distinguish
// them via glyph + word alone.
func TestAnsiStrippedStatusesAreDistinguishable(t *testing.T) {
	mk := func(v vm.VM, job jobSnapshot, hasJob bool) string {
		in := baseTileInput()
		in.VM = v
		in.Job = job
		in.HasJob = hasJob
		return ansi.Strip(renderTile(in))
	}

	running := mk(vm.VM{Name: "a", Status: "Running"}, jobSnapshot{}, false)
	stopped := mk(vm.VM{Name: "a", Status: "Stopped"}, jobSnapshot{}, false)
	building := mk(vm.VM{Name: "a", Status: "Running"}, jobSnapshot{State: jobRunning, Provision: true}, true)
	failed := mk(vm.VM{Name: "a", Status: "Running"}, jobSnapshot{State: jobFailed, Provision: true}, true)

	renders := map[string]string{"running": running, "stopped": stopped, "building": building, "failed": failed}
	words := map[string]string{"running": "Running", "stopped": "Stopped", "building": "Building", "failed": "Failed"}
	for name, word := range words {
		if !strings.Contains(renders[name], word) {
			t.Errorf("%s render missing its status word %q:\n%s", name, word, renders[name])
		}
	}
	// Pairwise distinct after stripping colour.
	seen := map[string]string{}
	for name, r := range renders {
		if other, dup := seen[r]; dup {
			t.Errorf("%s and %s render identically after ansi.Strip", name, other)
		}
		seen[r] = name
	}
}

// A focused tile's border must differ from an unfocused one in more than
// just colour (colour alone would vanish under NO_COLOR/monochrome): it
// switches border glyph sets too.
func TestFocusedTileBorderDiffersFromUnfocused(t *testing.T) {
	in := baseTileInput()
	in.VM = vm.VM{Name: "web", Status: "Running"}

	unfocused := renderTile(in)
	in.Focused = true
	focused := renderTile(in)

	if ansi.Strip(unfocused) == ansi.Strip(focused) {
		t.Fatalf("a focused tile must render visibly differently (border glyphs) from an unfocused one even with colour stripped")
	}
}

// renderTile must not panic on a completely zero-value input.
func TestRenderTileZeroValueDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("renderTile panicked on a zero-value input: %v", r)
		}
	}()
	renderTile(tileInput{})
}

// tileGaugeRow's label/bar/value arithmetic must use DISPLAY width (ansi.
// StringWidth), not byte length: a label carrying a multi-byte rune (the
// coming low-capacity warning marker, "⚠") is padded by fmt's %-*s verb to a
// given number of RUNES, whose UTF-8 byte length is wider than its terminal
// column count — using len() there would starve the bar by the difference
// and leave the row short of the tile's fixed width before tilePad's final
// safety net silently pads it back out, which would misalign the bar/value
// against every sibling row even though the overall width comes out right.
func TestTileGaugeRowUsesDisplayWidthNotByteLength(t *testing.T) {
	const width = 40
	asciiRow := tileGaugeRow("cpu", "50%", width, func(barWidth int) string { return strings.Repeat("#", barWidth) })
	warnRow := tileGaugeRow("⚠ cpu", "50%", width, func(barWidth int) string { return strings.Repeat("#", barWidth) })

	// Both labels are padded by fmt's %-*s to the SAME tileGaugeLabelWidth
	// (measured in runes/columns, not bytes), so both rows must reserve the
	// exact same bar width — regardless of "⚠" costing 3 UTF-8 bytes for one
	// display column. A byte-length-based computation would (wrongly) starve
	// the multi-byte row's bar by the extra bytes, which is exactly the bug
	// this pins closed.
	asciiBar := strings.Count(asciiRow, "#")
	warnBar := strings.Count(warnRow, "#")
	if asciiBar != warnBar {
		t.Fatalf("bar width must not depend on a label's BYTE length: ascii bar=%d, warn bar=%d, want equal", asciiBar, warnBar)
	}
}

// --- Rules 3+4: a single VM's own mem/disk gauge warns below the low-free threshold (10%). ---

// A running VM whose guest heartbeat reports less than 10% memory free (rule
// 3) gets a "⚠ mem" label and its row rendered in warnStyle instead of the
// ordinary chrome grey — while an otherwise-identical VM at or above the threshold
// renders EXACTLY as today (no marker, no colour change).
func TestTileMemGaugeWarnsBelowLowFreeThreshold(t *testing.T) {
	low := baseTileInput()
	low.VM = vm.VM{Name: "web", Status: "Running"}
	low.Sample = guestSample{HasCPU: true, CPUPct: 10, MemTotal: 1000, MemUsed: 970} // 3% free
	low.HasSample = true
	got := ansi.Strip(renderTile(low))
	if !strings.Contains(got, "⚠ mem") {
		t.Fatalf("mem free below 5%% must show the ⚠ mem marker, got:\n%s", got)
	}

	ok := baseTileInput()
	ok.VM = vm.VM{Name: "web", Status: "Running"}
	ok.Sample = guestSample{HasCPU: true, CPUPct: 10, MemTotal: 1000, MemUsed: 900} // 10% free
	ok.HasSample = true
	got2 := ansi.Strip(renderTile(ok))
	if strings.Contains(got2, "⚠") {
		t.Fatalf("mem free at 10%% must NOT show a warning marker, got:\n%s", got2)
	}
}

// The disk gauge is mem's exact twin (rule 4), using DiskUsed vs the VM's
// allocated Disk size — the same numbers tileDiskLine already renders.
func TestTileDiskGaugeWarnsBelowLowFreeThreshold(t *testing.T) {
	low := baseTileInput()
	low.VM = vm.VM{Name: "web", Status: "Stopped", Disk: "100000000000", DiskUsed: "97000000000"} // 3% free
	got := ansi.Strip(renderTile(low))
	if !strings.Contains(got, "⚠ disk") {
		t.Fatalf("disk free below 5%% must show the ⚠ disk marker, got:\n%s", got)
	}

	ok := baseTileInput()
	ok.VM = vm.VM{Name: "web", Status: "Stopped", Disk: "100000000000", DiskUsed: "50000000000"} // 50% free
	got2 := ansi.Strip(renderTile(ok))
	if strings.Contains(got2, "⚠") {
		t.Fatalf("disk free at 50%% must NOT show a warning marker, got:\n%s", got2)
	}
}

// Both warnings can be active on the same tile simultaneously.
func TestTileBothMemAndDiskWarningsSimultaneously(t *testing.T) {
	in := baseTileInput()
	in.VM = vm.VM{Name: "web", Status: "Running", Disk: "100000000000", DiskUsed: "98000000000"} // 2% free
	in.Sample = guestSample{HasCPU: true, CPUPct: 10, MemTotal: 1000, MemUsed: 980}              // 2% free
	in.HasSample = true

	got := ansi.Strip(renderTile(in))
	if !strings.Contains(got, "⚠ mem") || !strings.Contains(got, "⚠ disk") {
		t.Fatalf("both mem and disk must warn simultaneously, got:\n%s", got)
	}
}

// A VM with NO reading (heartbeat quiet, or disk unmeasurable) must never
// invent a warning — mirroring tileGaugeNoReading's own refusal.
func TestTileNoReadingNeverWarns(t *testing.T) {
	in := baseTileInput()
	in.VM = vm.VM{Name: "web", Status: "Running", Disk: "100000000000", DiskUsed: ""} // disk unmeasurable
	in.HasSample = false                                                              // no heartbeat sample at all

	got := ansi.Strip(renderTile(in))
	if strings.Contains(got, "⚠") {
		t.Fatalf("no reading must never show a warning marker, got:\n%s", got)
	}
}

// The marker must never overflow or force the tile's fixed width to change:
// a warned row still measures exactly the tile's inner width, single-cell
// glyph and all.
func TestTileWarningMarkerNeverOverflowsFixedWidth(t *testing.T) {
	in := baseTileInput()
	in.Width = 44
	in.VM = vm.VM{Name: "web", Status: "Running", Disk: "100000000000", DiskUsed: "98000000000"}
	in.Sample = guestSample{HasCPU: true, CPUPct: 10, MemTotal: 1000, MemUsed: 980}
	in.HasSample = true

	got := renderTile(in)
	lines := strings.Split(ansi.Strip(got), "\n")
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != in.Width {
			t.Fatalf("line %d width = %d, want exactly %d (the tile's fixed full width, border included): %q", i, w, in.Width, l)
		}
	}
}

// TestTileWarningRenderingGolden pins the EXACT rendering of a tile with both
// mem and disk below 5% free, at a fixed 44-column width — the low-capacity-
// warning feature's one golden, extending this file's existing inline-golden
// style (ansi.Strip + a literal expected string, as every other test above
// already asserts against) rather than a new testdata/*.golden fixture: the
// exact text is what's load-bearing here, not a whole-board render.
func TestTileWarningRenderingGolden(t *testing.T) {
	in := baseTileInput()
	in.Width = 44
	in.VM = vm.VM{Name: "web", Status: "Running", Disk: "100000000000", DiskUsed: "98000000000"} // 2% disk free
	in.Sample = guestSample{HasCPU: true, CPUPct: 10, MemTotal: 1000, MemUsed: 980}              // 2% mem free
	in.HasSample = true
	in.Now = time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

	want := strings.Join([]string{
		"╭──────────────────────────────────────────╮",
		"│ web                                      │",
		"│ ● Running                                │",
		"│ cpu      ███░░░░░░░░░░░░░░░░░░░░░░░░ 10% │",
		"│ ⚠ mem    ██████████████████ 980 B/1000 B │",
		"│ ⚠ disk   █████████████ 91.3 GiB/93.1 GiB │",
		"│ up                                       │",
		"╰──────────────────────────────────────────╯",
	}, "\n")

	got := ansi.Strip(renderTile(in))
	if got != want {
		t.Fatalf("tile rendering with both mem and disk below 5%% free changed.\ngot:\n%s\nwant:\n%s", got, want)
	}
}

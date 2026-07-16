package ui

// boardshot_test.go is the home-page screenshot GENERATOR. It is not an
// assertion — it exists so the marketing/docs board image (docs/images/board.png)
// can be regenerated deterministically whenever the TUI's colours or layout
// change, instead of being hand-captured from a live `sand` and lost the moment
// the palette moves.
//
// It builds the board white-box (this is package ui) with a fixed, hand-seeded
// fleet, writes the COLOURED final render (View().Content, WITHOUT ansi.Strip —
// the colour escapes are the whole point) to the path in BOARD_SHOT_OUT, and
// skips entirely when that env var is unset, so a normal `go test ./internal/ui/`
// never runs it and CI stays green.
//
// The seeded board (matches docs/images/board.png tile-for-tile):
//   - version "0.5.0", host pinned to 16 cores / 16 GiB RAM / 100 GiB free.
//   - drupal-contrib: Running, x86_64, 4 vCPUs, up 3h42m; live heartbeat 34% cpu,
//     3.4 GiB / 8 GiB mem; disk 34 GiB / 100 GiB. FOCUSED (blue border).
//     Header "cpu 8%" falls out of 34% x 4 vCPUs / 16 host cores = 8.5 -> 8.
//   - lullabotdotcom: Stopped, aarch64, disk 12 GiB / 50 GiB, last used 2d ago.
//   - Messages: the three exact lines seeded via logMsg below.
//
// Regenerate the PNG with charmbracelet/freeze (JetBrains Mono is bundled in the
// freeze module, so --font.file points at it — it need not be installed):
//
//	BOARD_SHOT_OUT=/tmp/board.ansi go test ./internal/ui/ -run TestGenerateHomeBoardShot -count=1
//	freeze /tmp/board.ansi -o board.png \
//	  --font.file <freeze-module>/font/JetBrainsMono-Regular.ttf \
//	  --font.family "JetBrains Mono" --font.size 20 --line-height 1.6 \
//	  --background "#0d1117" --padding 24 --window --border.radius 8

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

func TestGenerateHomeBoardShot(t *testing.T) {
	out := os.Getenv("BOARD_SHOT_OUT")
	if out == "" {
		t.Skip("set BOARD_SHOT_OUT to regenerate the home-page board screenshot")
	}

	// Deterministic host + managed index, exactly as the goldens pin them.
	isolateHostState(t)
	pinHostCapacity(t, 16<<30, 100<<30) // 16 GiB RAM, 100 GiB free disk, 16 cores
	pinVersion(t, "0.5.0")
	seedManagedIndex(t, "drupal-contrib", "lullabotdotcom")

	// The lima runner is never consulted: the board is hand-built below, not driven.
	cli := lima.New(listFakeRunner{})
	prov := &provision.Provisioner{Lima: cli}
	m := New(singleFleet(provider.NewLocalLima(cli, prov), registry.LocalScope)).(model)

	const gib = int64(1) << 30
	now := time.Now()

	m.members[0].vms = []vm.VM{
		{
			Name:     "drupal-contrib",
			Status:   "Running",
			CPUs:     4,
			Memory:   strconv.FormatInt(8*gib, 10),
			Disk:     strconv.FormatInt(100*gib, 10),
			DiskUsed: strconv.FormatInt(34*gib, 10),
			Arch:     "x86_64",
			UpSince:  now.Add(-3*time.Hour - 42*time.Minute),
		},
		{
			Name:     "lullabotdotcom",
			Status:   "Stopped",
			CPUs:     2,
			Memory:   strconv.FormatInt(4*gib, 10),
			Disk:     strconv.FormatInt(50*gib, 10),
			DiskUsed: strconv.FormatInt(12*gib, 10),
			Arch:     "aarch64",
			LastUsed: now.Add(-60 * time.Hour), // squarely inside the "2d ago" bucket
		},
	}
	m.members[0].state = connConnected
	m.focusVM.Name = "drupal-contrib" // the focused (blue-bordered) tile

	// A live guest heartbeat for the running VM: cpu 34%, mem 3.4 GiB of 8 GiB.
	// latest() only reads seen+last, so a bare struct is a complete reading here.
	m.heartbeats.beats[vmHandle{Scope: registry.LocalScope, Name: "drupal-contrib"}] = &heartbeat{
		seen: true,
		last: guestSample{
			CPUPct:   34,
			HasCPU:   true,
			MemUsed:  uint64(34 * gib / 10), // 3.4 GiB
			MemTotal: uint64(8 * gib),       // 8 GiB
		},
	}

	// The three Messages lines, oldest first (the strip renders newest last).
	m.logMsg("starting drupal-contrib…")
	m.logMsg("secrets saved for drupal-contrib — applying to the running VM…")
	m.logMsg("opened drupal-contrib in a new tmux window")

	m.applySize(160, 40) // the wide board

	content := m.View().Content // NO ansi.Strip: keep the colour escapes
	if err := os.WriteFile(out, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", out, err)
	}
}

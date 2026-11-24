package ui

// docshots_test.go is the documentation screenshot GENERATOR, the docs-site
// counterpart of boardshot_test.go's home-page image. It is not an assertion:
// it writes the COLOURED render of each screen a docs page shows to
// $DOC_SHOTS_DIR/<name>.ansi, and skips when that variable is unset, so a
// normal `go test` never runs it. `freeze` turns each file into the PNG under
// docs/images/ (see "Regenerating the docs screenshots" in
// docs/contributing/development.md).
//
// Each scene reaches its screen the way a user does wherever it can: real key
// presses through Update, the real form submit, the real build-finished
// message. Only what would need a live VM is seeded: the provider is a fake,
// the build log is a recorded Ansible run, and the guest heartbeat is a fixed
// reading. That keeps the images reproducible (no host stats, no real build)
// while the rendering — and as much of the path to it as possible — is the
// application's own. A refactor that breaks a scene's path fails here rather
// than leaving a screenshot of a screen users can no longer reach.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// docShotWidth/Height is one terminal size for every docs image, so they line
// up across pages. 100x30 fits the whole New VM form without scrolling.
const (
	docShotWidth  = 100
	docShotHeight = 30
)

func TestGenerateDocShots(t *testing.T) {
	dir := os.Getenv("DOC_SHOTS_DIR")
	if dir == "" {
		t.Skip("set DOC_SHOTS_DIR to regenerate the docs screenshots")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, m model) {
		t.Helper()
		path := filepath.Join(dir, name+".ansi")
		// The build spinner's frames are braille characters, which JetBrains
		// Mono (the one font freeze embeds) cannot draw: each would render as
		// a missing-glyph box. A still image shows one frame anyway, so the
		// dotted circle, which the font has, stands in for it.
		content := strings.Map(func(r rune) rune {
			if r >= 0x2800 && r <= 0x28FF {
				return '◌'
			}
			return r
		}, m.View().Content)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	isolateHostState(t)
	pinHostCapacity(t, 16<<30, 200<<30) // 16 GiB RAM, 200 GiB free disk, 16 cores
	pinVersion(t, "0.8.0")

	m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)
	m = resized(m, docShotWidth, docShotHeight)
	m = docShotUpdate(t, m, vmsLoadedMsg{scope: registry.LocalScope})

	// Quickstart step 1: the empty board, focus on the add-a-VM tile.
	write("quickstart-board-empty", m)

	// Quickstart step 2: the New VM form, filled in as the page says.
	m, cmd := press(t, m, runeKey('n'))
	deliverToolsetLoad(t, &m, cmd)
	m.inputs[fName].SetValue("drupal")
	m.inputs[fGitName].SetValue("Alex Example")
	m.inputs[fGitEmail].SetValue("alex@example.com")
	m.inputs[fCloneURL].SetValue("https://github.com/acme/mysite")
	// Values the form seeds from the machine running the test, pinned so the
	// image shows neither this machine's user nor its free disk.
	m.inputs[fUser].SetValue("alex")
	m.inputs[fCPUs].SetValue("4")
	m.hostDiskFree = 200 << 30
	// Move down to the checkbox with the arrow key, as the reader does.
	for range 30 {
		if m.toggleFocus >= 0 && m.createToggles()[m.toggleFocus].label == "Install Claude Code" {
			break
		}
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeySpace})
	if !m.toolClaude {
		t.Fatal("space on Install Claude Code did not check it")
	}
	write("quickstart-new-vm-form", m)

	// ctrl+s submits the form and returns to the board with a building tile.
	// The returned command would start the real build; it is deliberately not
	// run, and the recorded log below stands in for its output.
	m, _ = press(t, m, ctrlKey('s'))
	if m.view != viewBoard {
		t.Fatalf("ctrl+s should return to the board, view = %v", m.view)
	}
	key := provisionKey(registry.LocalScope, "drupal")
	m.jobs.addOutput(key, recordedBuildLog(t, 30))
	write("quickstart-building", m)

	// The build finishes: Lima lists the VM, and its first heartbeat lands.
	m = docShotUpdate(t, m, provisionDoneMsg{job: key})
	const gib = int64(1) << 30
	m = docShotUpdate(t, m, vmsLoadedMsg{scope: registry.LocalScope, vms: []vm.VM{{
		Name:     "drupal",
		Status:   "Running",
		CPUs:     4,
		Memory:   strconv.FormatInt(8*gib, 10),
		Disk:     strconv.FormatInt(100*gib, 10),
		DiskUsed: strconv.FormatInt(9*gib, 10),
		Arch:     "aarch64",
		UpSince:  time.Now().Add(-4 * time.Minute),
	}}})
	m.heartbeats.beats[vmHandle{Scope: registry.LocalScope, Name: "drupal"}] = &heartbeat{
		seen: true,
		last: guestSample{
			CPUPct: 3, HasCPU: true,
			MemUsed: uint64(18 * gib / 10), MemTotal: uint64(8 * gib),
			Cache: uint64(5 * gib / 10), HasCache: true,
			// Local Lima's host-side reading (the VM process's RSS), which
			// the tile and header prefer over the guest's own figure.
			HostMemUsed: uint64(21 * gib / 10), HasHostMem: true,
			DiskUsed: uint64(9 * gib), DiskTotal: uint64(98 * gib),
		},
	}
	write("quickstart-running", m)
}

// docShotUpdate delivers one message through the real Update path. The
// commands it returns (refresh ticks, heartbeats) are dropped: a scene is a
// still frame, and running them would reach for a live host.
func docShotUpdate(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(model)
}

// recordedBuildLog is the start of a real finalize run's output, cut after the
// n-th Ansible task, so the tile's progress bar shows a real fraction parsed by
// the real parser.
func recordedBuildLog(t *testing.T, n int) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "ansible-finalize.log"))
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	tasks := 0
	for _, line := range strings.SplitAfter(string(raw), "\n") {
		if strings.HasPrefix(line, taskPrefix) {
			tasks++
			if tasks > n {
				break
			}
		}
		out.WriteString(line)
	}
	return out.String()
}

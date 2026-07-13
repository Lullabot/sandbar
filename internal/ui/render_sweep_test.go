package ui

import (
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"

	"github.com/charmbracelet/x/ansi"
)

// GOLDENS PIN THE RESPONSIVE RANGE; THIS SWEEP IS ITS COMPLEMENT: it proves
// there is no terminal size — however cramped, however vast — at which the
// board fails to render at all. layout_test.go already pins this at the pure
// classify(w, h) level (task 03: TestClassifyNoSizeIsUnrenderable); this
// asserts it end to end, through the REAL board renderer, over a fleet that
// actually has tiles to draw and a hidden VM to disclose — the surfaces this
// task added on top of classify's budgets.
func TestBoardRendersAtEverySizeInTheSweep(t *testing.T) {
	pinHostCapacity(t, 16<<30, 100<<30)
	m := newTestModel(t)
	if err := m.reg.Add(vm.CreateConfig{Name: "web", BaseName: "claude-base"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "web", Status: "Running"},
		{Name: "claude-base", Status: "Stopped"}, // hidden: a base image
	}})
	m = loaded.(model)

	widths := []int{1, 5, 10, 20, 40, 60, 80, 100, 120, 160, 200, 400}
	heights := []int{1, 3, 5, 10, 15, 20, 24, 30, 40, 60, 100}

	for _, w := range widths {
		for _, h := range heights {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("boardView panicked at %dx%d: %v", w, h, r)
					}
				}()
				mm := resized(m, w, h)
				view := ansi.Strip(mm.boardView())
				if view == "" {
					t.Fatalf("boardView() at %dx%d rendered nothing", w, h)
				}
				if strings.Contains(strings.ToLower(view), "too small") {
					t.Fatalf("boardView() at %dx%d hit a \"terminal too small\" wall:\n%s", w, h, view)
				}
			}()
		}
	}
}

// The VM (detail) screen gets the same sweep: its footer is the one that
// used to overflow the terminal (gap (a) task 08 handed task 09), so it is
// worth its own pass rather than trusting the board's sweep to cover it.
func TestDetailViewRendersAtEverySizeInTheSweep(t *testing.T) {
	m := newTestModel(t)
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{{Name: "claude", Status: "Running", CPUs: 2}}})
	m = loaded.(model)
	m.view = viewDetail
	m.detail, _ = m.lookupVM("claude")

	for _, w := range []int{1, 10, 20, 40, 80, 100, 160, 400} {
		for _, h := range []int{1, 5, 10, 24, 30, 60} {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("detailView panicked at %dx%d: %v", w, h, r)
					}
				}()
				mm := resized(m, w, h)
				view := ansi.Strip(mm.detailView())
				if view == "" {
					t.Fatalf("detailView() at %dx%d rendered nothing", w, h)
				}
				if strings.Contains(strings.ToLower(view), "too small") {
					t.Fatalf("detailView() at %dx%d hit a \"terminal too small\" wall:\n%s", w, h, view)
				}
			}()
		}
	}
}

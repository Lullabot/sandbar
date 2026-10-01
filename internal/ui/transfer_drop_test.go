package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"
	"github.com/lullabot/sandbar/internal/browse"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// This wrapper observes transitions on the program's own goroutine. The tests
// feed terminal bytes rather than manufacturing PasteMsg, exercising Bubble
// Tea's bracketed-paste decoder as well as Sand's routing and copy dispatch.
type transferState struct {
	view      view
	source    string
	recursive bool
	browser   string
}

type transferProgram struct {
	model
	initial tea.Cmd
	changed chan transferState
}

func (m transferProgram) Init() tea.Cmd { return m.initial }
func (m transferProgram) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.model.Update(msg)
	m.model = next.(model)
	select {
	case m.changed <- transferState{m.view, m.transferSrc, m.transferRecursive, ansi.Strip(m.browser.View())}:
	default:
	}
	return m, cmd
}

type transferredPaths struct {
	recursive bool
	src, dst  string
}

func TestTransferDroppedPathsThroughTerminal(t *testing.T) {
	for _, bracketed := range []bool{true, false} {
		for _, upload := range []bool{true, false} {
			for _, directory := range []bool{false, true} {
				t.Run(fmt.Sprintf("bracketed=%v/upload=%v/directory=%v", bracketed, upload, directory), func(t *testing.T) {
					isolateHostState(t)
					root := t.TempDir()
					localSrc := filepath.Join(root, "My 雪 File.txt")
					if directory {
						localSrc = filepath.Join(root, "My 雪 Folder")
						if err := os.Mkdir(localSrc, 0700); err != nil {
							t.Fatal(err)
						}
					} else if err := os.WriteFile(localSrc, []byte("payload"), 0600); err != nil {
						t.Fatal(err)
					}
					localDest := filepath.Join(root, "My Downloads")
					if err := os.Mkdir(localDest, 0700); err != nil {
						t.Fatal(err)
					}
					guestSrc := "/guest/" + filepath.Base(localSrc)
					guestDest := "/guest/My Destination"
					copied := make(chan transferredPaths, 1)
					prov := &providerfake.Provider{
						GuestHomeFunc: func(vm.VM) string { return "/guest" },
						GuestPathFunc: func(name, p string) string { return name + ":" + p },
						ShellFunc: func(_ context.Context, _ string, _ io.Reader, out io.Writer, argv ...string) error {
							if len(argv) < 2 || argv[0] != "find" {
								return fmt.Errorf("unexpected command: %v", argv)
							}
							if argv[1] == "/guest" {
								typ := "f"
								if directory {
									typ = "d"
								}
								fmt.Fprintf(out, "%s\t7\t%s\nd\t0\tMy Destination\n", typ, filepath.Base(localSrc))
							}
							return nil
						},
						CopyFunc: func(_ context.Context, _ io.Writer, recursive bool, src, dst string) error {
							copied <- transferredPaths{recursive, src, dst}
							return nil
						},
					}
					m := New(singleFleet(prov, registry.LocalScope)).(model)
					target := boardVM{VM: vm.VM{Name: "web", Status: "Running"}, scope: registry.LocalScope}
					next, initial := m.startTransfer(target, upload)
					m = resized(next.(model), 100, 30)
					changed := make(chan transferState, 128)
					reader, writer := io.Pipe()
					defer reader.Close()
					defer writer.Close()
					program := tea.NewProgram(transferProgram{m, initial, changed}, tea.WithInput(reader), tea.WithOutput(io.Discard), tea.WithoutSignals(), tea.WithWindowSize(100, 30))
					done := make(chan error, 1)
					go func() { _, err := program.Run(); done <- err }()
					defer program.Kill()
					drop := func(p string, selectSource bool) {
						t.Helper()
						// A trailing space and shell-escaped spaces mirror a terminal file drop.
						text := strings.ReplaceAll(p, " ", `\ `) + " "
						input := "\x1b[200~" + text + "\x1b[201~"
						if !bracketed {
							input = "\x0c" + text // ctrl+l opens/clears path entry
							if selectSource {
								input += "\r"
							}
						}
						if _, err := io.WriteString(writer, input); err != nil {
							t.Fatal(err)
						}
					}
					source, dest := guestSrc, localDest
					wantSrc, wantDst := "web:"+guestSrc, localDest
					if upload {
						source, dest = localSrc, guestDest
						wantSrc, wantDst = localSrc, "web:"+guestDest
					}
					drop(source, true)
					deadline := time.After(5 * time.Second)
					for {
						select {
						case state := <-changed:
							if state.view == viewDest {
								if state.source != source || state.recursive != directory {
									t.Fatalf("selected %q, recursive=%v", state.source, state.recursive)
								}
								goto destination
							}
						case <-deadline:
							t.Fatal("dropped source did not reach destination prompt")
						}
					}
				destination:
					select {
					case got := <-copied:
						t.Fatalf("drop started copy without confirmation: %+v", got)
					default:
					}
					drop(dest, false)
					if _, err := io.WriteString(writer, "\x13"); err != nil {
						t.Fatal(err)
					} // ctrl+s
					select {
					case got := <-copied:
						if got != (transferredPaths{directory, wantSrc, wantDst}) {
							t.Fatalf("copy = %+v; want recursive=%v src=%q dst=%q", got, directory, wantSrc, wantDst)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("confirmed transfer did not reach provider.Copy")
					}
					program.Quit()
					select {
					case err := <-done:
						if err != nil {
							t.Fatal(err)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("program did not quit")
					}
				})
			}
		}
	}
}

func TestTransferInvalidDestinationBlocksCopy(t *testing.T) {
	isolateHostState(t)
	called := false
	prov := &providerfake.Provider{CopyFunc: func(context.Context, io.Writer, bool, string, string) error { called = true; return nil }}
	m := New(singleFleet(prov, registry.LocalScope)).(model)
	m.view = viewDest
	m.dest, _ = browse.NewDestInput("dest: ", "/previous", nil)
	next, _ := m.Update(tea.PasteMsg{Content: `'/one' '/two'`})
	next, cmd := next.(model).Update(ctrlKey('s'))
	if cmd != nil || called || next.(model).view != viewDest {
		t.Fatal("invalid destination started a copy")
	}
}

// The path field and its instructions must remain visible at 80x24.
func TestTUITransferPathEntry(t *testing.T) {
	isolateHostState(t)
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "notes.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	m := resized(New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model), 80, 24)
	next, _ := m.startTransfer(boardVM{VM: vm.VM{Name: "web", Status: "Running"}, scope: registry.LocalScope}, true)
	m = next.(model)
	changed := make(chan transferState, 128)
	tm := teatest.NewTestModel(t, transferProgram{model: m, initial: m.browser.Open(tmp), changed: changed}, teatest.WithInitialTermSize(80, 24))
	waitForText(t, tm, "notes.txt")
	tm.Send(ctrlKey('l'))
	tm.Type("/tmp/example path")
	deadline := time.After(5 * time.Second)
	for {
		select {
		case state := <-changed:
			if strings.Contains(state.browser, "/tmp/example path") {
				goto typed
			}
		case <-deadline:
			t.Fatal("path field lost typed characters")
		}
	}
typed:
	screen := finalScreen(t, tm)
	if lipgloss.Height(string(screen)) > 25 {
		t.Fatalf("path picker exceeds 24 rows: %s", screen)
	}
	teatest.RequireEqualOutput(t, screen)
}

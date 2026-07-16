package ui

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lullabot/sandbar/internal/browse"
	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// copyArgsRunner records the argv of the `limactl copy` it is asked to stream, so
// a test can assert on the endpoints the UI actually hands to limactl rather than
// on an intermediate the UI computes and might not use.
type copyArgsRunner struct {
	mu   sync.Mutex
	args []string
}

func (r *copyArgsRunner) Output(context.Context, ...string) ([]byte, error) { return nil, nil }

func (r *copyArgsRunner) Stream(_ context.Context, _ io.Reader, _ io.Writer, args ...string) error {
	if len(args) > 0 && args[0] == "copy" {
		r.mu.Lock()
		r.args = append([]string(nil), args...)
		r.mu.Unlock()
	}
	return nil
}

func (r *copyArgsRunner) StreamOut(_ context.Context, _ io.Reader, _ io.Writer, _ ...string) error {
	return nil
}

func (r *copyArgsRunner) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.args
}

// The destination handed to limactl is the directory the user picked, VERBATIM —
// for a directory source exactly as for a file. lima.Copy places the source INSIDE
// the destination (scp's semantics, and why that backend is pinned), so appending
// the source's basename here would nest a directory one level too deep the moment
// the destination already contained it: a re-upload of mydir landing in
// dest/mydir/mydir. That appending is what this test replaced.
func TestTransferDestIsTheUsersDirectoryVerbatim(t *testing.T) {
	cases := []struct {
		name             string
		upload           bool
		recursive        bool
		destDir, srcPath string
		wantSrc, wantDst string
	}{
		{
			name: "upload a directory", upload: true, recursive: true,
			destDir: "/home/a.guest/proj", srcPath: "/Users/a/work/mydir",
			wantSrc: "/Users/a/work/mydir", wantDst: "web:/home/a.guest/proj",
		},
		{
			name: "download a directory", upload: false, recursive: true,
			destDir: "/Users/a/dl", srcPath: "/home/a.guest/proj/mydir",
			wantSrc: "web:/home/a.guest/proj/mydir", wantDst: "/Users/a/dl",
		},
		{
			name: "upload a file", upload: true, recursive: false,
			destDir: "/home/a.guest/proj", srcPath: "/Users/a/work/notes.txt",
			wantSrc: "/Users/a/work/notes.txt", wantDst: "web:/home/a.guest/proj",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &copyArgsRunner{}
			m := newTestModelWithCli(t, lima.New(rec))
			m.view = viewDest
			m.transferVM = "web"
			m.transferScope = registry.LocalScope
			m.transferSrc = tc.srcPath
			m.transferUpload = tc.upload
			m.transferRecursive = tc.recursive
			m.dest, _ = browse.NewDestInput("Destination dir: ", tc.destDir, nil)

			after, cmd := m.Update(ctrlKey('s'))
			m = after.(model)
			l := newTeaLoop(t, m)
			l.exec(cmd)
			l.pump("the copy to reach limactl", func(model) bool { return rec.seen() != nil })

			args := rec.seen()
			src, dst := args[len(args)-2], args[len(args)-1]
			if src != tc.wantSrc || dst != tc.wantDst {
				t.Fatalf("limactl copy endpoints = (%q, %q), want (%q, %q)\nfull argv: %v",
					src, dst, tc.wantSrc, tc.wantDst, args)
			}
		})
	}
}

// updateBrowse's upload-destination branch must derive the target VM (and thus
// the guest destination lister/default dir) from m.transferVM — the transfer's
// own VM — not from whichever tile happens to be under the board's focus ring.
// This is the wrong-VM bug documented at transfer.go:27-33: it used to read
// m.detail (the VM screen's own record), which was harmless only while the VM
// screen was the sole place a verb could fire from. The focus ring here sits on
// a DIFFERENT vm than the transfer targets, so a regression back to reading focus
// instead of m.transferVM would seed the destination from the wrong VM's config.
func TestBrowseSelectionTargetsTransferVMNotFocus(t *testing.T) {
	m := newTestModel(t)
	if err := m.reg.Add(vm.CreateConfig{
		Name: "focus-vm", BaseName: "sandbar-base", CloneURL: "https://github.com/org/focus-repo",
	}); err != nil {
		t.Fatalf("seed focus-vm: %v", err)
	}
	if err := m.reg.Add(vm.CreateConfig{
		Name: "xfer-vm", BaseName: "sandbar-base", CloneURL: "https://github.com/org/xfer-repo",
	}); err != nil {
		t.Fatalf("seed xfer-vm: %v", err)
	}
	m = putOnBoard(t, m, vm.VM{Name: "focus-vm", Status: "Running"})
	m = putOnBoard(t, m, vm.VM{Name: "xfer-vm", Status: "Running"})
	m.focusVM.Name = "focus-vm" // the ring is on a DIFFERENT VM than the transfer targets

	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	// Mirror startTransfer's upload setup directly (as TestTransferDestIsTheUsersDirectoryVerbatim
	// does), so the browser lists a temp dir we control instead of the real cwd.
	m.transferVM = "xfer-vm"
	m.transferScope = registry.LocalScope
	m.transferUpload = true
	m.browser = browse.NewBrowser(browse.NewLocalLister(), "Upload")
	m.browser.SetSize(80, 20)
	m.view = viewBrowse

	l := newTeaLoop(t, m)
	l.exec(m.browser.Open(tmp))
	l.pump("the temp dir to load", func(mm model) bool {
		return strings.Contains(ansi.Strip(mm.browser.View()), "notes.txt")
	})

	after, _ := l.m.Update(ctrlKey('s')) // browse.selectKey: choose the highlighted entry
	m = after.(model)

	if m.view != viewDest {
		t.Fatalf("selecting an entry should advance to the destination prompt, view=%v", m.view)
	}
	wantDef := "/home/" + vm.HostUser() + "/github.com/org/xfer-repo"
	if got := m.dest.Value(); got != wantDef {
		t.Fatalf("dest default = %q, want %q (derived from transferVM %q, not focused VM %q)",
			got, wantDef, m.transferVM, "focus-vm")
	}
}

// updateDest's Esc arm must clear the browser's pending selection
// (browser.ClearSelection, transfer.go:101) before returning to the browser.
// Without that clear, updateBrowse's `if p, isDir, ok := m.browser.Selected(); ok`
// check (transfer.go:68) would immediately see the stale selection on the very
// next keystroke and bounce the user straight back to the destination prompt,
// making Esc un-escapable.
func TestDestEscClearsBrowserSelection(t *testing.T) {
	m := newTestModel(t)
	m = putOnBoard(t, m, vm.VM{Name: "claude", Status: "Running"})

	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	m.transferVM = "claude"

	m.transferScope = registry.LocalScope
	m.transferUpload = false
	m.browser = browse.NewBrowser(browse.NewLocalLister(), "Download")
	m.browser.SetSize(80, 20)
	m.view = viewBrowse

	l := newTeaLoop(t, m)
	l.exec(m.browser.Open(tmp))
	l.pump("the temp dir to load", func(mm model) bool {
		return strings.Contains(ansi.Strip(mm.browser.View()), "notes.txt")
	})

	selected, _ := l.m.Update(ctrlKey('s')) // pick the highlighted entry
	m = selected.(model)
	if m.view != viewDest {
		t.Fatalf("precondition: selecting an entry should reach the destination prompt, view=%v", m.view)
	}
	if _, _, ok := m.browser.Selected(); !ok {
		t.Fatalf("precondition: the browser should hold a pending selection")
	}

	after, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = after.(model)

	if m.view != viewBrowse {
		t.Fatalf("esc from the destination prompt should return to the browser, view=%v", m.view)
	}
	if _, _, ok := m.browser.Selected(); ok {
		t.Fatal("esc should clear the browser's pending selection")
	}

	// The regression this guards: without ClearSelection, the next keystroke sees
	// the still-pending selection and bounces straight back into viewDest.
	bounced, _ := m.Update(runeKey('j'))
	m = bounced.(model)
	if m.view != viewBrowse {
		t.Fatalf("a keystroke after esc must not bounce back to the destination prompt, view=%v", m.view)
	}
}

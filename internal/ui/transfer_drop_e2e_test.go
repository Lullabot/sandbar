//go:build limae2e

package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// A transfer needs SSH, not containers or host filesystem mounts. Disabling
// those defaults keeps the fixture independent of container-tool installation.
const transferE2EOverlay = `base:
- template:_images/debian-13
cpus: 2
memory: "2GiB"
disk: "` + vm.BaseDiskFloor + `"
mounts: []
containerd:
  system: false
  user: false
`

// The byte-decoder test uses a fake provider; this gated test checks the far
// side of the real copy operation, including directory nesting and spaces.
func TestE2EDroppedTransferPathsMoveContents(t *testing.T) {
	if os.Getenv("LIMA_E2E") == "" {
		t.Skip("set LIMA_E2E=1 and use -tags limae2e for real VM transfers")
	}
	if _, err := exec.LookPath("limactl"); err != nil {
		t.Skip("limactl unavailable")
	}
	isolateHostState(t)
	cli := lima.New(lima.NewExecRunner())
	const name = "sand-dropped-path-e2e"
	t.Cleanup(func() { _ = cli.Delete(name, true) })
	overlay := filepath.Join(t.TempDir(), "vm.yaml")
	if err := os.WriteFile(overlay, []byte(transferE2EOverlay), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cli.Create(name, overlay); err != nil {
		t.Fatal(err)
	}
	v, err := cli.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	prov := provider.NewLocalLima(cli, &provision.Provisioner{Lima: cli})
	m := New(singleFleet(prov, registry.LocalScope)).(model)
	m = putOnBoard(t, m, v)
	target := boardVM{VM: v, scope: registry.LocalScope}
	const guestDest = "/tmp/sand drop destination"
	e2eGuestOut(t, cli, name, "mkdir", "-p", guestDest)
	for _, directory := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "directory"}[directory], func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "My 雪 File.txt")
			payload := "dropped path transfer payload"
			if directory {
				source = filepath.Join(root, "My 雪 Folder")
				if err := os.Mkdir(source, 0700); err != nil {
					t.Fatal(err)
				}
			}
			file := source
			if directory {
				file = filepath.Join(source, "My File.txt")
			}
			if err := os.WriteFile(file, []byte(payload), 0600); err != nil {
				t.Fatal(err)
			}
			run := func(upload bool, src, dest string) {
				t.Helper()
				next, initial := m.startTransfer(target, upload)
				l := newTeaLoop(t, next.(model))
				l.exec(initial)
				l.send(tea.PasteMsg{Content: "'" + src + "'"})
				pumpTimeout(t, l, "dropped source validation", 30*time.Second, func(m model) bool { return m.view == viewDest })
				l.send(tea.PasteMsg{Content: "'" + dest + "'"})
				l.send(ctrlKey('s'))
				key := transferKey(registry.LocalScope, name)
				pumpTimeout(t, l, "real transfer completion", 60*time.Second, func(m model) bool { j, ok := m.jobs.snapshot(key); return ok && j.State != jobRunning })
				job, _ := l.m.jobs.snapshot(key)
				if job.State != jobSucceeded {
					t.Fatalf("copy failed: %s", job.Output)
				}
				m = l.m
			}
			run(true, source, guestDest)
			guestSource := guestDest + "/" + filepath.Base(source)
			guestFile := guestSource
			if directory {
				guestFile += "/My File.txt"
			}
			if got := e2eGuestOut(t, cli, name, "cat", guestFile); got != payload {
				t.Fatalf("guest content=%q", got)
			}
			download := t.TempDir()
			run(false, guestSource, download)
			downloaded := filepath.Join(download, filepath.Base(source))
			if directory {
				downloaded = filepath.Join(downloaded, "My File.txt")
			}
			got, err := os.ReadFile(downloaded)
			if err != nil || string(got) != payload {
				t.Fatalf("downloaded content=%q, err=%v", got, err)
			}
		})
	}
}

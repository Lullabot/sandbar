// Package browse provides a source-agnostic directory browser for the TUI's
// file-transfer flow: a DirLister seam with a host (os.ReadDir) and a guest
// (limactl shell … find) implementation, one reusable bubbles/list Browser that
// renders either side, and a destination-path prompt with drag-drop/paste
// normalization. Nothing here shells out on the render goroutine — the guest
// lister's single SSH round-trip is meant to be driven from a tea.Cmd.
package browse

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// DirEntry is one directory member, normalized so the host and guest listers
// return the same shape. Size is best-effort (0 when unknown).
type DirEntry struct {
	Name  string
	IsDir bool
	Size  int64
}

// DirLister lists the immediate children of path. Implementations must be safe
// to call off the Update goroutine (the guest variant shells out over SSH).
type DirLister interface {
	List(ctx context.Context, path string) ([]DirEntry, error)
}

// localLister lists host directories via os.ReadDir.
type localLister struct{}

// NewLocalLister returns a DirLister over the host filesystem.
func NewLocalLister() DirLister { return localLister{} }

func (localLister) List(_ context.Context, path string) ([]DirEntry, error) {
	des, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	out := make([]DirEntry, 0, len(des))
	for _, de := range des {
		// A per-entry Info() error (e.g. a broken symlink) is tolerated: the entry
		// is still listed, just with size 0, rather than failing the whole listing.
		var size int64
		if fi, err := de.Info(); err == nil {
			size = fi.Size()
		}
		out = append(out, DirEntry{Name: de.Name(), IsDir: de.IsDir(), Size: size})
	}
	return out, nil
}

// GuestShell is the narrow backend capability guestLister needs: a single
// merged-output guest command per listing. Any provider (the local Lima
// provider, and eventually a remote one) that can run a command in a named
// instance satisfies this without guestLister ever seeing the concrete
// backend type — see provider.Provider.Shell, whose signature this mirrors.
type GuestShell interface {
	Shell(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error
}

// guestLister lists a guest directory with a single `find … -printf` over
// the backend's guest shell, parsing its tab-separated
// "<type>\t<size>\t<name>" output.
type guestLister struct {
	sh GuestShell
	vm string
}

// NewGuestLister returns a DirLister over the given VM's filesystem, backed by
// sh.Shell. find -printf is GNU findutils, present on the apt-based
// Debian/Ubuntu guests.
func NewGuestLister(sh GuestShell, vm string) DirLister { return guestLister{sh: sh, vm: vm} }

func (g guestLister) List(ctx context.Context, path string) ([]DirEntry, error) {
	var buf bytes.Buffer
	// find prints one line per entry: "<type>\t<size>\t<name>" (a single SSH
	// round-trip). -mindepth/-maxdepth 1 restricts it to immediate children.
	if err := g.sh.Shell(ctx, g.vm, nil, &buf,
		"find", path, "-mindepth", "1", "-maxdepth", "1",
		"-printf", "%y\t%s\t%f\n"); err != nil {
		return nil, fmt.Errorf("list guest %s:%s: %w (%s)", g.vm, path, err, strings.TrimSpace(buf.String()))
	}
	var out []DirEntry
	sc := bufio.NewScanner(&buf)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "\t", 3)
		if len(parts) != 3 {
			continue // skip a malformed line rather than failing the whole listing
		}
		// %y is 'd' (dir), 'f' (file), or 'l' (symlink); only 'd' counts as a
		// directory for v1. Size defaults to 0 on a parse failure.
		size, _ := strconv.ParseInt(parts[1], 10, 64)
		out = append(out, DirEntry{Name: parts[2], IsDir: parts[0] == "d", Size: size})
	}
	return out, sc.Err()
}

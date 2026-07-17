// Package paste is the single reusable operation both the CLI (`sand
// paste-image`) and the TUI's paste verb call: read the host clipboard image
// (internal/clipboard) and, if one is present, deliver it into the target
// guest's single-slot file `<guest-home>/.sand/clip/latest.png` in one round
// trip. It returns a typed Result so callers render a message without
// re-deriving what happened.
//
// This package deliberately does not check whether the VM is Running. Both
// callers already have to resolve/guard that themselves (the CLI mirrors
// shell.go's status check; the TUI's command registry withholds verbs from a
// non-running VM via commandreg.go's enabledFor), so PasteImage stays
// single-purpose: clipboard in, guest file out. Passing a non-running VM's
// name here surfaces as the underlying Shell error, not a distinct Result.
package paste

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/lullabot/sandbar/internal/clipboard"
	"github.com/lullabot/sandbar/internal/vm"
)

// Status distinguishes the shapes a paste attempt can end in.
type Status int

const (
	// Staged means the clipboard held an image and it was written to the guest.
	Staged Status = iota
	// NoImage means the clipboard was read successfully but held no image — the
	// guest was never touched.
	NoImage
)

// Result is what PasteImage returns so a caller (CLI or TUI) can render a
// message without re-deriving what happened.
type Result struct {
	Status Status
	// VMName is the instance PasteImage was targeting, carried through so a
	// caller can format its message (e.g. "staged image on <VMName> — press S
	// then Ctrl-V") without threading the name back through separately.
	VMName string
	// GuestPath is the absolute guest-side path the image was written to. Only
	// meaningful when Status is Staged.
	GuestPath string
}

// clipDir / clipFile name the single-slot guest destination, relative to the
// guest home: <guest-home>/.sand/clip/latest.png. Shared by this package's one
// writer so the path is spelled exactly once.
const (
	clipDir  = "/.sand/clip"
	clipFile = "/latest.png"
)

// writeScript is run in the guest as `sh -c writeScript sand <dir> <file>`. It
// creates the single-slot directory, writes the image from stdin, and locks
// both down — one round trip, positional args so neither path ever needs
// shell-quoting. Mirrors internal/lima/sshhost.go's WriteFile.
const writeScript = `mkdir -p -- "$1"; chmod 700 "$1"; cat > "$2"; chmod 600 "$2"`

// guestWriter is the narrow surface PasteImage needs from a backend: writing
// bytes into a guest over one round trip, and resolving that guest's absolute
// home directory. provider.Provider satisfies this today (its Shell and
// GuestHome methods), so any caller already holding a provider.Provider can
// pass it in directly — this package does not import internal/provider itself,
// which keeps it free to be imported by both cmd/sand and internal/ui without
// risking a cycle through provider.
//
// It is narrowed (rather than depending on provider.Provider's full interface)
// so a unit test can satisfy it with a minimal fake wrapping a real
// *lima.Client over a fake Runner — the existing argv-recording seam — without
// having to stub provider.Provider's many other methods.
type guestWriter interface {
	// Shell runs argv inside the named instance, piping stdin to the guest and
	// streaming combined output to out. Mirrors provider.Provider.Shell /
	// lima.Client.Shell.
	Shell(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error
	// GuestHome returns v's guest login-user absolute home directory, or "" if
	// it could not be resolved. Mirrors provider.Provider.GuestHome.
	GuestHome(v vm.VM) string
}

// readClipboardImage is the injectable seam over clipboard.ReadImagePNG, so a
// unit test can simulate "no image" or "image present" without touching the
// real host clipboard — the same discipline internal/clipboard's own `run` var
// and internal/lima's newCmd/Runner seams use. Production leaves this at its
// default; only a test overwrites it.
var readClipboardImage = clipboard.ReadImagePNG

// PasteImage reads the host clipboard image and, if present, writes it to v's
// single-slot guest file <guest-home>/.sand/clip/latest.png in ONE guest round
// trip. When the clipboard holds no image (clipboard.ErrNoImage), it returns a
// NoImage Result WITHOUT making any guest call at all. Any other clipboard
// error, or a failure resolving the guest home or writing the file, is
// returned as an error.
//
// gw is typically a provider.Provider (its Shell + GuestHome methods satisfy
// guestWriter structurally); v is the target instance, resolved by the caller
// exactly as it already resolves the VM for Shell/Upload/Download.
func PasteImage(ctx context.Context, gw guestWriter, v vm.VM) (Result, error) {
	data, err := readClipboardImage()
	if err != nil {
		if errors.Is(err, clipboard.ErrNoImage) {
			return Result{Status: NoImage, VMName: v.Name}, nil
		}
		return Result{}, fmt.Errorf("paste: read clipboard: %w", err)
	}

	home := gw.GuestHome(v)
	if home == "" {
		return Result{}, fmt.Errorf("paste: could not resolve guest home for %q", v.Name)
	}
	dir := home + clipDir
	file := dir + clipFile

	if err := gw.Shell(ctx, v.Name, bytes.NewReader(data), io.Discard,
		"sh", "-c", writeScript, "sand", dir, file); err != nil {
		return Result{}, fmt.Errorf("paste: write image to guest %q: %w", v.Name, err)
	}

	return Result{Status: Staged, VMName: v.Name, GuestPath: file}, nil
}

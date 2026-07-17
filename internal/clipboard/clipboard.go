// Package clipboard reads an IMAGE off the clipboard of the machine sand runs
// on, and makes it structurally impossible to read text. This is the
// load-bearing security boundary for `sand paste-image`: ReadImagePNG has
// exactly one success shape (PNG bytes) and exactly one "nothing to paste"
// shape (ErrNoImage) — there is no code path anywhere in this package that
// returns clipboard TEXT.
//
// Each platform file (clipboard_darwin.go, clipboard_linux.go,
// clipboard_other.go) implements readImagePNG() behind a `//go:build` tag,
// mirroring the internal/ui/hostres_*.go seam. The contract is
// gate-then-fetch: a platform must confirm an advertised image/* type BEFORE
// it fetches any bytes, so a text-only clipboard is rejected at the gate with
// zero fetch of clipboard content.
package clipboard

import (
	"context"
	"errors"
	"os/exec"
)

// ErrNoImage means the clipboard was read successfully but holds no image
// (e.g. it holds text, or nothing at all).
var ErrNoImage = errors.New("clipboard: no image available")

// ErrUnsupported means the current platform has no clipboard-image probe
// (non-unix host).
var ErrUnsupported = errors.New("clipboard: unsupported platform")

// run is the injectable exec seam every platform implementation calls instead
// of exec.Command directly, so a test can substitute a fake that records which
// commands ran and replays canned output — the same discipline
// internal/lima/sshhost.go's newCmd field uses. Production wires it to
// exec.CommandContext(...).Output(); a test overwrites the var.
var run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// ReadImagePNG returns PNG bytes of the current clipboard image. It returns
// ErrNoImage when the clipboard holds no image, or ErrUnsupported on a
// platform with no clipboard-image probe. It NEVER returns clipboard text.
func ReadImagePNG() ([]byte, error) { return readImagePNG() }

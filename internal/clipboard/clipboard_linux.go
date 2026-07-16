//go:build linux

package clipboard

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// readImagePNG implements the Linux clipboard-image read: gate on an
// advertised image/png TARGETS/type-listing entry, then fetch — never the
// other way around. xclip (X11) is tried first; wl-paste (Wayland) is tried
// only when xclip's binary itself is absent — X11 first, Wayland strictly as
// a fallback. Neither binary present is ErrUnsupported; either binary
// present but advertising no image/png is ErrNoImage with ZERO fetch.
func readImagePNG() ([]byte, error) {
	ctx := context.Background()

	if targets, ok := xclipTargets(ctx); ok {
		return fetchIfImagePNG(ctx, targets, "xclip", []string{"-selection", "clipboard", "-t", "image/png", "-o"})
	}
	if targets, ok := wlPasteTargets(ctx); ok {
		return fetchIfImagePNG(ctx, targets, "wl-paste", []string{"--type", "image/png"})
	}
	return nil, ErrUnsupported
}

// xclipTargets runs xclip's TARGETS gate. ok is false only when the xclip
// binary itself could not be found (the caller should then try wl-paste);
// any other outcome (success, or a run error such as "no selection owner")
// is reported as ok=true with whatever bytes were produced, so the absence
// of an image type there is a definitive ErrNoImage rather than a fallback.
func xclipTargets(ctx context.Context) ([]byte, bool) {
	out, err := run(ctx, "xclip", "-selection", "clipboard", "-t", "TARGETS", "-o")
	if err != nil && errors.Is(err, exec.ErrNotFound) {
		return nil, false
	}
	return out, true
}

// wlPasteTargets runs wl-paste's type-listing gate (-l). ok is false only
// when the wl-paste binary itself could not be found.
func wlPasteTargets(ctx context.Context) ([]byte, bool) {
	out, err := run(ctx, "wl-paste", "-l")
	if err != nil && errors.Is(err, exec.ErrNotFound) {
		return nil, false
	}
	return out, true
}

// fetchIfImagePNG performs the FETCH half of gate-then-fetch: it only issues
// fetchArgv when the gate's targets listing advertised image/png specifically
// (a non-PNG image type, e.g. only image/jpeg, is treated as no image in v1 —
// no converter, no fetch). This is the one place a fetch command is ever
// invoked, and it is unreachable unless the gate already found image/png.
func fetchIfImagePNG(ctx context.Context, targets []byte, fetchName string, fetchArgs []string) ([]byte, error) {
	if !hasImagePNGTarget(targets) {
		return nil, ErrNoImage
	}
	out, err := run(ctx, fetchName, fetchArgs...)
	if err != nil {
		return nil, ErrNoImage
	}
	return out, nil
}

// hasImagePNGTarget reports whether a TARGETS/type listing (one MIME type per
// line) advertises image/png.
func hasImagePNGTarget(targets []byte) bool {
	for _, line := range strings.Split(string(targets), "\n") {
		if strings.TrimSpace(line) == "image/png" {
			return true
		}
	}
	return false
}

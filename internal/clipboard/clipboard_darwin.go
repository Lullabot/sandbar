//go:build darwin

package clipboard

import (
	"context"
	"fmt"
	"os"
)

// readImagePNG implements the macOS clipboard-image read. The osascript
// coercion `the clipboard as «class PNGf»` IS the gate: AppleScript refuses to
// coerce a non-image clipboard to PNG, so a non-zero osascript exit means "no
// image" and is mapped to ErrNoImage. There is no separate text read and no
// fallback to pbpaste — success is defined ONLY as osascript producing PNG
// bytes.
func readImagePNG() ([]byte, error) {
	tmp, err := os.CreateTemp("", "sand-clip-*.png")
	if err != nil {
		return nil, fmt.Errorf("clipboard: create temp file: %w", err)
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(path)

	// The coercion writes the clipboard's PNG representation to path; it fails
	// (non-zero exit) when the clipboard holds no coercible image, which is
	// exactly the gate this package requires — no image, no bytes.
	script := fmt.Sprintf(
		`set f to (open for access (POSIX file %q) with write permission)
set eof f to 0
write (the clipboard as «class PNGf») to f
close access f`, path)

	if _, err := run(context.Background(), "osascript", "-e", script); err != nil {
		return nil, ErrNoImage
	}

	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil, ErrNoImage
	}
	return data, nil
}

---
id: 1
group: "sand-paste-image"
dependencies: []
status: "pending"
created: 2026-07-17
model: "sonnet"
effort: "high"
skills:
  - go
  - clipboard
complexity_score: 7
complexity_notes: "Security-critical image-only guarantee across two real platforms plus a stub; needs an injectable exec seam so the gate-then-fetch contract is unit-testable without a real clipboard."
---
# Clipboard-Read Seam (image-only, build-tagged)

## Objective
Add a build-tagged Go seam that returns the current clipboard image as PNG bytes
from the machine sand runs on, or a `no image` sentinel — and that can NEVER
return clipboard text. macOS and Linux implement it; non-unix returns an
`unsupported` sentinel. This is the load-bearing security boundary for the whole
`sand paste-image` feature.

## Skills Required
- `go` — new package with `//go:build` platform files and an injectable exec seam.
- `clipboard` — macOS `osascript «class PNGf»` and Linux X11/Wayland selection
  semantics.

## Acceptance Criteria
- [ ] New package `internal/clipboard` exposes `ReadImagePNG() ([]byte, error)`
      returning `ErrNoImage` (a sentinel) when no image is on the clipboard and
      `ErrUnsupported` on non-unix platforms.
- [ ] `//go:build darwin`, `//go:build linux`, and `//go:build !darwin && !linux`
      files exist, mirroring `internal/ui/hostres_*.go`.
- [ ] The read is **gate-then-fetch**: it confirms an advertised `image/*` type
      before fetching bytes; with no image type it returns `ErrNoImage` and
      performs **zero** fetch of clipboard content.
- [ ] `go test ./internal/clipboard/...` passes, including a test that feeds a
      TARGETS/type listing containing only `text/plain` and asserts the result is
      `ErrNoImage` AND that no image/text-fetch command was ever invoked.
- [ ] `go build ./...` and `go vet ./...` succeed.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Mirror the platform-seam pattern in `internal/ui/hostres_darwin.go` /
  `hostres_linux.go` / `hostres_other.go` (build tags, no cross-file leakage).
- Introduce an injectable command-runner seam (a package var or function field,
  e.g. `var run = func(ctx, name string, args ...string) (stdout []byte, err error)`)
  so tests can substitute a fake and assert exactly which commands ran — the same
  discipline `internal/lima/sshhost.go` uses with its `newCmd` field.
- Output is always PNG (the guest shim serves a single PNG); normalize accordingly.

## Input Dependencies
None. This is a Phase-1 leaf.

## Output Artifacts
- `internal/clipboard` package with `ReadImagePNG`, `ErrNoImage`, `ErrUnsupported`.
- Consumed by task 3 (paste orchestration core).

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Package shape** (`internal/clipboard/clipboard.go`, no build tag):
```go
package clipboard

import "errors"

var ErrNoImage = errors.New("clipboard: no image available")
var ErrUnsupported = errors.New("clipboard: unsupported platform")

// run is the injectable exec seam. Production wires it to exec.Command in an
// init or leaves it as a default; tests overwrite it to record/replay.
var run = func(ctx context.Context, name string, args ...string) ([]byte, error) { /* exec.CommandContext(...).Output() */ }

// ReadImagePNG returns PNG bytes of the current clipboard image, or ErrNoImage.
func ReadImagePNG() ([]byte, error) { return readImagePNG() }
```
Each platform file implements `readImagePNG()`.

**`clipboard_darwin.go` (`//go:build darwin`):**
- The coercion is itself the gate. Write the PNG to a temp file via osascript,
  then read it back:
  `osascript -e 'set f to (open for access (POSIX file "<tmp>") with write permission)' -e 'set eof f to 0' -e 'write (the clipboard as «class PNGf») to f' -e 'close access f'`.
- If osascript exits non-zero (clipboard has no image / cannot coerce), map to
  `ErrNoImage`. Only return bytes on success. Never fall back to `pbpaste` or any
  text read.

**`clipboard_linux.go` (`//go:build linux`):**
- Gate: `xclip -selection clipboard -t TARGETS -o` and grep for a line matching
  `^image/`; if xclip is absent, try `wl-paste -l`. If neither lists an image
  type → `ErrNoImage`. If neither binary exists → `ErrUnsupported`.
- Fetch (only if the gate found an image type): `xclip -selection clipboard -t image/png -o`,
  falling back to `wl-paste --type image/png`. If the gate advertised an image
  type but not PNG specifically (e.g. only image/jpeg), treat as `ErrNoImage` in
  v1 (documented limitation — do NOT add an image converter).
- Never issue the fetch before the gate passes.

**`clipboard_other.go` (`//go:build !darwin && !linux`):**
- `func readImagePNG() ([]byte, error) { return nil, ErrUnsupported }`.

**Test (`clipboard_linux_test.go`):** overwrite `run` with a fake keyed on
(name,args). Case A: TARGETS returns `"text/plain\nUTF8_STRING"` → assert
`ReadImagePNG` returns `ErrNoImage` and the fake recorded NO call whose args
contain `image/png`. Case B: TARGETS returns `"image/png\ntext/plain"` and the
png fetch returns bytes → assert those bytes come back. This proves the
image-only property that a text clipboard yields zero fetched content.

**Security note for the executor:** the entire point of this task is that there
is no code path returning non-image clipboard content. Do not add a text branch
"for completeness".
</details>

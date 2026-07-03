---
id: 4
group: "path-entry"
dependencies: []
status: "completed"
created: 2026-07-03
skills:
  - go
  - bubble-tea
---
# Destination prompt: `textinput` with paste/drag-drop path normalization

## Objective
Build the destination-entry component: a `textinput` pre-filled with a sensible
default and editable, that accepts pasted/drag-dropped paths and normalizes their
shell-escaping. Because bubbletea surfaces terminal drag-and-drop as
bracketed-paste key input, a dropped path arrives as pasted text; the field
un-escapes backslash-escaped spaces, strips surrounding quotes, and removes an
optional `file://` prefix so a dropped path is immediately usable. The
normalization is a pure, unit-tested function.

## Skills Required
- **go**, **bubble-tea**: `bubbles/textinput`, `tea.KeyMsg` paste handling, string parsing, table tests.

## Acceptance Criteria
- [ ] A pure function `func NormalizePath(s string) string` (in `tui/internal/browse`) that: trims surrounding whitespace; removes a leading `file://` or `file://localhost` prefix; strips one layer of surrounding matching quotes (`"…"` or `'…'`); and un-escapes backslash-escaped characters (a `\` before a non-alphanumeric char is dropped, so `/a\ b\ c` → `/a b c`, `\(x\)` → `(x)`).
- [ ] A small `DestInput` component wrapping `textinput.Model`: `NewDestInput(prompt, def string) DestInput`, `Value() string`, `Update(msg) (DestInput, tea.Cmd)`, `View() string`. On a paste key message (`tea.KeyMsg` with `msg.Paste == true`), the pasted runes are passed through `NormalizePath` before being inserted/set, so a dropped path lands clean.
- [ ] Typing/pasting remains fully editable; the default is pre-filled and selected/replaceable. Drag-and-drop is treated as a convenience layered on the always-available typing path (no dependence on it).
- [ ] Unit test: a table exercising `NormalizePath` covers a backslash-space path (`/Users/me/My\ Files` → `/Users/me/My Files`), a double-quoted path (`"/a b/c"` → `/a b/c`), a single-quoted path, a `file:///Users/me/x` → `/Users/me/x`, and a plain path (unchanged). Optionally one component test asserting a `Paste` KeyMsg with an escaped path yields a clean `Value()`.
- [ ] `cd tui && gofmt -l . && go build ./... && go vet ./... && go test ./...` all pass.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Use `github.com/charmbracelet/bubbles/textinput` (already a dependency).
- `DestInput` must be copy-safe (embedded in the root model passed by value): hold only the `textinput.Model` and small strings.
- Percent-decoding of `file://` URLs (`%20` → space) is **out of scope** for v1 — only strip the scheme prefix. Keep the source path as discrete process arguments downstream (never a shell string); `NormalizePath` returns plain text only.

## Input Dependencies
None. Self-contained; adds files to the `tui/internal/browse` package (which task
2/3 also populate, but with different files — no ordering dependency).

## Output Artifacts
- `tui/internal/browse/dest.go` — `NormalizePath` + `DestInput`.
- `tui/internal/browse/dest_test.go` — `NormalizePath` table test (+ optional paste test).

## Implementation Notes

<details>
<summary>Detailed implementation steps</summary>

1. `NormalizePath`:
   ```go
   func NormalizePath(s string) string {
       s = strings.TrimSpace(s)
       s = strings.TrimPrefix(s, "file://localhost")
       s = strings.TrimPrefix(s, "file://")
       // strip one layer of surrounding matching quotes
       if len(s) >= 2 {
           if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
               s = s[1 : len(s)-1]
           }
       }
       // drop a backslash that escapes a non-alphanumeric char (spaces, parens, etc.)
       var b strings.Builder
       for i := 0; i < len(s); i++ {
           if s[i] == '\\' && i+1 < len(s) {
               nxt := s[i+1]
               isAlnum := (nxt >= 'a' && nxt <= 'z') || (nxt >= 'A' && nxt <= 'Z') || (nxt >= '0' && nxt <= '9')
               if !isAlnum {
                   continue // skip the backslash; keep the escaped char next iteration
               }
           }
           b.WriteByte(s[i])
       }
       return strings.TrimSpace(b.String())
   }
   ```
   (Byte iteration is fine here — paths are UTF-8 and the escapes we drop are ASCII.)

2. `DestInput`:
   ```go
   type DestInput struct{ ti textinput.Model }
   func NewDestInput(prompt, def string) DestInput {
       ti := textinput.New()
       ti.Prompt = prompt
       ti.SetValue(def)
       ti.CursorEnd()
       ti.Focus()
       return DestInput{ti: ti}
   }
   func (d DestInput) Value() string { return d.ti.Value() }
   func (d DestInput) View() string  { return d.ti.View() }
   func (d DestInput) Update(msg tea.Msg) (DestInput, tea.Cmd) {
       if k, ok := msg.(tea.KeyMsg); ok && k.Paste {
           d.ti.SetValue(NormalizePath(string(k.Runes)))
           d.ti.CursorEnd()
           return d, nil
       }
       var cmd tea.Cmd
       d.ti, cmd = d.ti.Update(msg)
       return d, cmd
   }
   ```
   (In bubbletea v1, a bracketed paste arrives as a `tea.KeyMsg` with `Paste==true`
   and the pasted text in `Runes`. If the installed version exposes a distinct
   `tea.PasteMsg` instead, handle that type the same way — normalize and set.)

3. Tests: a plain table over `NormalizePath` inputs/outputs (see acceptance
   criteria). Keep it a handful of rows; this is the highest-value unit here.
</details>

### Meaningful Test Strategy Guidelines
Your critical mantra for test generation is: "write a few tests, mostly integration".
- **DO** table-test `NormalizePath` (the custom un-escaping/quote/scheme logic).
- **DON'T** test `textinput` keystroke handling itself — only that a paste is normalized before it lands.

---
id: 2
group: "review-range"
dependencies: [1]
status: "pending"
created: 2026-09-29
models:
  anthropic: "claude-sonnet-5"
  openai: "gpt-6-sol"
effort: "medium"
skills:
  - go
  - cli
---
# Accept `-- <diff args>` in `sand land --review`

## Objective
`sand land NAME PATH --review -- <diff args>` passes the arguments to the session as `DiffArgs`.

## Skills Required
`go` and CLI flag handling.

## Acceptance Criteria
- [ ] Args are split at the first `--` before `reorderLandFlags`; everything before goes through the existing parser unchanged, everything after becomes `DiffArgs`.
- [ ] `--` arguments without `--review` fail with a clear error; a trailing empty `--` means no range.
- [ ] Without `--`, parsing and behaviour are identical to today.
- [ ] `landUsage` shows the new synopsis and an example (`HEAD~2`), and mentions `HEAD~2...HEAD`, `--staged`, and that a saved review is still carried in.
- [ ] Verify: `go test ./cmd/sand` passes with new table tests (split at `--`, `--` without `--review`, trailing `--`, no `--`), and `go run ./cmd/sand land --help` prints the new text.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
Edit `cmd/sand/land.go` (`runLand`, `landUsage`); the session is built in the `case *reviewFlag:` branch. Keep the `--clean` behaviour and error style.

## Input Dependencies
`Session.DiffArgs` from task 1.

## Output Artifacts
Updated `sand land` command and help text.

## Implementation Notes
<details>

- Split first, then call `fs.Parse(reorderLandFlags(before))`.
- Follow the existing `--clean only applies to --review` refusal for the new error.
- Test philosophy: write a few meaningful tests, mostly integration; do not test the `flag` package. Combine related parsing scenarios into one table test.
</details>

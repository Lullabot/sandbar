---
id: 1
group: "review-range"
dependencies: []
status: "pending"
created: 2026-09-29
models:
  anthropic: "claude-sonnet-5"
  openai: "gpt-6-sol"
effort: "medium"
skills:
  - go
  - shell-scripting
---
# Add DiffArgs to landreview.Session

## Objective
Let `landreview.Session` take caller-supplied diff arguments and pass them to `self-review-serve` in place of the resolved base, leaving the no-range path unchanged.

## Skills Required
`go` for the session change and tests; `shell-scripting` for the guest resume probe.

## Acceptance Criteria
- [ ] `Session` has an optional `DiffArgs []string`; nil/empty behaves exactly as today (existing `internal/landreview` tests pass unedited).
- [ ] With `DiffArgs` set, `Run` does not run the base-resolution script, does not apply `maxDiffFiles`, and appends each element as its own argv element after any `--resume-from`.
- [ ] The resume check runs on its own in that case; a saved `review.xml` is carried in, and `Clean` removes it first so no `--resume-from` is added.
- [ ] The progress line shows the range (for example `range: HEAD~2`).
- [ ] Verify: `go test ./internal/landreview` passes, including new tests for: no range (unchanged argv), `HEAD~2`, `HEAD~2...HEAD`, `--staged`, range + saved review, range + `Clean`, and an argument such as `x; touch /tmp/y` arriving as one inert element.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Share the `sandresume` snippet between `diffBaseScript` and a new fixed-literal resume-only script via one constant, so the `output-file` rules cannot drift. `diffBaseScript`'s output must not change.
- The resume script takes the checkout path as a positional argument and prints only a flag; Go composes the `--resume-from` path.
- Arguments are user input, not guest data: no allow-list, but drop empty elements. Never build a shell string from them.

## Input Dependencies
None.

## Output Artifacts
`Session.DiffArgs`, used by the CLI and TUI tasks.

## Implementation Notes
<details>

- Edit `internal/landreview/landreview.go`: `Run` (around the `diffBase` call and argv assembly), `diffBaseScript`, `parseDiffBase`, `describeBase`.
- Use `internal/providerfake.Provider` to capture the `ShellOut` and serve argv, following existing tests in `landreview_test.go`.
- No `t.Parallel()`. Keep comments about the why, and reference no plan documents.
- Test philosophy: write a few meaningful tests, mostly integration. Test this code's argv and resume logic, not the framework or the VCS tool; combine related scenarios into one table test.
</details>

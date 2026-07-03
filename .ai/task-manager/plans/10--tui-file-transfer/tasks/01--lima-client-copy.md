---
id: 1
group: "transport"
dependencies: []
status: "pending"
created: 2026-07-03
skills:
  - go
---
# Add a streaming `Copy` method to `lima.Client`

## Objective
Give the TUI one testable way to run `limactl copy`, consistent with every other
lima call. Add a `Copy` method to `lima.Client` that builds `copy -v
--backend=auto [-r] <src> <dst>` and executes it through `Runner.Stream`, so
progress streams live and a cancelled context kills the subprocess. Also add a
small helper for forming guest endpoints (`<vm>:<path>`). Unit-test argv
construction against the existing fake `Runner`.

## Skills Required
- **go**: methods on an existing type, `context`, `io.Writer`, table-driven argv tests.

## Acceptance Criteria
- [ ] `lima.Client` gains `Copy(ctx context.Context, out io.Writer, recursive bool, src, dst string) error` that streams `limactl copy -v --backend=auto [-r] <src> <dst>` via `Runner.Stream` (reusing the existing `runStream` helper, so a cancelled `ctx` kills the process and errors are wrapped like the other `*Streaming` methods).
- [ ] `-r` is appended **iff** `recursive` is true; `-v` and `--backend=auto` are always present.
- [ ] An exported helper `func GuestPath(instance, path string) string` returns `instance + ":" + path`; host paths are passed through unchanged by callers.
- [ ] Unit tests (in `tui/internal/lima/client_test.go`, extending the existing `TestMethodArgv` table or a new test) assert: a host→guest non-recursive copy builds `["copy","-v","--backend=auto","<src>","<vm>:<dst>"]`, and a guest→host recursive copy builds `["copy","-v","--backend=auto","-r","<vm>:<src>","<dst>"]`, using the existing `fakeRunner` (which records `Stream` calls into `f.calls`).
- [ ] `cd tui && gofmt -l . && go build ./... && go vet ./... && go test ./...` all pass.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Stdlib only (`context`, `io`, `fmt`, `strings` as already imported in `client.go`).
- Must go through the `Runner` seam — never spawn a real `limactl`. The design contract is **destination-is-a-directory**: callers always pass a directory as `dst` and the source is placed inside it, which makes the result identical across the rsync and scp backends. This method does not enforce that; it only constructs args faithfully.

## Input Dependencies
None. Builds directly on the existing `Runner`/`Client` in `tui/internal/lima`.

## Output Artifacts
- `tui/internal/lima/client.go` — new `Copy` method + `GuestPath` helper.
- `tui/internal/lima/client_test.go` — argv assertions for `Copy`.

## Implementation Notes

<details>
<summary>Detailed implementation steps</summary>

1. In `tui/internal/lima/client.go`, add near the other `*Streaming` methods:
   ```go
   // Copy wraps `limactl copy`, streaming its verbose output to out. The auto
   // backend prefers rsync (resumable) and falls back to scp; -v streams
   // progress; -r is used for directory sources. Guest endpoints are formed with
   // GuestPath ("<vm>:/path"); host endpoints are plain paths. The caller's
   // contract is that dst is always a DIRECTORY and src is placed inside it, so
   // the result is identical whether rsync or scp runs. Goes through Runner.Stream
   // so a cancelled ctx kills the transfer, exactly like the *Streaming methods.
   func (c *Client) Copy(ctx context.Context, out io.Writer, recursive bool, src, dst string) error {
       args := []string{"copy", "-v", "--backend=auto"}
       if recursive {
           args = append(args, "-r")
       }
       args = append(args, src, dst)
       return c.runStream(ctx, out, args...)
   }

   // GuestPath forms a limactl guest endpoint ("<instance>:<path>") for copy/shell.
   func GuestPath(instance, path string) string { return instance + ":" + path }
   ```
   `runStream` already wraps `c.r.Stream(ctx, nil, out, args...)` and folds failures into a `limactl <args>: %w` error — reuse it.

2. Tests in `client_test.go`: the existing `fakeRunner.Stream` appends `args` to
   `f.calls`, so the same assertion style as `TestMethodArgv` works. Add cases
   (either into that table or a dedicated `TestCopyArgv`):
   ```go
   {"copy-upload", func(c *Client) {
       _ = c.Copy(context.Background(), io.Discard, false, "/host/file.txt", GuestPath("vm1", "/home/u/dir"))
   }, []string{"copy", "-v", "--backend=auto", "/host/file.txt", "vm1:/home/u/dir"}},
   {"copy-download-recursive", func(c *Client) {
       _ = c.Copy(context.Background(), io.Discard, true, GuestPath("vm1", "/home/u/src"), "/host/dst")
   }, []string{"copy", "-v", "--backend=auto", "-r", "vm1:/home/u/src", "/host/dst"}},
   ```
   Keep it to these two argv assertions — do not try to exercise a real copy here
   (that is the gated e2e task).

3. Note on module path: the package is imported today as
   `github.com/deviantintegral/claude-code-ansible/tui/internal/lima`. Do not
   hard-code that string anywhere new; if plans 06/07 land first the module path
   changes and the imports move with it.
</details>

### Meaningful Test Strategy Guidelines
Your critical mantra for test generation is: "write a few tests, mostly integration".
- **DO** test that `Copy` builds the correct argv (recursive flag, `-v`, `--backend=auto`, guest prefix ordering) via the fake `Runner`.
- **DON'T** test `limactl`/`os/exec` itself or run a real copy — the gated `limae2e` task covers the real round-trip.

---
id: 2
group: "core"
dependencies: [1]
status: "completed"
created: 2026-06-26
skills:
  - golang
---
# Implement the `lima` client and `Runner` abstraction

## Objective
Create the single boundary between the app and the `limactl` binary: a `Runner`
interface (real + fake), and a `Client` exposing the lifecycle operations the TUI
needs (List, Status, Start, Stop, Delete, Clone, Create, Shell), with the
`limactl list` JSON parser unit-tested against a fixture.

## Skills Required
- **golang**: `os/exec`, interfaces for testability, JSON parsing, table tests.

## Acceptance Criteria
- [ ] `Runner` interface abstracts subprocess execution; `execRunner` runs real `limactl`, `fakeRunner` is used in tests.
- [ ] `Client` methods: `List() ([]vm.VM, error)`, `Status(name)`, `Start(name)`, `Stop(name)`, `Delete(name, force)`, `Clone(base, name)`, `Create(name, overlayPath)`, `Shell(ctx, name, stdin io.Reader, out io.Writer, argv ...string)`.
- [ ] `List()` parses `limactl list --format json` (one JSON object per line) into `[]vm.VM`.
- [ ] A `Preflight()` helper checks `limactl` is present and `limactl clone --help` succeeds (mirrors the script's guards), returning a clear error otherwise.
- [ ] Unit tests cover the list parser (fixture-based) and verify each method invokes the expected `limactl` argv via `fakeRunner`.
- [ ] `cd tui && go build ./... && go vet ./... && go test ./...` pass.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Stdlib only (`os/exec`, `encoding/json`, `context`, `io`, `bufio`).
- `limactl` is NOT installed in this environment — tests MUST use `fakeRunner`, never spawn real `limactl`.

## Input Dependencies
- Task 01: the `vm` package (`vm.VM`).

## Output Artifacts
- `tui/internal/lima/runner.go` — `Runner` interface + `execRunner`
- `tui/internal/lima/client.go` — `Client` with all methods + `Preflight`
- `tui/internal/lima/client_test.go` — list-parser + argv tests using `fakeRunner`
- `tui/internal/lima/testdata/list.json` — captured `limactl list --format json` fixture

## Implementation Notes

<details>
<summary>Detailed implementation steps</summary>

1. `runner.go` — define the abstraction. Use streaming for `Shell` (needs live
   output) and buffered for the rest:
   ```go
   package lima

   import (
       "context"
       "io"
       "os/exec"
   )

   // Runner executes limactl. Abstracted so tests never spawn a real binary.
   type Runner interface {
       // Output runs `limactl args...`, returns combined stdout, and an error.
       Output(ctx context.Context, args ...string) ([]byte, error)
       // Stream runs `limactl args...`, piping stdin in and combined output to out.
       Stream(ctx context.Context, stdin io.Reader, out io.Writer, args ...string) error
   }

   type execRunner struct{ bin string }

   func NewExecRunner() Runner { return &execRunner{bin: "limactl"} }

   func (r *execRunner) Output(ctx context.Context, args ...string) ([]byte, error) {
       cmd := exec.CommandContext(ctx, r.bin, args...)
       return cmd.CombinedOutput()
   }

   func (r *execRunner) Stream(ctx context.Context, stdin io.Reader, out io.Writer, args ...string) error {
       cmd := exec.CommandContext(ctx, r.bin, args...)
       cmd.Stdin = stdin
       cmd.Stdout = out
       cmd.Stderr = out
       return cmd.Run()
   }
   ```

2. `client.go` — wrap the runner:
   ```go
   type Client struct{ r Runner }
   func New(r Runner) *Client { return &Client{r: r} }
   ```
   - `List`: run `--format json` (NOT the Go-template `--format '{{.Status}}'`,
     which is harder to parse). `limactl list --json` historically emits one JSON
     object per line, so decode with a `json.Decoder` in a loop over the byte
     stream. Map fields: `.name`→Name, `.status`→Status, `.cpus`→CPUs,
     `.memory`→Memory (may be bytes; store raw string or humanize), `.disk`→Disk,
     `.dir`→Dir, `.arch`→Arch. Be tolerant of missing fields.
   - `Status(name)`: `limactl list <name> --format '{{.Status}}'`, trim space.
   - `Start(name)`: `limactl start <name>` (use `Stream` so the TUI can show it).
   - `Stop(name)`: `limactl stop <name>`.
   - `Delete(name, force)`: `limactl delete <name>` plus `-f` when force.
   - `Clone(base, name)`: `limactl clone <base> <name>`.
   - `Create(name, overlayPath)`: `limactl start --name <name> --tty=false <overlayPath>`.
   - `Shell(ctx, name, stdin, out, argv...)`: `Stream(ctx, stdin, out, append([]string{"shell", name}, argv...)...)`.
   - `Preflight()`: `Output(ctx, "--version")`; if the binary is missing
     (`exec.ErrNotFound`/`errors.Is`), return a friendly "limactl not found — install
     Lima" error. Then `Output(ctx, "clone", "--help")`; non-nil error → "your Lima
     is too old: 'limactl clone' is required".

3. `fakeRunner` (in `client_test.go` or a `_test.go` helper): records the args of
   each call and returns canned bytes/errors keyed by the first arg. Example:
   ```go
   type fakeRunner struct {
       calls   [][]string
       outputs map[string][]byte // keyed by args[0]
       err     error
   }
   func (f *fakeRunner) Output(_ context.Context, args ...string) ([]byte, error) {
       f.calls = append(f.calls, args)
       return f.outputs[args[0]], f.err
   }
   func (f *fakeRunner) Stream(_ context.Context, _ io.Reader, out io.Writer, args ...string) error {
       f.calls = append(f.calls, args)
       return f.err
   }
   ```

4. `testdata/list.json` — a 2-instance fixture (one Running, one Stopped) in the
   one-object-per-line shape. Test that `List()` returns 2 `vm.VM` with the right
   Name/Status/CPUs. If the installed Lima's exact JSON keys are uncertain, define
   the struct tags to match the documented keys and keep the fixture consistent with
   them; the parser is what's under test, not Lima itself.

5. Argv tests: call `Start("x")`, assert `fake.calls` contains `["start","x"]`; etc.

Keep tests few and integration-leaning: one list-parse test + one
argv-per-method table test is enough.
</details>

### Meaningful Test Strategy Guidelines
Your critical mantra for test generation is: "write a few tests, mostly integration".
- **DO** test the `limactl list` JSON parser (custom transformation) and that methods build the correct argv.
- **DON'T** test `os/exec` itself or try to run real `limactl`. Use `fakeRunner`.

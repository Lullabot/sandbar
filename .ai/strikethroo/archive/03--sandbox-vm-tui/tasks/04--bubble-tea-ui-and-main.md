---
id: 4
group: "ui"
dependencies: [2, 3]
status: "completed"
created: 2026-06-26
skills:
  - golang
  - bubbletea
---
# Build the Bubble Tea TUI and the `claude-vm` entry point

## Objective
Implement the interactive CRUD surface with Charm's Bubble Tea: a VM list (Read),
a detail view (Read), a create form (Create) that runs the provisioner async with a
streamed progress pane, lifecycle key actions (Start/Stop/Restart — Update), and a
delete/recreate confirmation (Delete). Wire it all into `cmd/claude-vm/main.go`.

## Skills Required
- **golang**: application wiring, goroutines/channels, `os/exec` boundary via existing packages.
- **bubbletea**: Elm-architecture model/update/view, `tea.Cmd` async, bubbles components, lipgloss styling.

## Acceptance Criteria
- [ ] List view (bubbles `table`) shows all VMs with status, refreshed from `lima.Client.List()`; `enter` opens a detail view.
- [ ] Create form (bubbles `textinput` fields) seeded with `vm.DefaultCreateConfig()`; the token field is masked; submit validates via `CreateConfig.Validate()`.
- [ ] Submitting the form runs `provision.CreateVM` as a `tea.Cmd` whose streamed output appears live in a scrollable progress pane (bubbles `viewport` + `spinner`) without blocking input.
- [ ] Key actions on the selected VM: `s` start, `x` stop, `r` restart (stop+start), `d` delete/recreate behind a confirm prompt; each runs as a `tea.Cmd` and refreshes the list on completion.
- [ ] On startup, `lima.Preflight()` runs; a missing/old `limactl` shows a clear message instead of crashing.
- [ ] A `help` bar (bubbles `help`) lists the keybindings; `q`/`ctrl+c` quits.
- [ ] `cmd/claude-vm/main.go` builds the `Client`/`Provisioner` and runs the program; `cd tui && go build ./... && go vet ./... && go test ./...` pass.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- `github.com/charmbracelet/bubbletea`, `github.com/charmbracelet/bubbles` (table, textinput, viewport, spinner, help, key), `github.com/charmbracelet/lipgloss`. Add via `cd tui && go get`.
- Consumes `lima.Client` (task 02) and `provision.Provisioner` (task 03).
- The long-running create MUST stream: use a channel/`io.Writer` adapter feeding `tea.Msg`s; never call the provisioner synchronously in `Update`.

## Input Dependencies
- Task 02: `lima.Client`, `Preflight`.
- Task 03: `provision.Provisioner` (`CreateVM`, `Recreate`, `BuildBase`).

## Output Artifacts
- `tui/internal/ui/model.go` — root model, view-switching, key handling
- `tui/internal/ui/list.go`, `detail.go`, `form.go`, `progress.go` — the four views (split as convenient)
- `tui/internal/ui/commands.go` — `tea.Cmd`s wrapping lima/provision calls + msg types
- `tui/internal/ui/model_test.go` — a light test of a key state transition (e.g. list → confirm-delete)
- `tui/cmd/claude-vm/main.go` — entry point

## Implementation Notes

<details>
<summary>Detailed implementation steps</summary>

1. **Model & views**. A root `model` holds a `view` enum
   (`viewList|viewDetail|viewForm|viewProgress`), the `*lima.Client`, the
   `*provision.Provisioner`, and sub-models (table, form inputs, viewport, spinner,
   help). `Update` dispatches by current view; `View` renders the active one with
   `lipgloss` framing.

2. **List (Read)**. On `Init`/refresh, run a `listCmd` `tea.Cmd` that calls
   `Client.List()` and returns a `vmsLoadedMsg`. Populate the `table`. `enter` →
   `viewDetail` for the highlighted row; `n` → `viewForm`.

3. **Create form (Create)**. A slice of `textinput.Model` for Name, Hostname, User,
   GitName, GitEmail, CPUs, Memory, Disk, DockerProxyHost, CloneURL, CloneToken
   (token `EchoMode = textinput.EchoPassword`). `tab`/`shift+tab` move focus; seed
   from `vm.DefaultCreateConfig()` and host git config (`git config user.name/email`
   via `os/exec`, best-effort). On submit, parse CPUs with `vm.ParseCPUs`, build a
   `CreateConfig`, call `Validate()`, and on success switch to `viewProgress` and
   fire the create command.

4. **Progress pane + async streaming (the critical pattern)**. Do NOT block
   `Update`. Pattern:
   ```go
   type provisionOutputMsg string
   type provisionDoneMsg struct{ err error }

   func (m model) createCmd(cfg vm.CreateConfig) tea.Cmd {
       // ch is created and stored on the model before returning the batch.
       return func() tea.Msg {
           pr, pw := io.Pipe()
           go func() {
               err := m.prov.CreateVM(context.Background(), cfg, pw)
               pw.CloseWithError(err)
           }()
           // hand the reader to a reader-cmd; or use a channel the model drains.
           return provisionStartedMsg{r: pr}
       }
   }
   ```
   Practical approach: store an `io.PipeReader` (or a buffered channel of strings) on
   the model; a small `readNextCmd` reads one chunk/line and returns
   `provisionOutputMsg`, re-issuing itself until EOF, then `provisionDoneMsg`. Append
   each chunk to the `viewport` content and keep the `spinner` ticking. On
   `provisionDoneMsg`, show success/error and offer to return to the list (which
   refreshes).

5. **Lifecycle (Update)**. `s`/`x`/`r` build a `tea.Cmd` calling
   `Client.Start/Stop` (restart = stop then start), returning an
   `actionDoneMsg{err}` that triggers a list refresh. Use `Stream` output into the
   progress pane for visibility, or run quietly and just refresh — keep it simple.

6. **Delete/Recreate (Delete)**. `d` opens a confirm overlay (a small bool state).
   Confirm-delete → `Client.Delete(name, true)`; offer recreate →
   `prov.Recreate(...)` in the progress view. Always confirm before destroying.

7. **Preflight & main**. `cmd/claude-vm/main.go`:
   ```go
   func main() {
       cli := lima.New(lima.NewExecRunner())
       if err := cli.Preflight(); err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
       dir, err := provision.LocatePlaybook()
       if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
       prov := &provision.Provisioner{Lima: cli, PlaybookDir: dir}
       if _, err := tea.NewProgram(ui.New(cli, prov), tea.WithAltScreen()).Run(); err != nil {
           fmt.Fprintln(os.Stderr, err); os.Exit(1)
       }
   }
   ```

8. **Test (light)**. In `model_test.go`, construct the model with a fake/nil client,
   send a `d` `tea.KeyMsg`, and assert the model enters the confirm-delete state.
   Don't test bubbletea internals or rendering. One or two transitions is enough.
</details>

### Meaningful Test Strategy Guidelines
Your critical mantra for test generation is: "write a few tests, mostly integration".
- **DO** test one or two custom state transitions (e.g. submit-validation failure keeps the form; `d` enters confirm).
- **DON'T** test the Bubble Tea framework, terminal rendering, or component internals (already tested upstream). Keep UI tests minimal.

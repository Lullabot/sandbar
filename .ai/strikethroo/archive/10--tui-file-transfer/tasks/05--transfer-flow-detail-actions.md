---
id: 5
group: "ui-flow"
dependencies: [1, 3, 4]
status: "completed"
created: 2026-07-03
skills:
  - go
  - bubble-tea
complexity_score: 5
complexity_notes: "Integration glue: two new view states + Upload/Download detail actions + running-VM guard + start-dir derivation + launching Client.Copy through the reused pipe→viewport progress plumbing, with model-transition tests. Bounded because the browser, dest prompt, and Copy already exist."
---
# Wire Upload/Download actions and the sequential transfer flow into the TUI

## Objective
Wire the pieces into the existing single-active-view state machine without
disrupting current screens. Add **Upload** and **Download** actions to the VM
detail view; add a browse view and a destination-prompt view; and run the
transfer through the existing pipe→viewport streaming so progress and
cancellation come for free. The flow is sequential single-pane: browse source →
confirm destination directory → streamed copy. Both directions require a running
VM and guard with a clear message otherwise.

## Skills Required
- **go**, **bubble-tea**: Bubble Tea model/update/view state machine, key routing, `tea.Cmd` orchestration, model tests.

## Acceptance Criteria
- [ ] Two new view states (`viewBrowse`, `viewDest`) are added to the `view` enum in `tui/internal/ui/model.go`, routed in `Update` (key dispatch), `View`, and `forward` (non-key messages, incl. the browser's async `dirLoadedMsg` and the dest input's blink), so existing screens are unaffected.
- [ ] The detail view (`detail.go`) gains **Upload** (`u`) and **Download** (`d`) actions, added to `keys.go` and the detail view's help bar; both **guard on the VM being Running** (mirroring the list's shell guard) and, when not running, set a clear status shown on the detail view instead of proceeding.
- [ ] **Upload**: opens the browser with a **host** `DirLister` (`browse.NewLocalLister()`) starting at `os.Getwd()` (falling back to the home dir); after a source is selected, opens the destination prompt pre-filled with a **guest** directory default; confirming runs `Client.Copy` with `src`=host path, `dst`=`lima.GuestPath(vm, destDir)`.
- [ ] **Download**: opens the browser with a **guest** `DirLister` (`browse.NewGuestLister(m.cli, vm)`) starting at the guest checkout dir (`<home>/<host>/<org>/<repo>` derived from the VM's recorded `CloneURL`, falling back to the guest home); after a source is selected, opens the destination prompt pre-filled with the **host** working directory; confirming runs `Client.Copy` with `src`=`lima.GuestPath(vm, sourcePath)`, `dst`=host destDir.
- [ ] In both directions the **destination is always a directory** and the selected file/dir is placed inside it (no full-target-path typing); a directory source sets `recursive=true` on `Copy`.
- [ ] Confirming the destination switches to the **reused** `viewProgress`, streaming `limactl copy -v` output via the same `io.Pipe`→`viewport`→`readNextCmd` plumbing as provisioning; the spinner animates; `ctrl+c` cancels (killing the copy) and reports the cancel; success/failure is reported like create/recreate. The transfer must NOT be recorded in the managed registry (set the provisioning config's Name empty so the existing `provisionDoneMsg` handler skips `reg.Add`).
- [ ] `model_test.go` covers: Upload from a Running VM opens `viewBrowse` with a host lister; the same action on a Stopped VM stays put and sets the running-required status; and confirming a destination transitions to `viewProgress` (state transition only — do not run a real copy; the fake lima client / a stubbed run is sufficient).
- [ ] `cd tui && gofmt -l . && go build ./... && go vet ./... && go test ./...` all pass.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Reuse the existing streaming machinery in `progress.go` (`readPipe`, `readNextCmd`, `provisionOutputMsg`, `provisionDoneMsg`, the spinner, `setOutput`) rather than inventing new plumbing. Add a `beginTransfer` helper that mirrors `beginProvision` but closes over `Client.Copy` and clears `provCfg` so nothing is recorded as managed.
- Guest listing and the copy both happen in `tea.Cmd`s (the browser already loads asynchronously; the transfer runs in the goroutine feeding the pipe) so `Update` never blocks on SSH.
- The root `model` is passed by value — new fields (embedded `browse.Browser`, `browse.DestInput`, and small strings like `transferVM`, `transferSrc`, `transferRecursive bool`, and a `transferDir` up/down flag) must be copy-safe (no `strings.Builder`, no pointers beyond the existing `reader`).

## Input Dependencies
- Task 1: `lima.Client.Copy` and `lima.GuestPath`.
- Task 3: `browse.Browser`, `browse.NewLocalLister`, `browse.NewGuestLister`, its `Open`/`Update`/`Selected`.
- Task 4: `browse.DestInput` (and, indirectly, `NormalizePath`).

## Output Artifacts
- `tui/internal/ui/model.go` — new view states, model fields, Update/View/forward routing.
- `tui/internal/ui/detail.go` — Upload/Download actions + guard + status line.
- `tui/internal/ui/keys.go` — `Upload`/`Download` bindings and detail help.
- `tui/internal/ui/transfer.go` (new) — browse/dest update handlers, start-dir derivation, `beginTransfer`, copy `tea.Cmd`.
- `tui/internal/ui/model_test.go` — transition tests.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**1. View states — `model.go`:** extend the enum:
```go
const (
    viewList view = iota
    viewDetail
    viewForm
    viewProgress
    viewBrowse
    viewDest
)
```
Add fields to `model`:
```go
browser          browse.Browser
dest             browse.DestInput
transferVM       string
transferUpload   bool   // true = upload (host→guest), false = download
transferSrc      string // chosen source (absolute; host path or guest path w/o vm: prefix)
transferRecursive bool  // source is a directory
```

**2. Keys — `keys.go`:** add to `keyMap` and `defaultKeys()`:
```go
Upload:   key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "upload")),
Download: key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "download")),
```
Add them to the `viewDetail` branch of `viewHelp()` (alongside Back/Quit). `d` is
free on the detail view (delete lives only on the list's confirm overlay).

**3. Detail actions — `detail.go` `updateDetail`:** before the existing
Back/Quit cases, handle Upload/Download:
```go
case key.Matches(msg, m.keys.Upload):
    return m.startTransfer(true)   // host→guest
case key.Matches(msg, m.keys.Download):
    return m.startTransfer(false)  // guest→host
```
`startTransfer(upload bool)` (in `transfer.go`): guard `if m.detail.Status != "Running" { m.status = m.detail.Name + " must be running to transfer files (press s to start it)"; return m, nil }`. Render `m.status` in `detailView()` (add a status line like `listView` does). Then:
- set `m.transferVM = m.detail.Name`, `m.transferUpload = upload`;
- build the lister + start dir per direction (see step 5);
- `m.browser = browse.NewBrowser(lister, title)`, `m.view = viewBrowse`;
- return `m, m.browser.Open(startDir)`.

**4. Update routing — `model.go`:**
- In the `tea.KeyMsg` switch add `case viewBrowse: return m.updateBrowse(msg)` and `case viewDest: return m.updateDest(msg)`.
- In `forward` (non-key msgs), add `case viewBrowse: m.browser, cmd = m.browser.Update(msg); return m, cmd` (this delivers the browser's own `dirLoadedMsg`) and `case viewDest: m.dest, cmd = m.dest.Update(msg); return m, cmd`.
- In `View`, add `case viewBrowse: return m.browser.View()` and `case viewDest: return m.destView()`.
- Note: the browser's `dirLoadedMsg` is an internal type of the `browse` package; deliver it by forwarding **all** unhandled messages while `m.view==viewBrowse` to `m.browser.Update` (as `forward` does). Keep key messages going through `updateBrowse`, which itself calls `m.browser.Update(msg)` and then checks `m.browser.Selected()`.

**5. `updateBrowse` + start dirs — `transfer.go`:**
```go
func (m model) updateBrowse(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
    if key.Matches(msg, m.keys.Back) && m.browser.NotFiltering() { // esc backs out to detail
        m.view = viewDetail
        return m, nil
    }
    var cmd tea.Cmd
    m.browser, cmd = m.browser.Update(msg)
    if p, isDir, ok := m.browser.Selected(); ok {
        m.transferSrc = p
        m.transferRecursive = isDir
        // open the destination prompt with a per-direction default dir
        def := m.hostWorkDir()
        if m.transferUpload {
            def = m.guestDefaultDir() // upload lands in the guest
        }
        m.dest = browse.NewDestInput("Destination dir: ", def)
        m.view = viewDest
        return m, textinput.Blink
    }
    return m, cmd
}
```
Start-dir helpers:
- `hostWorkDir()`: `os.Getwd()` then fall back to `os.UserHomeDir()`.
- `guestDefaultDir()`: `home := "/home/" + userOf(m.transferVM)` (guest user from the VM's recorded config via `m.reg.Config`, defaulting to `hostUser()`/`"debian"` if unknown — Debian/Ubuntu convention). If the VM's recorded `CloneURL` is non-empty, append the org-relative checkout dir. The checkout layout `<host>/<org>/<repo>` is what `provision.cloneOrgRelDir` computes; that helper is unexported, so either (a) add a tiny exported wrapper in `provision`, or (b) replicate the parse locally (host + org + repo-without-.git from the URL). Fall back to `home` when there is no CloneURL or the parse fails.

**6. `updateDest` — `transfer.go`:**
```go
func (m model) updateDest(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
    switch {
    case key.Matches(msg, m.keys.Back):
        m.view = viewBrowse
        return m, nil
    case key.Matches(msg, m.keys.Submit): // ctrl+s confirms
        return m.launchCopy()
    }
    var cmd tea.Cmd
    m.dest, cmd = m.dest.Update(msg)
    return m, cmd
}
```
`destView()` renders a title, the `m.dest.View()`, a hint ("the selected item is
placed INSIDE this directory"), and the help bar (Submit/Back).

**7. `launchCopy` + `beginTransfer` — `transfer.go`:** build endpoints per
direction, then reuse the streaming plumbing:
```go
func (m model) launchCopy() (tea.Model, tea.Cmd) {
    destDir := m.dest.Value()
    var src, dst string
    if m.transferUpload {
        src, dst = m.transferSrc, lima.GuestPath(m.transferVM, destDir)
    } else {
        src, dst = lima.GuestPath(m.transferVM, m.transferSrc), destDir
    }
    title := "Uploading " + path.Base(m.transferSrc)
    if !m.transferUpload { title = "Downloading " + path.Base(m.transferSrc) }
    run := func(ctx context.Context, out io.Writer) error {
        return m.cli.Copy(ctx, out, m.transferRecursive, src, dst)
    }
    cmd := m.beginTransfer(title, run)
    return m, cmd
}
```
`beginTransfer` is `beginProvision` minus the provisioner-specific config: set up
the pipe, `m.reader`, `m.running=true`, `m.canceled=false`, `m.doneErr=nil`,
`m.output=""`, `m.progressTitle`, `m.view=viewProgress`, the cancellable context
into `m.cancel`, **clear `m.provCfg = vm.CreateConfig{}`** so `provisionDoneMsg`
does not record it as managed, spawn the goroutine `pw.CloseWithError(run(ctx, pw))`,
and return `tea.Batch(readNextCmd(m.reader), m.spinner.Tick)`. Refactor by
extracting the shared body of `beginProvision` into a lower-level
`beginStream(title string, run func(ctx, io.Writer) error)` that both call, OR
copy the ~15 lines — either is fine; keep `beginProvision`'s behaviour identical.

**8. Tests — `model_test.go`:** construct the model as the existing tests do
(fake lima client). Set `m.view=viewDetail`, `m.detail = vm.VM{Name:"x", Status:"Running"}`,
feed a `tea.KeyMsg` for `u`; assert `m.view==viewBrowse`. Repeat with
`Status:"Stopped"`; assert `m.view` stays `viewDetail` and `m.status` mentions
"must be running". For the dest→progress transition, set up `m.view=viewDest`
with a `transferVM`/`transferSrc` and a dest value, feed `ctrl+s`; assert
`m.view==viewProgress` and `m.running`. Keep it to state transitions — do not run
a real copy.

**9. Module path:** import `browse`, `lima`, `vm` via their current
`github.com/deviantintegral/claude-code-ansible/tui/...` paths; do not add new
hard-coded module strings (plans 06/07 may rename).
</details>

### Meaningful Test Strategy Guidelines
Your critical mantra for test generation is: "write a few tests, mostly integration".
- **DO** test the integration/critical paths: the running-VM guard, Upload/Download opening the browser with the right lister, and the destination-confirm → progress transition.
- **DON'T** re-test the browser, dest prompt, or `Copy` (covered by their own tasks) or run a real transfer in a unit test.

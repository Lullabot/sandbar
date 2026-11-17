---
id: 4
group: "cli"
dependencies: [2]
status: "completed"
created: 2026-11-13
skills:
  - go
  - cli
complexity_score: 5
---
# Shared open-in-VS-Code operation and `sandbar code` CLI

## Objective
Implement the shared operation both the TUI and CLI call, and the `sandbar code NAME [--profile NAME]` command.

## Skills Required
Go, CLI design in `cmd/sandbar`.

## Acceptance Criteria
- [x] Shared function (e.g. `internal/vscode.Prepare(ctx, p provider.Provider, v vm.VM, cloneURL string) (Result, error)`) calls `EditorSSH`, `sshconfig.Upsert`, computes the folder (`GuestHome(v)` + `/` + `provision.CheckoutRelDir(cloneURL)` when a clone URL exists, else `GuestHome(v)`), and finds `code` (PATH via a seam, then `/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code` on darwin). `Result` carries Alias, Folder, CodePath ("" when not found), LaunchArgv (`code --remote ssh-remote+<alias> <folder>`), AuthorizedKey.
- [x] `Launch(argv)` starts the process detached (`cmd.Start`, no terminal stdio, `Process.Release`) via a package-level func var tests replace.
- [x] `sandbar code` resolves the owning profile via `resolveVMProfile`, refuses a non-running VM ("start it first"), checks `sshconfig.IncludeStatus`: missing + writable + stdin is a terminal → prompt `Add <line> to ~/.ssh/config? [y/N]`; non-terminal or "no" → print the line to add by hand; symlink/unwritable → print the line. Then runs Prepare and either launches (printing "Opening VS Code on <alias> (<folder>)") or prints the Remote-SSH fallback naming the alias.
- [x] Registered in `cmd/sandbar/main.go` dispatch and usage text.
- [x] Tests (isolated `HOME`/`XDG_CONFIG_HOME`/`XDG_DATA_HOME`, providerfake, fake launcher): launch argv for a VM with a clone URL, fallback when `code` is missing, non-running refusal. `go test ./cmd/sandbar ./internal/vscode` passes.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
Follow `cmd/sandbar/shell.go` for profile resolution/binding. Obtain the clone URL the way `reset`/`updates` read a VM's recorded config (marker/registry); reuse that path. No new module dependencies (check go.mod for an existing terminal-detection package).

## Input Dependencies
Task 1 (sshconfig), Task 2 (`EditorSSH`).

## Output Artifacts
`internal/vscode` (shared op + launcher seam) used by task 5; `sandbar code` command.

## Implementation Notes
<details>

- A new `internal/vscode` package importing `provider`, `provision`, `sshconfig`, `vm` avoids cycles; `internal/ui` can import it.
- Fallback text: `VS Code's "code" command was not found. In VS Code, run "Remote-SSH: Connect to Host…" and choose <alias>.`
- When AuthorizedKey is set, print `Authorized <path> in the guest.`
- Test philosophy: write a few tests, mostly integration; cover the critical workflow and error paths, not trivial plumbing.

</details>

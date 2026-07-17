---
id: 4
group: "sand-paste-image"
dependencies: [3]
status: "completed"
created: 2026-07-17
model: "sonnet"
effort: "medium"
skills:
  - go
  - cli
complexity_score: 5
complexity_notes: "Mirrors the existing sand shell command's flag/arg/profile handling; the orchestration is done by task 3, so this is entrypoint wiring plus result printing."
---
# CLI: `sand paste-image <vm>`

## Objective
Add the `sand paste-image <vm>` subcommand that reads the clipboard image and
stages it on the named VM's guest clipboard, mirroring `sand shell`'s exact
argument contract (one explicit VM name + `--profile`), and prints a clear
result.

## Skills Required
- `go` — a new `cmd/sand` subcommand.
- `cli` — flag parsing and dispatch consistent with the existing commands.

## Acceptance Criteria
- [ ] `sand paste-image <vm>` exists and is wired into `cmd/sand/main.go`'s
      subcommand dispatch.
- [ ] It requires exactly one VM name (`sand paste-image: need exactly one VM name`
      on 0 or >1, mirroring `runShell`) and honors `--profile`, resolving the
      provider the same way `sand shell` does (`resolveShellProvider` / the
      profile-resolution path in `cmd/sand/resolve.go`).
- [ ] Refuses cleanly when the VM is unknown or not running, with a specific
      message (same guard as `sand shell`).
- [ ] On success prints e.g. `staged image on <vm> — press Ctrl-V in the guest`;
      on an empty/non-image clipboard prints `no image on clipboard` and exits
      non-zero (or a documented zero — pick and state it), staging nothing.
- [ ] `go build ./...` succeeds; `sand paste-image --help` shows usage;
      `sand paste-image` with no arg errors as specified.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Copy the structure of `cmd/sand/shell.go` (`runShell`, `reorderShellFlags`,
  the `--profile` flag, the exactly-one-name check, the Running guard).
- Delegate all clipboard read + guest write to task 3's `PasteImage` core; this
  command only resolves the target and renders the result.
- Register in `main.go` alongside `create` / `shell`.

## Input Dependencies
- Task 3: the `PasteImage` orchestration core.

## Output Artifacts
- `cmd/sand/paste_image.go` (or similar) and the `main.go` registration.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

- Read `cmd/sand/shell.go` end-to-end and mirror it: same flag set (`--profile`),
  same `fs.Parse(reorder…)`, same `need exactly one VM name` error, same
  provider/scope resolution and Running check.
- After resolving `(prov, scope, vmName)` and confirming Running, call
  `PasteImage(ctx, prov, scope, vmName)` and switch on the result to print the
  message. Send the human message to stdout; errors to stderr.
- Keep the command name literally `paste-image` (decision 10 in the plan): it
  states the image-only contract.
- No file-browser, no interactivity — it is a one-shot command.
</details>

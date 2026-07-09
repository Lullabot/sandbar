---
id: 3
group: "cli"
dependencies: [1]
status: "completed"
created: 2026-07-09
model: "sonnet"
effort: "high"
skills:
  - go
  - cli
---
# `sand secret` CLI — set / list / rm

## Objective
Add the `sand secret` command group so users can add, inspect (masked), and remove secrets on a VM's host store, reading secret values without ever placing them on argv.

## Skills Required
- **go** — a new `sand secret` subcommand tree in `cmd/sand`, using the `internal/secrets` package.
- **cli** — flag/argument parsing consistent with the existing `sand create` style; stdin/prompt value entry; masked output.

## Acceptance Criteria
- [ ] `go build ./...` and `go test ./cmd/sand/...` pass.
- [ ] `sand secret set MY_VAR` reads the value from stdin (e.g. `printf 'v\n' | sand secret set MY_VAR --vm test`) and stores it as a global secret; the value never appears in the args (verify a `set` invocation while inspecting `ps` shows no value on argv, or unit-test that the value comes from the reader, not a positional arg).
- [ ] `sand secret set TOK --vm test --dir github.com/acme --github` stores a directory-scoped GitHub token.
- [ ] `sand secret set VAR --vm test --dir some/dir` stores a directory-scoped generic env var.
- [ ] `sand secret list --vm test` prints secret names with masked values; `--reveal` shows cleartext.
- [ ] `sand secret rm MY_VAR --vm test` removes it; a follow-up `list` no longer shows it.
- [ ] Help text (`sand secret --help`) documents the subcommands and the stdin value convention.

## Technical Requirements
- Values are read from stdin (or an interactive prompt when a TTY), **never** taken as a positional/flag argument, mirroring the existing "never on argv" hygiene.
- `--vm <name>` selects the target VM (default to the current/selected VM if the codebase has that notion; otherwise require it).
- `--dir <relpath>` marks a secret directory-scoped; its absence means VM-global. `--github` marks a GitHub credential (implies the `github` category).
- Use `internal/secrets` for all persistence and redaction.

## Input Dependencies
- Task 1: `internal/secrets` store package (types, load/save, redaction).

## Output Artifacts
- `sand secret set|list|rm` wired into `cmd/sand` command dispatch.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

Follow the pattern in `cmd/sand/create.go`/`main.go` for subcommand registration and `flag.NewFlagSet` usage. Add a `secret` command with `set`, `list`, `rm` leaves.

Value entry: read from `os.Stdin`. If stdin is a TTY, print a prompt to stderr (`Enter value for MY_VAR: `) and read a line (optionally without echo — a simple line read is acceptable if no-echo adds too much dependency; document the choice). Never accept the value as a CLI argument.

Category selection:
- no `--dir` → `global` (append `{name, value}`).
- `--dir X --github` → `github` (append `{scope: X, token: value}`); `--github` with no `--dir` → default GitHub token (`scope: ""`).
- `--dir X` (no `--github`) → `dir_env` (append `{scope: X, name, value}`).

`list` uses the redaction helper from task 1 by default; `--reveal` prints raw values. `rm` removes by (category, scope, name) and saves.

Do not render into any VM here — this task only mutates the host store. (Applying to a running VM is task 5's `sync`.) Keep every path free of logging secret values.
</details>

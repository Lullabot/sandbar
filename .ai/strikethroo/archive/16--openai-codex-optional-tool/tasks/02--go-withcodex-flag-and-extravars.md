---
id: 2
group: "go-toolset"
dependencies: []
status: "completed"
created: 2026-07-16
model: "sonnet"
effort: "medium"
skills:
  - golang
---
# Wire WithCodex through CreateConfig, the --with-codex flag, and Ansible extra-vars

## Objective

Register `codex` as the fifth toolset tool in the Go binary: a `WithCodex` field on `vm.CreateConfig` (default **false**), a `"codex"` entry in `ToolPtrs()`, a `--with-codex` CLI flag on `sand create`, and a `toolset_codex` extra-var emitted by `provision.BuildExtraVars` — with the existing table-driven tests extended to prove both the enabled and unchanged-default cases.

## Skills Required

`golang` — idiomatic additions to existing, well-commented wiring; table-driven test extensions.

## Acceptance Criteria

- [x] `internal/vm/vm.go`: `CreateConfig` has `WithCodex bool`; the field-block comment is updated (it currently names four fields/flags and says "All four default to true") to note codex is the deliberate exception — opt-IN, default false; `DefaultCreateConfig()` leaves `WithCodex` false (explicitly or by omission, with a comment); `ToolPtrs()` gains `"codex": &c.WithCodex`.
- [x] `cmd/sand/create.go`: registers `fs.BoolVar(&cfg.WithCodex, "with-codex", cfg.WithCodex, "Install OpenAI Codex in the base image")` alongside the other four `--with-*` flags.
- [x] `internal/provision/vars.go`: `BuildExtraVars` emits `varItem{"toolset_codex", cfg.WithCodex}` with the other toolset vars (base/full phases only — the existing structure already omits them for finalize; keep it that way).
- [x] Tests extended (in their existing table-driven style, same files): `internal/vm/vm_test.go` covers `ToolsetKey()` including codex when enabled AND that a default config's key omits codex (stamp unchanged for existing users — assert the exact default key string `claude+ddev+go+java`); `ApplyToolset` round-trips a set containing `codex`. `internal/provision/vars_test.go` asserts `toolset_codex` is emitted `false` by default, `true` when `WithCodex` is true, and absent for the finalize phase.
- [x] Verification: `go test ./internal/vm/... ./internal/provision/... ./cmd/...` exits 0 with the new assertions present.
- [x] Verification: `go vet ./...` exits 0.
- [x] Verification: `go run ./cmd/sand create --help 2>&1 | grep with-codex` prints the new flag line.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- `ToolPtrs()` is the single registration point (its comment: "Adding a tool means adding a field, a line here, and its flag; nothing else has to learn the name"). Do NOT touch `ToolsetKey`, `ApplyToolset`, the create-time adopt-unless-explicit logic in `cmd/sand/create.go` (the `fs.Visit`/`BaseToolset` block), or the shrink-warning machinery in `internal/provision/baseversion.go` — they iterate `ToolPtrs()`/parse stamps generically and pick codex up for free.
- Match surrounding comment density and style; the codebase is heavily commented with rationale.
- `internal/ui` compiles against `CreateConfig` but its toggle is Task 3's scope — do not add the TUI toggle here; ensure `go build ./...` still passes without it (it will: the field is additive).

## Input Dependencies

None — first-phase task. Reference material: `internal/vm/vm.go:55-149` (field block, defaults, ToolPtrs/ApplyToolset/ToolsetKey), `cmd/sand/create.go:102-105` (flag registrations) and `:138-146` (adoption loop, no changes), `internal/provision/vars.go:58-61`, `internal/vm/vm_test.go`, `internal/provision/vars_test.go:83-160`.

## Output Artifacts

- Updated `internal/vm/vm.go`, `cmd/sand/create.go`, `internal/provision/vars.go` and their tests.
- Consumed by Task 3 (TUI toggle reads/writes `cfg.WithCodex`) and Task 4 (docs document the flag; help dump regen needs it).

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

Key invariant to preserve and test: `ToolsetKey()` renders only ENABLED tools sorted alphabetically, so a default (codex-off) config must render exactly `claude+ddev+go+java` — byte-identical to the previous release; that is Success Criterion 2 of the plan. When codex is enabled with the others, the key is `claude+codex+ddev+go+java` (alphabetical). Add both as explicit assertions.

In `DefaultCreateConfig()`, the four existing fields are set `true` explicitly; leave `WithCodex` out of the literal and add a short comment in the field-block doc (or next to the literal) stating codex is deliberately opt-in — the zero value is the default. Update the `WithClaude, WithDDEV...` doc comment (vm.go:61-68): it says "All four default to true ... the flags are opt-OUT"; reword to cover five fields with codex as the opt-in exception.

In `vars_test.go`, follow the existing test names/patterns (`TestBuildExtraVars_...`); there is an existing test asserting the four toolset vars for the base phase and one asserting finalize omits them — extend both, and add the codex-true case (mirror `TestBuildExtraVars_ClaudeCanBeDeselected`, inverted: codex can be selected).

`baseversion.go` needs no code change; `parseToolset`/`toolsetFromStamp` are name-agnostic. Do not add tests there unless an existing table makes it a one-line addition.

Run `gofmt` via `go fmt ./...` if needed; CI runs `go test ./... -race`.
</details>

---
id: 1
group: "go-rename"
dependencies: []
status: "completed"
created: "2026-07-03"
skills:
  - go
---
# Go module + binary rename (module path, imports, `cmd/sand`, app-name strings)

## Objective
Make the Go code self-consistent under the new module path `github.com/lullabot/sandbar/tui` and the new command/binary name `sand`. After this task the module builds a `sand` binary, `go test ./...` is green, and no old **import path** or user-facing **app name** (`claude-vm`) survives in the Go UI/command/registry code.

## Skills Required
- `go` — module path rewrite, command-directory rename, reading the build/test signal as the completeness check.

## Acceptance Criteria
- [ ] `tui/go.mod` module line is `module github.com/lullabot/sandbar/tui`.
- [ ] Every Go import of `github.com/deviantintegral/claude-code-ansible/tui/...` is rewritten to `github.com/lullabot/sandbar/tui/...`. `grep -rn 'deviantintegral/claude-code-ansible/tui' tui --include='*.go'` returns **0**.
- [ ] Command directory renamed: `tui/cmd/claude-vm` → `tui/cmd/sand` (via `git mv`), and `tui/cmd/sand/main.go` doc comment says `sand`.
- [ ] User-facing app name and app-name comments changed `claude-vm` → `sand` in `tui/internal/ui/{list,detail,model}.go`, `tui/internal/ui/model_test.go`, and the `claude-vm` **comments** in `tui/internal/registry/registry.go` (lines ~1, 34, 93).
- [ ] `cd tui && go build -o /tmp/sand ./cmd/sand` succeeds (binary is `sand`).
- [ ] `cd tui && go test ./...` passes.
- [ ] `grep -rn 'claude-vm' tui/cmd tui/internal/ui` returns **0**.

## Technical Requirements
Go 1.24 toolchain (already present). Module path is load-bearing across **46 import lines in 24 files**; a single `go.mod` edit plus a scripted import rewrite covers them, and the compiler + tests prove completeness.

## Input Dependencies
None — this is the first task.

## Output Artifacts
- New module path and `cmd/sand` command directory that downstream tasks (internal-artifact sweep, external sweep, data-dir migration) build on.

## Implementation Notes

<details>
<summary>Step-by-step</summary>

1. **Module path** — in `tui/go.mod` change:
   `module github.com/deviantintegral/claude-code-ansible/tui` → `module github.com/lullabot/sandbar/tui`.

2. **Rewrite all imports** — only the module import prefix, over Go files:
   ```bash
   cd /home/debian/claude-code-ansible
   grep -rl 'github.com/deviantintegral/claude-code-ansible/tui' tui --include='*.go' \
     | xargs sed -i 's#github.com/deviantintegral/claude-code-ansible/tui#github.com/lullabot/sandbar/tui#g'
   ```
   This matches only the `/tui`-suffixed module import — it will **not** touch repo-URL fixtures like `https://github.com/deviantintegral/claude-code-ansible` (no `/tui`), which are handled in task 3.

3. **Rename the command directory** (preserve history):
   ```bash
   git mv tui/cmd/claude-vm tui/cmd/sand
   ```
   Then in `tui/cmd/sand/main.go` line 1: `// Command claude-vm is the interactive TUI ...` → `// Command sand is the interactive TUI ...`.

4. **App-name strings & comments** `claude-vm` → `sand` in:
   - `tui/internal/ui/list.go`: the title `titleStyle.Render("claude-vm")` (→ `"sand"`), the two `m.status = "...claude-vm..."` strings (lines ~171, 270), and the code comments (lines ~17, 37, 53, 266) — e.g. "claude-vm-managed VMs" → "sand-managed VMs", "claude-vm base image" → "sand base image".
   - `tui/internal/ui/detail.go` line ~40: `managed = "yes (claude-vm)"` → `"yes (sand)"`.
   - `tui/internal/ui/model.go` (package doc line 1, comments lines ~57, 240): "claude-vm" → "sand".
   - `tui/internal/ui/model_test.go` line ~69 comment: "claude-vm-managed" → "sand-managed".
   - `tui/internal/registry/registry.go` comments only (lines ~1, 34, 93): "created by claude-vm" / "claude-vm-managed" → "created by sand" / "sand-managed". **Do not** touch the data-dir string on lines ~47/57 (`claude-code-ansible`) — that is task 4.

5. **Do NOT** touch (left for later tasks, to keep coupled pairs and data-dir together):
   - `tui/internal/provision/{provision,staging,baseversion}.go` and `provision_test.go` `claude-vm-*` **artifact** paths/prefixes (task 2).
   - Repo-URL fixtures in `provision_test.go`, `staging_test.go`, `overlay_test.go` (task 3).
   - The `claude-code-ansible` data-dir string in `registry.go` (task 4).

6. **Verify**:
   ```bash
   cd tui && go build -o /tmp/sand ./cmd/sand && go test ./... && \
   grep -rn 'claude-vm' cmd internal/ui && echo "STRAGGLERS ABOVE" || echo "ui/cmd clean"
   grep -rn 'deviantintegral/claude-code-ansible/tui' . --include='*.go' && echo "IMPORT STRAGGLERS" || echo "imports clean"
   ```
</details>

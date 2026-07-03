---
id: 3
group: "external-sweep"
dependencies: [2]
status: "pending"
created: "2026-07-03"
skills:
  - bash
  - go
---
# External-reference sweep (repo/raw URLs, org, app name in docs + test fixtures)

## Objective
Repoint every non-module, non-data-dir reference to the new identity: `deviantintegral` → `lullabot`, `claude-code-ansible` → `sandbar`, and the app name `claude-vm` → `sand` in docs. Covers the install/CI/doc URLs and the repo-URL/clone-path **test fixtures** in Go tests. The data-dir `CACHE_DIR`/`defaultPath` code and its migration are intentionally left to task 4.

## Skills Required
- `bash` — edit `install.sh`, `scripts/new-vm.sh`, and Markdown docs.
- `go` — update repo-URL string fixtures in Go tests, keeping each test's input URL and its expected-output assertions consistent.

## Acceptance Criteria
- [ ] `install.sh`: header curl URL + `REPO_URL` point at `https://github.com/lullabot/sandbar` / `...sandbar.git`.
- [ ] `scripts/new-vm.sh`: `REPO_URL`, `INSTALL_URL`, and the `--recreate` curl URL point at `lullabot/sandbar`.
- [ ] `README.md` and `tui/README.md`: repo/raw URLs, app name (`claude-vm`→`sand`, incl. `go build -o sand ./cmd/sand`), and data-dir prose (`~/.local/share/claude-code-ansible` → `~/.local/share/sandbar`) updated.
- [ ] Go test URL fixtures updated **with matching assertions**: `provision_test.go`, `staging_test.go`, `overlay_test.go`.
- [ ] `grep -rn 'deviantintegral' install.sh scripts/new-vm.sh README.md tui/README.md tui/internal` returns **0**.
- [ ] `grep -rn 'claude-vm' README.md tui/README.md` returns **0**.
- [ ] `cd tui && go test ./...` passes; `shellcheck install.sh scripts/new-vm.sh` passes.

## Technical Requirements
Raw content URLs and the module path do not follow GitHub redirects, so curl|bash one-liners must be updated outright. The Go test fixtures assert that the provisioning code echoes the clone URL into tar/chown paths, so input URL and expected tokens must change together or the tests fail.

## Input Dependencies
- Task 2 (internal-artifact sweep) — sequenced so `new-vm.sh` is edited by one task at a time.

## Output Artifacts
- All external repo/app references point at `lullabot/sandbar` / `sand`, leaving only the data-dir code + migration for task 4.

## Implementation Notes

<details>
<summary>Step-by-step</summary>

Replacement rules for URLs: `deviantintegral/claude-code-ansible` → `lullabot/sandbar` (in `github.com/...` and `raw.githubusercontent.com/...` URLs, `.git` suffix preserved).

1. **`install.sh`**: header comment curl URL (line ~5) and `REPO_URL` (line ~15). Leave `CACHE_DIR` (line ~16) for task 4.

2. **`scripts/new-vm.sh`**: `REPO_URL` (~23), `INSTALL_URL` (~25), the `--recreate` curl URL (~173), and any header/comment URL. Leave `CACHE_DIR` (~24) for task 4.

3. **`README.md`**: curl URLs (lines ~18, 29); data-dir prose (~21) `~/.local/share/claude-code-ansible` → `~/.local/share/sandbar`; app-name mentions (~70, ~115) `claude-vm` → `sand`.

4. **`tui/README.md`**: title/heading (lines ~1, 3) `claude-vm` → `sand`; build/run commands (~31, 32) `go build -o claude-vm ./cmd/claude-vm` → `go build -o sand ./cmd/sand` and `./claude-vm` → `./sand`; prose mentions (~55, 62, 233, 237) `claude-vm` → `sand`; data-dir path (~245) `~/.local/share/claude-code-ansible` → `~/.local/share/sandbar`.

5. **Go test URL fixtures** — change input URL **and** expected assertions together:
   - `tui/internal/provision/provision_test.go`: every `cfg.CloneURL = "https://github.com/deviantintegral/claude-code-ansible"` → `"https://github.com/lullabot/sandbar"`; and the assertions/labels referencing `github.com/deviantintegral` (the stage-out tar `-czf`, the `chown` path `/home/andrew/github.com/deviantintegral`, comments) → `github.com/lullabot`.
   - `tui/internal/provision/staging_test.go`: the URL-parse test cases (~lines 55, 82) — input `https://github.com/deviantintegral/claude-code-ansible` → `https://github.com/lullabot/sandbar`, and expected org/repo tokens `github.com/deviantintegral` → `github.com/lullabot`, `github.com/deviantintegral/claude-code-ansible` → `github.com/lullabot/sandbar`.
   - `tui/internal/provision/overlay_test.go` (~line 13): `const playbookDir = "/home/andrew/src/claude-code-ansible"` → `"/home/andrew/src/sandbar"`.

6. **Verify**:
   ```bash
   cd /home/debian/claude-code-ansible
   grep -rn 'deviantintegral' install.sh scripts/new-vm.sh README.md tui/README.md tui/internal
   grep -rn 'claude-vm' README.md tui/README.md
   cd tui && go test ./...
   cd /home/debian/claude-code-ansible && shellcheck install.sh scripts/new-vm.sh
   ```
   `claude-code-ansible` will still appear on the `CACHE_DIR` lines of `install.sh`/`new-vm.sh` and in `registry.go` — that is expected and handled by task 4.
</details>

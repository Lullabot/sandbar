---
id: 5
group: "verification"
dependencies: [4]
status: "completed"
created: "2026-07-03"
skills:
  - go
  - bash
---
# Completeness verification gate (build + test + vet/fmt + shellcheck + repo-wide grep == 0)

## Objective
Prove the rename is complete and consistent: the module builds a `sand` binary, all tests pass, the scripts pass shellcheck, and a repository-wide search (excluding `.ai/task-manager/` and `todo.md`) finds **zero** `deviantintegral`, `claude-code-ansible`, or `claude-vm` references. Fix any straggler found here.

## Skills Required
- `go` — build/test/vet/fmt the module.
- `bash` — shellcheck + the repo-wide completeness grep.

## Acceptance Criteria
- [ ] `cd tui && go build -o /tmp/sand ./cmd/sand` succeeds (binary is `sand`).
- [ ] `cd tui && go vet ./...` is clean and `gofmt -l .` prints nothing.
- [ ] `cd tui && go test ./...` passes.
- [ ] `shellcheck install.sh scripts/new-vm.sh` passes.
- [ ] Repo-wide grep (excluding `.ai/`, `.git/`, `todo.md`) for `deviantintegral` and `claude-vm` returns **0** hits.
- [ ] Repo-wide grep for `claude-code-ansible` returns **only** the intentional legacy-path references in the index-migration/cleanup machinery — `registry.go` (`migrateLegacyIndex` + comment), `registry_test.go` (the migration test), and the `OLD_CACHE_DIR` cleanup guards in `install.sh`/`new-vm.sh` — and **zero** stale identity references. (These deliberate references are the mechanism that satisfies Success Criterion #4 — migrate a pre-existing `claude-code-ansible` index — so they reconcile with, rather than violate, Criterion #3's "zero references" intent. Plan 07 removes the shell scripts; the registry migration is the durable part.)
- [ ] The two coupled pairs read identically on both endpoints: `/dev/shm/sand-vars.yml` (`new-vm.sh` ↔ `provision.go`) and `/var/log/sand-{provision,finalize}.log` (`new-vm.sh` ↔ `.github/workflows/test.yml`).

## Technical Requirements
The Go build + tests catch any missed import or broken reference; the repo-wide grep catches missed strings; the coupled-pair endpoint read catches a one-sided rename that the build cannot detect (ephemeral runtime paths).

## Input Dependencies
- Tasks 1–4 complete.

## Output Artifacts
- A green build/test/lint signal and a zero-hit completeness grep confirming Success Criteria 2 & 3 of the plan.

## Implementation Notes

<details>
<summary>Step-by-step</summary>

1. **Build / vet / fmt / test**:
   ```bash
   cd /home/debian/claude-code-ansible/tui
   go build -o /tmp/sand ./cmd/sand
   go vet ./...
   gofmt -l .            # must print nothing
   go test ./...
   ```

2. **Shellcheck**:
   ```bash
   cd /home/debian/claude-code-ansible
   shellcheck install.sh scripts/new-vm.sh
   ```

3. **Repo-wide completeness grep** (the plan excludes planning docs + todo.md):
   ```bash
   cd /home/debian/claude-code-ansible
   grep -rn -e deviantintegral -e claude-code-ansible -e claude-vm . \
     --exclude-dir=.ai --exclude-dir=.git --exclude=todo.md
   ```
   Expected output: **empty**. Any hit is a straggler — fix it in place (respecting the coupled-pair and data-dir rules from tasks 2 and 4), then re-run build/test and the grep.

4. **Confirm coupled pairs by reading both endpoints**:
   ```bash
   grep -n 'sand-vars.yml' scripts/new-vm.sh tui/internal/provision/provision.go
   grep -n 'sand-provision.log\|sand-finalize.log' scripts/new-vm.sh .github/workflows/test.yml
   ```
   Each pair must show the same path on both sides.

5. Note in the task result the one-time `~/.lima/_sand/` base-image rebuild (expected on first run after the rename — not a regression), and that the lima-e2e CI job is what exercises the coupled provisioning paths end-to-end.
</details>

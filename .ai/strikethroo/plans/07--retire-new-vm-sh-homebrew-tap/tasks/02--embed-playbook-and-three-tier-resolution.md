---
id: 2
group: "sand-binary"
dependencies: [1]
status: "completed"
created: 2026-07-06
model: "sonnet"
effort: "high"
skills:
  - go
complexity_score: 7
complexity_notes: "Subtle correctness: go:embed silently drops dot/underscore files without the all: prefix; the embedded FS must materialise byte-identically and its temp dir must outlive the read-only /mnt/playbook mount through provisioning. New three-tier resolution plus a fileset-completeness unit test."
---
# Embed the playbook and make resolution working-tree-first

## Objective
Make the `sand` binary self-sufficient with no repo checkout on disk (the
Homebrew case) while preserving the working-tree dev loop. Embed the playbook
fileset into the binary with `//go:embed`, and rework `LocatePlaybook` into
two-tier resolution: **(1)** if run inside a git checkout containing `site.yml`,
return the working tree (today's behaviour — uncommitted playbook edits still
provision the VM); **(2)** otherwise materialise the embedded fileset into a
private temp dir and return that path. The existing read-only `/mnt/playbook`
mount, in-guest `rsync -a --delete`, and vars-over-stdin secret handling are
unchanged.

## Skills Required
- **go** — `//go:embed` / `embed.FS`, `io/fs` walking, materialising an embedded
  tree to disk, and a table-free unit test asserting fileset completeness.

## Acceptance Criteria
- [ ] The playbook is embedded via `//go:embed` covering `site.yml`,
      `ansible.cfg`, `inventory`, `roles`, and `group_vars`, using the `all:`
      prefix for directories that may hold dot/underscore files
      (e.g. `//go:embed all:roles`).
- [ ] `LocatePlaybook` returns the git working tree when run inside a checkout
      containing `site.yml`, and otherwise writes the embedded fileset to a
      fresh private temp dir and returns that path.
- [ ] A unit test runnable via `go test ./internal/provision -run Embed` asserts
      the embedded FS contains `site.yml`, `ansible.cfg`, `inventory`, at least
      one `roles/*/tasks/main.yml`, and the `group_vars/` file(s) — proving
      `all:roles` did not silently drop dot/underscore files.
- [ ] `go build ./cmd/sand` and `go test ./...` both pass from the repo root.
- [ ] Running the built binary from **outside** any git checkout (e.g. copy
      `sand` to `/tmp` and run `--help` or a dry code path) does not error on
      playbook resolution with "not inside a git checkout"; resolution falls
      through to the embedded copy. (Full provisioning is exercised in task 5.)

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- `//go:embed` cannot reference files outside its own module or use `..`. After
  task 1 the module root is the repo root, so the embed directive must live in a
  `.go` file **at the repo root** (the playbook files are its siblings). It
  cannot live in `internal/provision` (a subdirectory still cannot reach the
  parent's files).
- Suggested shape: a root-level file (e.g. `playbook_embed.go`,
  `package sandbar`) declaring `//go:embed site.yml ansible.cfg inventory all:roles all:group_vars`
  and exporting `var PlaybookFS embed.FS`. Have `internal/provision` consume it
  — either import the root package for `sandbar.PlaybookFS`, or pass an `fs.FS`
  into a resolver function so the package stays decoupled. Root package
  `sandbar` must not import `internal/…` (avoid an import cycle).
- Current resolver: `LocatePlaybook()` in `internal/provision/playbook.go` does
  `git rev-parse --show-toplevel` and checks for `site.yml`; its clone-to-cache
  `TODO` is retired by this task (embed replaces it).
- Materialisation must be byte-identical to the working tree so the embedded and
  checkout paths provision the same guest. Preserve file contents exactly; a
  0755/0644 mode split is fine (the mount is read-only and re-`rsync`ed in-guest).
- The temp dir must live long enough: `RenderBaseOverlay` mounts the returned
  path read-only at `/mnt/playbook` and the guest `rsync`s from it during
  provisioning, so the dir must not be deleted until the caller is done. Do not
  `defer os.RemoveAll` inside the resolver; leave cleanup to process exit (temp
  dir) or return the path for the caller to own.
- `gitPlaybookVersion` (baseversion.go) will fail on the embedded temp dir
  (not a git checkout). That is already handled: `baseStale` treats a
  version-lookup error as "not stale" and `BuildBase` logs a note and proceeds.
  No change needed there, but confirm the embedded path does not rebuild-loop.

## Input Dependencies
- **Task 1** (module at repo root) — required: `go:embed` can only reach the
  playbook once the module root is the repo root.

## Output Artifacts
- An embedded playbook fileset and a checkout-free resolution path. This is what
  lets a Homebrew-installed binary (task 4) provision with no repo present, and
  it is exercised end-to-end by the CI migration (task 5, working-tree path) and
  validated on release (embedded path).

## Implementation Notes
The crux risk is `go:embed` silently excluding files whose names begin with `.`
or `_` under `roles/` — the `all:` prefix prevents that, and the unit test is
your guard. Test *your* materialisation and fileset completeness (custom logic),
not the `embed` package itself.

<details>
<summary>Step-by-step</summary>

1. Create a repo-root file, e.g. `playbook_embed.go`:
   ```go
   package sandbar

   import "embed"

   //go:embed site.yml ansible.cfg inventory all:roles all:group_vars
   var PlaybookFS embed.FS
   ```
   (`package sandbar` at the module root. Adjust the exact file list if `roles/`
   or `group_vars/` gains members; `all:` covers dot/underscore names.)
2. Rework `internal/provision/playbook.go`:
   - Keep tier 1: `git rev-parse --show-toplevel`; if it succeeds and
     `<top>/site.yml` exists, return `<top>` (unchanged behaviour).
   - Add tier 2: on any failure of tier 1, materialise the embedded FS to a
     private temp dir and return it. Implementation:
     ```go
     dir, err := os.MkdirTemp("", "sand-playbook-*")
     // walk sandbar.PlaybookFS with fs.WalkDir, recreating dirs and
     // os.WriteFile-ing each file (read via fs.ReadFile). Return dir.
     ```
     Pass `sandbar.PlaybookFS` in (import the root package) or thread an `fs.FS`
     parameter through from `cmd/sand/main.go`. Do not delete the temp dir in
     the resolver — the mount/rsync happen later.
   - Delete the stale clone-to-cache `TODO` comment block.
3. Add the embed unit test in `internal/provision` (so
   `go test ./internal/provision -run Embed` matches plan Self-Validation):
   walk the embedded FS (or the materialised dir) and assert the required
   members exist. Fail loudly if `roles/*/tasks/main.yml` or the `group_vars`
   file is missing (that would mean an embed pattern dropped files).
4. Update `cmd/sand/main.go` only if you chose the `fs.FS`-parameter approach
   (pass `sandbar.PlaybookFS` into the resolver / `Provisioner`). If you used
   the imported-package approach, `main.go` may be unchanged.
5. Verify: `go build ./cmd/sand`, `go test ./...`, then copy `./sand` to `/tmp`
   and run it from there to confirm resolution no longer hard-fails outside a
   checkout. (A full provision from the embedded copy is validated in task 5 and
   at release — not required to pass here.)
6. Do **not** add the `sand create` subcommand here (task 3) or touch CI/release
   (tasks 4–5). Scope is embed + resolver + test.
</details>

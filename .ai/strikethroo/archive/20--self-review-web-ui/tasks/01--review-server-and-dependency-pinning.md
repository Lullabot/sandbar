---
id: 1
group: "review-webapp"
dependencies: []
status: "completed"
created: 2026-08-04
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Orchestrates a third-party Node API into an HTTP contract, owns the loopback-binding security property, and establishes the dependency pinning every later task builds on."
skills:
  - nodejs
  - renovate
---
# Review server and pinned self-review dependencies

## Objective

Create the Node HTTP server half of the sandbar review web app at
`roles/self-review/files/webapp/`, backed by `@self-review/core`, together with
the pinned `package.json` / lockfile it needs and the Renovate rule that keeps
the three `@self-review/*` packages moving in lockstep.

## Skills Required

- **nodejs** — a `node:http` server, argv parsing, filesystem writes, and the
  `@self-review/core` API.
- **renovate** — one `packageRules` entry in the existing `renovate.json`.

## Acceptance Criteria

- [ ] `roles/self-review/files/webapp/package.json` exists, is `private`, uses
      `"type": "module"`, and pins `@self-review/core`, `@self-review/react` and
      `@self-review/types` to the **same exact** version (no `^`/`~`).
      `npm ls @self-review/core @self-review/react @self-review/types` after
      `npm ci` prints one identical version for all three.
- [ ] `npm ci` in that directory succeeds and produces a committed
      `package-lock.json`.
- [ ] `node server/index.mjs --repo <path> --port <n>` starts and, verified with
      `ss -ltn | grep <n>`, listens on `127.0.0.1:<n>` and **not** `0.0.0.0:<n>`.
- [ ] Against a scratch git repo with one modified tracked file:
      `curl -sS 127.0.0.1:<n>/api/diff` returns JSON whose files array contains
      that file's path with at least one hunk.
- [ ] `curl -sS 127.0.0.1:<n>/api/config` returns a JSON config payload
      (HTTP 200).
- [ ] `curl -sS -X POST 127.0.0.1:<n>/api/review -H 'content-type: application/json' -d @state.json`
      writes `review.xml` into the repo root; `cat review.xml` shows the posted
      comment's body, file path and line, and the server process then exits with
      status 0.
- [ ] `curl -sS -o /dev/null -w '%{http_code}' 127.0.0.1:<n>/` prints `200` and
      serves `dist/index.html` when `dist/` exists; when `dist/` is absent it
      returns a clear error rather than crashing.
- [ ] `node --test` in the webapp directory passes, covering payload assembly
      from a real temporary git repo and the review-state → XML round trip.
- [ ] `renovate.json` gains a `packageRules` entry grouping `@self-review/**`
      into a single PR, carrying a `description` like every other entry in that
      file. `npx --yes -p renovate renovate-config-validator renovate.json`
      exits 0. (The `-p renovate` is required: `renovate-config-validator` is a
      bin inside the `renovate` package, not a package name npx can resolve.)
- [ ] `roles/self-review/files/webapp/node_modules/` is git-ignored.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Node 24 (matches `base_nodejs_major_version`), ES modules, `node:http` —
  **no web framework**; the plan's minimal-dependency principle applies.
- `@self-review/core` exports used: `getRepoRootAsync`, `runGitDiffAsync`,
  `parseDiff`, `getUntrackedFilesAsync`, `generateUntrackedDiffs`,
  `computePayloadStats`, `createIgnoreFilter`, `loadConfig`, `checkWritability`,
  `serializeReview`.
- Tests use Node's built-in `node --test` runner — do not add a test framework.
- The server must bind `127.0.0.1` explicitly. This is a security property, not
  a default to rely on.

## Input Dependencies

None. This task establishes the dependency pinning and the HTTP contract that
task 2 (browser client) and task 5 (CLI orchestration) consume.

## Output Artifacts

- `roles/self-review/files/webapp/package.json` and `package-lock.json`
- `roles/self-review/files/webapp/server/index.mjs`
- `roles/self-review/files/webapp/server/*.test.mjs`
- The `@self-review/**` grouping rule in `renovate.json`
- A `.gitignore` entry for the webapp's `node_modules/`

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Pin the exact version.** Look up the current version with
`npm view @self-review/core version` and use that identical exact string for all
three packages. They are released in lockstep and mixing versions violates the
contract between them. Add `react` and `react-dom` (v19) as dependencies too —
task 2 needs them and they belong in the same manifest.

**Directory layout.**

```
roles/self-review/files/webapp/
  package.json
  package-lock.json
  server/
    index.mjs
    payload.mjs          # diff payload assembly, unit-testable
    payload.test.mjs
    review.test.mjs
  src/                   # task 2 fills this in
  vite.config.ts         # task 2
```

**Argument parsing.** Accept `--repo <path>` (required, the checkout root),
`--port <n>` (required), and `--diff-args <string>` (optional; the git diff
range to review, e.g. `main...HEAD`). Absent `--diff-args`, review the working
tree, which is `@self-review/core`'s own default. Do not shell out to git
directly — go through the core exports.

**Endpoints.**

- `GET /api/diff` → a `DiffLoadPayload`. Resolve the repo root with
  `getRepoRootAsync`, run `runGitDiffAsync` with the requested range, feed the
  raw diff to `parseDiff`, add untracked files via `getUntrackedFilesAsync` +
  `generateUntrackedDiffs`, filter through `createIgnoreFilter`, and attach
  `computePayloadStats`. Mirror the shape the Electron app's own diff request
  produces — read `src/main/ipc-handlers.ts` and `src/main/git-diff-loader.ts`
  in the upstream repo for the exact field expectations.
- `GET /api/config` → `{ config, outputPathInfo }` from `loadConfig` and
  `checkWritability`.
- `POST /api/review` → parse the JSON body as a `ReviewState`, pass it to
  `serializeReview`, write the result to `review.xml` in the repo root, respond
  `200`, then `server.close()` and `process.exit(0)`. **Exit is the completion
  signal** the CLI in task 5 waits on — do not keep running after a submit.
- `GET /` and `GET /assets/*` → static files from `dist/`, with correct
  content types for `.html`, `.js`, `.css`, `.svg` and `.woff2`.

Return JSON errors with a non-2xx status and a readable `error` field; never let
an exception take the process down mid-review.

**Static path safety.** Resolve every static request against `dist/` and reject
anything that escapes it after `path.resolve`, so a `../` request cannot read
outside the bundle.

**Tests (`node --test`).** Keep to the plan's "a few tests, mostly integration"
philosophy — test *this* code, not `@self-review/core` and not `node:http`:

1. Build a temporary git repo in `os.tmpdir()` (init, commit a file, modify it,
   add an untracked file), point the payload builder at it, and assert the
   payload names both the modified and untracked file and carries hunks.
2. Round-trip a minimal `ReviewState` with one comment through the submit
   handler and assert the written `review.xml` contains the comment body, file
   path and line.

Do not write tests for trivial argv parsing or for static file serving.

**Renovate rule.** Add to the existing `packageRules` array in `renovate.json`:

```json
{
  "description": "self-review's three packages are released in lockstep; a mixed set breaks the embedding contract, so bump them together",
  "matchPackageNames": ["@self-review/**"],
  "groupName": "self-review"
}
```

The repository's existing `minimumReleaseAge` and automerge settings already
apply — do not restate them. No custom regex manager is needed: Renovate's
native npm manager discovers the `package.json` on its own.

**Do not install node_modules into the repo working tree beyond what you need
to verify the task**, and make sure `node_modules/` is git-ignored. `roles/` is
embedded into the `sand` binary via `go:embed all:roles` in `playbook_embed.go`,
so a stray `node_modules` tree on disk would be embedded into a locally built
binary. Verify with `go build ./...` after adding the ignore entry.

</details>

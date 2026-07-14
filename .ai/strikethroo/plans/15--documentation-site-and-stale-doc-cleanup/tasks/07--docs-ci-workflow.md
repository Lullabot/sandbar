---
id: 7
group: "docs-infrastructure"
dependencies: [1]
status: "pending"
created: 2026-07-14
model: "sonnet"
effort: "medium"
skills:
  - github-actions
  - mkdocs
---
# GitHub Actions: docs deploy and PR strict build

## Objective

Add `.github/workflows/docs.yml`: publish the site with `mike` to the `gh-pages` branch on pushes to `main` and on release tags, and run `mkdocs build --strict` on pull requests so a broken link fails CI.

## Skills Required

`github-actions` (triggers, permissions, the `gh-pages`-branch publishing model); `mkdocs` (mike's deploy/alias semantics).

## Acceptance Criteria

- [ ] `.github/workflows/docs.yml` exists with three behaviours: a `pull_request` job running `mkdocs build --strict`; a `push` to `main` publishing the `main` version via mike; and a release-tag push publishing that version and moving the `latest` alias.
- [ ] The deploy job uses `permissions: contents: write`, `fetch-depth: 0`, and a `pages` concurrency group. It does **not** use `actions/deploy-pages`, `id-token`, or an `environment:` block — mike pushes to the branch itself.
- [ ] The tag trigger and version extraction match this repo's actual release tags (release-please, `release-type: go` — confirm the tag format in `release-please-config.json` and existing git tags before writing the filter).
- [ ] The workflow file parses as valid GitHub Actions YAML. Verify with `uvx --from check-jsonschema check-jsonschema --builtin-schema vendor.github-workflows .github/workflows/docs.yml` and paste the output; if that tool is unavailable, paste a successful `python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/docs.yml'))"` plus your own read-through of the triggers.
- [ ] The strict-build command in the workflow is the same one that passes locally — run it locally and paste the exit-0 output.
- [ ] Report the manual GitHub Pages setting the deploy depends on (Settings → Pages → Deploy from a branch → `gh-pages` / root) as an explicit follow-up; do not claim the site is live.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- `astral-sh/setup-uv` to provide `uv`/`uvx`; pin actions by SHA if the repo's other workflows do (check `.github/workflows/test.yml` and follow the local convention).
- The docs toolchain is pinned in `docs/requirements.txt` — invoke it as `uvx --with-requirements docs/requirements.txt <tool>` and do **not** additionally pin the version in the `uvx` invocation, so there is exactly one place to bump.

## Input Dependencies

Task 1: `mkdocs.yml` and `docs/requirements.txt` must exist for the workflow to run.

## Output Artifacts

`.github/workflows/docs.yml`.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

Modelled on `Lullabot/playwright-drupal`'s `.github/workflows/docs.yml`, with one deliberate addition: **they have no docs check on pull requests**, so a broken link reaches their `main`. Add that job.

The publishing model matters and is easy to get wrong. `mike` **commits and pushes to the `gh-pages` branch itself**. That is *not* the modern OIDC `actions/deploy-pages` flow. Consequences:

- `permissions: contents: write` — and nothing else. No `pages:`, no `id-token:`, no `environment:`.
- `fetch-depth: 0` on checkout, so mike can see and rewrite `gh-pages` history.
- A git identity must be configured in the job before mike commits.
- GitHub Pages must be pointed at the `gh-pages` branch in repository settings, once, by a human.

Shape (adapt names and the tag filter to this repo):

```yaml
name: Docs

on:
  push:
    branches: [main]
    tags: ['v*']
  pull_request:
  workflow_dispatch:

permissions:
  contents: write

concurrency:
  group: "pages"
  cancel-in-progress: false

jobs:
  build:
    if: github.event_name == 'pull_request'
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@<sha>
      - uses: astral-sh/setup-uv@<sha>
      - run: uvx --with-requirements docs/requirements.txt mkdocs build --strict

  deploy:
    if: github.event_name != 'pull_request'
    runs-on: ubuntu-24.04
    timeout-minutes: 10
    steps:
      - uses: actions/checkout@<sha>
        with:
          fetch-depth: 0
      - uses: astral-sh/setup-uv@<sha>
      - name: Configure git identity
        run: |
          git config user.name "github-actions[bot]"
          git config user.email "github-actions[bot]@users.noreply.github.com"
      - name: Deploy main docs
        if: github.ref == 'refs/heads/main'
        run: uvx --with-requirements docs/requirements.txt mike deploy --push main
      - name: Extract version from tag
        id: version
        if: startsWith(github.ref, 'refs/tags/v')
        run: echo "version=${GITHUB_REF_NAME#v}" >> "$GITHUB_OUTPUT"
      - name: Deploy release docs
        if: startsWith(github.ref, 'refs/tags/v')
        run: uvx --with-requirements docs/requirements.txt mike deploy --push --update-aliases "${{ steps.version.outputs.version }}" latest
      - name: Set default version
        if: startsWith(github.ref, 'refs/tags/v')
        run: uvx --with-requirements docs/requirements.txt mike set-default --push latest
```

**Verify the tag format first.** The reference repo tags `playwright-drupal-1.8.0` because its release-please config sets `include-v-in-tag: false`. This repo uses `release-type: go`. Run `git tag --list | tail` and read `release-release-config.json` / `release-please-config.json` to see what the tags actually look like, and write the filter and the version-extraction to match. Do not copy `v*` on faith.

**Action pinning.** Check how the existing workflows in `.github/workflows/` reference actions — if they pin by commit SHA with a `# vN` comment, match that convention exactly. Renovate is configured in this repo and will keep them current.

You cannot fully end-to-end test a deploy from here, and you should not pretend to. What you *can* verify, and must: the YAML parses, the strict build command succeeds locally, and the trigger/permission structure is right. Report the Pages setting as an outstanding manual step.
</details>

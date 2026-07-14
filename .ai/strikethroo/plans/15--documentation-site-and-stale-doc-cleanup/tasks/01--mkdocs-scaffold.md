---
id: 1
group: "docs-infrastructure"
dependencies: []
status: "completed"
created: 2026-07-14
model: "sonnet"
effort: "medium"
skills:
  - mkdocs
  - technical-writing
---
# MkDocs Material scaffold, nav, and stub pages

## Objective

Stand up the MkDocs + Material + mike toolchain at the repository root — `mkdocs.yml`, `docs/requirements.txt`, a home page, and a stub page for every entry in the nav tree — so that `mkdocs build --strict` passes from the very first commit and the content tasks have real files to fill in.

## Skills Required

`mkdocs` (config, nav, Material theme, strict builds) and `technical-writing` (the home page and stub headings).

## Acceptance Criteria

- [ ] `mkdocs.yml` exists at the repository root with `site_name`, `site_url`, `repo_url`, `docs_dir: docs`, `site_dir: site`, the Material theme, and an explicit `nav:` tree matching the Information Architecture below.
- [ ] `docs/requirements.txt` pins `mkdocs`, `mkdocs-material`, and `mike` to exact versions.
- [ ] Every file named in `nav:` exists under `docs/` with at least an H1 and a one-line description of what will live there.
- [ ] `.gitignore` contains `site/`.
- [ ] Running `uvx --with-requirements docs/requirements.txt mkdocs build --strict` from the repo root exits 0 and prints **no** `WARNING` lines. Paste the full command output into your completion report.
- [ ] `site/index.html` exists after that build, and `site/` is not tracked by git (`git status --porcelain site/` prints nothing).

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- MkDocs 1.6.x, mkdocs-material 9.x, mike 2.x, pinned exactly in `docs/requirements.txt`.
- The toolchain is invoked through `uvx --with-requirements docs/requirements.txt <tool>` — nothing is installed globally, and **no Node/npm toolchain is introduced** (this repo has no `package.json` and must not gain one).
- Repository: `github.com/lullabot/sandbar`; binary `sand`; published site URL `https://lullabot.github.io/sandbar/`.

## Input Dependencies

None. This is the first task.

## Output Artifacts

- `mkdocs.yml`
- `docs/requirements.txt`
- `docs/index.md` plus one stub per nav entry
- `.gitignore` updated with `site/`

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

This is modelled directly on `Lullabot/playwright-drupal`, which uses MkDocs + Material + mike driven through `uvx`. Follow that shape.

**1. `docs/requirements.txt`** — exact pins, one per line:

```
mkdocs==1.6.1
mkdocs-material==9.7.6
mike==2.2.0
```

(Check for newer patch releases if you like, but pin exactly. This is a standard pip-requirements file so the repo's existing Renovate config will manage it without changes.)

**2. `mkdocs.yml`** at the repository root:

```yaml
site_name: sandbar
site_url: https://lullabot.github.io/sandbar/
repo_url: https://github.com/lullabot/sandbar
site_description: Disposable Claude Code development VMs on Lima.
docs_dir: docs
site_dir: site

theme:
  name: material
  palette:
    - media: "(prefers-color-scheme: light)"
      scheme: default
      toggle:
        icon: material/brightness-7
        name: Switch to dark mode
    - media: "(prefers-color-scheme: dark)"
      scheme: slate
      toggle:
        icon: material/brightness-4
        name: Switch to light mode
  features:
    - navigation.tabs
    - navigation.top
    - content.code.copy

markdown_extensions:
  - admonition
  - attr_list
  - md_in_html
  - pymdownx.highlight:
      anchor_linenums: true
  - pymdownx.inlinehilite
  - pymdownx.snippets
  - pymdownx.superfences:
      custom_fences:
        - name: mermaid
          class: mermaid
          format: !!python/name:pymdownx.superfences.fence_code_format

extra:
  version:
    provider: mike
    alias: true

nav:
  - Home: index.md
  - Getting Started:
    - About sand: getting-started/index.md
    - Installation: getting-started/installation.md
    - Your First VM: getting-started/first-vm.md
    - How Provisioning Works: getting-started/how-it-works.md
  - Using sand:
    - The Board (TUI): using-sand/tui.md
    - CLI Reference: using-sand/cli-reference.md
    - Secrets: using-sand/secrets.md
    - Files and Shells: using-sand/files-and-shells.md
  - Reference:
    - Files and State: reference/files-and-state.md
    - Security Model: reference/security-model.md
    - Troubleshooting: reference/troubleshooting.md
  - Contributing:
    - Development: contributing/development.md
    - The Embedded Playbook: contributing/ansible-playbook.md
    - Releases: contributing/releases.md
---
```

(Do not leave a trailing `---` in the YAML — that is just the end of this snippet.)

Notes on the config, and why it differs from the reference where it does:
- `repo_url` **is** set here; the reference omits it and consequently has no repo link in its header.
- No `plugins:` block is needed — Material's built-in client-side search is active by default and is sufficient. Adding an explicit `plugins:` block would *disable* that default unless you also list `search`.
- The `extra.version.provider: mike` block is what renders the version dropdown once mike has published at least one version. It is harmless before then.
- Do **not** add a `custom_dir` theme override, a hero template, or brand CSS. The reference has those; reproducing another organisation's branding is out of scope. Keep the theme stock.

**3. Stub pages.** Create every file named in `nav:`. Each stub is an H1 matching its nav label plus a single sentence naming what will be documented there — for example `docs/using-sand/cli-reference.md`:

```markdown
# CLI Reference

Every `sand` command and every flag, with its real default.
```

`docs/index.md` is the one page you should write properly rather than stub: a short home page saying what `sand` is (a single Go binary that provisions disposable Claude Code development VMs on Lima), a two-or-three-bullet pitch, and links into Getting Started. Keep it to one screen. Do **not** use a `template:` front-matter override — plain markdown.

**4. `.gitignore`** — append `site/` (MkDocs' build output). Check whether the entry already exists before adding.

**5. Verify.** Run:

```
uvx --with-requirements docs/requirements.txt mkdocs build --strict
```

`--strict` turns warnings into errors, which is the entire quality gate for this toolchain — a nav entry pointing at a missing file, or a broken internal link, fails the build. It must exit 0 with a clean log before you report done. If `uvx` is not on the machine, install `uv` (`curl -LsSf https://astral.sh/uv/install.sh | sh`) rather than falling back to a global pip install.
</details>

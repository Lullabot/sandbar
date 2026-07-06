---
id: 2
group: "documentation"
dependencies: []
status: "completed"
created: "2026-03-09"
skills:
  - markdown
---
# Update README with direnv Usage and Fine-Grained Token Recommendations

## Objective
Add documentation to README.md explaining how to use direnv for per-directory `GH_TOKEN` environment variables, and recommend creating fine-grained GitHub PATs with limited permissions.

## Skills Required
- markdown: Writing clear technical documentation with code examples

## Acceptance Criteria
- [ ] README has a new subsection under "GitHub Authentication" explaining per-directory GitHub tokens with direnv
- [ ] The direnv section includes a concrete example showing `.envrc` creation, `direnv allow`, and how it overrides the default `gh auth` credential
- [ ] README has a new subsection recommending fine-grained PATs with guidance on minimal permission scoping
- [ ] The fine-grained PAT section explains advantages over classic PATs and lists recommended permissions
- [ ] The direnv section recommends adding `.envrc` to `.gitignore`

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- New sections go under the existing "GitHub Authentication" heading in README.md, after "Changing the token after deployment"
- `GH_TOKEN` takes precedence over `gh auth` stored credentials — document this behavior
- direnv requires `direnv allow` after creating/modifying `.envrc` — explain this as a security feature

## Input Dependencies
None — this task modifies README.md independently.

## Output Artifacts
- Modified `README.md` with two new subsections under "GitHub Authentication"

## Implementation Notes

<details>
<summary>Detailed implementation steps</summary>

Edit `/home/andrew.linux/claude-code-ansible/README.md`. Add two new subsections after the "Changing the token after deployment" subsection (which ends around line 92) and before the "## Security Model" section.

**Subsection 1: "Per-directory GitHub tokens with direnv"**

Content should cover:
- direnv is installed on the VM and hooked into bash
- To use a different GitHub token for a project directory, create a `.envrc` file:
  ```bash
  # ~/project-a/.envrc
  export GH_TOKEN=github_pat_xxxx
  ```
- Run `direnv allow` to approve the file (explain this is a security feature — direnv won't load unapproved files)
- When you `cd` into the directory, `GH_TOKEN` is set automatically; when you leave, it's unloaded
- `GH_TOKEN` takes precedence over the `gh auth` stored credential, so `gh` and `git` operations use the directory-specific token
- Recommend adding `.envrc` to `.gitignore` in project repos to avoid committing tokens
- Example with two directories:
  ```
  ~/client-a/.envrc  →  export GH_TOKEN=github_pat_aaa...
  ~/client-b/.envrc  →  export GH_TOKEN=github_pat_bbb...
  ```

**Subsection 2: "Recommended: Fine-grained Personal Access Tokens"**

Content should cover:
- Fine-grained PATs (as opposed to classic PATs) are recommended for use with this system
- Advantages: scoped to specific repositories, granular permissions, mandatory expiration dates
- Recommend creating separate tokens per project or client
- List commonly needed permissions:
  - `Contents: Read and write` — push and pull code
  - `Pull requests: Read and write` — create and manage PRs
  - `Issues: Read and write` — create and manage issues (if needed)
  - `Metadata: Read-only` — always required (automatically included)
- Navigate to: Settings → Developer settings → Personal access tokens → Fine-grained tokens
- Pair with direnv: create a fine-grained token per project directory, each scoped to only the repos needed

Also update the `user_github_pat` row in the "Configurable Variables" table to add a note that fine-grained PATs are recommended.

</details>

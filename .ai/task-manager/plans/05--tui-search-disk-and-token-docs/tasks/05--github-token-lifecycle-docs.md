---
id: 5
group: "documentation"
dependencies: [1, 3]
status: "completed"
created: "2026-07-03"
skills:
  - "technical-writing"
---
# Consolidated GitHub-token lifecycle docs + TUI README updates

## Objective
Replace the scattered GitHub-token fragments with one end-to-end lifecycle walkthrough in `README.md`, and update `tui/README.md` to document the new `/` search keybinding and the `Max Disk` / `Disk Used` columns while cross-referencing (not restating) the token walkthrough. The walkthrough must thread a single token through its whole life: create a fine-grained PAT → supply at VM-create → lands in the per-org `.env` as `GH_TOKEN` → direnv loads it → precedence over `gh auth login` → rotation/expiry → revoke → reset does not carry the token (re-supply unless *Preserve project .env + checkout*).

## Skills Required
- `technical-writing`: reorganizing existing Markdown into a coherent narrative and cross-referencing between two README files, keeping every step faithful to the actual behaviour.

## Acceptance Criteria
- [ ] `README.md`'s "GitHub Authentication" section is reworked into a single end-to-end token-lifecycle walkthrough covering, in order: create a fine-grained PAT (retaining the existing recommended-scopes table) → supply it at create time (TUI form field / `new-vm.sh --clone-token` / `project_clone_token` var) → it is written to the per-org `.env` as `GH_TOKEN` → direnv (`load_dotenv = true`) loads it on `cd` → `GH_TOKEN` takes precedence over `gh auth login` → rotation/expiry → revoke → a **reset does not carry the token** (never stored), so re-supply it unless *Preserve project .env + checkout* is enabled.
- [ ] The existing "separate VM per org/context" guidance is folded in as the multi-token recommendation (not left as a stray paragraph).
- [ ] A one-line caveat is added where disk sizing is discussed: `Disk Used` = allocated blocks (`st_blocks × 512`); on APFS it reflects shared-block accounting and may differ for blocks shared with a clone source.
- [ ] `tui/README.md`'s List-view keybindings table gains a `/` row (`/` — incremental name search; `esc` clears/exits, `enter` keeps the filter), placed alongside the existing `f` filter row.
- [ ] `tui/README.md`'s list/detail column descriptions are updated from `Disk` to `Max Disk` + `Disk Used` (and detail `Maximum Disk Size` + `Disk Used (allocated)`), consistent with the disk-sizing note already in that file.
- [ ] `tui/README.md`'s token/reset discussion points at the `README.md` walkthrough as the canonical explanation instead of restating the precedence/"token not stored" rules.
- [ ] No behavioural claim contradicts the code: `GH_TOKEN` precedence, `load_dotenv = true`, and the "clone token is never stored → re-supply on reset unless Preserve project .env + checkout" rule are all stated exactly as implemented.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Files touched: `README.md` and `tui/README.md` only. No code changes.
- Source of truth for the token flow: `README.md` lines ~200-241 (existing "GitHub Authentication"), the `project_clone_token` variable row (~line 272), the Security Model preserve note (~line 250), and `tui/README.md`'s Reset section (~lines 134-142) and Managed-VMs section (~lines 183-190).
- Keep the existing fine-grained PAT scope table (README ~lines 228-235) intact within the new walkthrough.

## Input Dependencies
- Task 1: the `/` search binding and its `esc`/`enter` semantics (documented in the keybindings table).
- Task 3: the `Max Disk` / `Disk Used` (list) and `Maximum Disk Size` / `Disk Used (allocated)` (detail) labels (documented in the column descriptions and the disk caveat).

## Output Artifacts
- Updated `README.md` and `tui/README.md` presenting one canonical token walkthrough plus accurate search/disk documentation.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**`README.md` — rework "GitHub Authentication" (lines ~200-241) into a lifecycle walkthrough.**

Keep it a single flowing section (a numbered walkthrough works well). Thread these steps, each already true in the codebase:
1. **Create** a fine-grained PAT at *Settings > Developer settings > Personal access tokens > Fine-grained tokens* with the recommended scopes — retain the existing bullet list of advantages and the permissions table verbatim (Contents R/W, Pull requests **Read**, Issues **Read**, Actions R/W, Workflows R/W, Metadata read-only), including the note that PRs/Issues are deliberately read-only so an agent can't self-merge.
2. **Supply** the token at VM-create time: the TUI create-form `GitHub token` field, or `new-vm.sh --clone-token`, or the `project_clone_token` playbook var. It is only used to clone a private repo into the VM.
3. **Where it lands:** for `github.com` URLs it is written to the per-org `.env` as `GH_TOKEN` (treat as a secret).
4. **How it loads:** direnv is installed with `load_dotenv = true`, so the `GH_TOKEN=...` line loads when you `cd` into that directory and unloads when you leave.
5. **Precedence:** `GH_TOKEN` takes precedence over any token stored by `gh auth login`; `gh` is the git credential helper, so `git push`/`pull` over HTTPS use whatever token is in the environment.
6. **Multiple orgs:** use a separate VM per org/context (fold in the current paragraph) rather than juggling several tokens on one host — VMs are disposable and this keeps each context's credentials and code isolated.
7. **Rotate / expire / revoke:** fine-grained PATs must have an expiry; when it expires or you rotate, update the `.env` `GH_TOKEN` (or re-supply on the next create), and revoke the old token in GitHub settings.
8. **Reset:** a reset/recreate does **not** carry the token — it is never stored in the managed-VM index — so a private-repo VM must have the token re-supplied on reset **unless** *Preserve project .env + checkout* is enabled (which keeps the existing `.env`, so no re-supply is needed).

Then, where disk sizing is discussed (or in the TUI/create-form disk note), add the one-line caveat:
> `Disk Used` reports allocated blocks (`st_blocks × 512`), so it can sit far below the maximum (qcow2 is sparse); on APFS it reflects shared-block accounting and may differ for blocks shared with a clone source.

**`tui/README.md` — keybindings, columns, and cross-reference.**
- In the **List view** keybindings table (lines ~45-56), add a row near the `f` row:
  | `/` | Incremental name search — type to filter the list by name; `esc` clears and exits, `enter` keeps the filter |
- Update any place that names the `Disk` column so the list reads `Max Disk` + `Disk Used` and the detail reads `Maximum Disk Size` + `Disk Used (allocated)`. Reconcile with the existing "Disk sizing" note (~line 149) so the max-vs-used distinction is clear (max can grow from the `20GiB` floor; used is the real allocated blocks).
- In the Reset section (~lines 134-142) and Managed-VMs section (~lines 183-190), replace any restated precedence/"token not stored" explanation with a cross-reference to the README walkthrough, e.g. "see [GitHub Authentication](../README.md#github-authentication)", keeping only the reset-specific fact that *Preserve project .env + checkout* avoids re-supplying the token.

**Consistency check:** re-read the final prose against `README.md`'s `project_clone_token` row and the Security Model preserve bullet to ensure nothing drifts from the implemented behaviour.
</details>

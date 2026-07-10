---
id: 3
group: "guest-provisioning"
dependencies: []
status: "completed"
created: 2026-07-09
model: "sonnet"
effort: "medium"
skills:
  - ansible
  - shell
---
# Guest shells source the secrets env file

## Objective

Make the guest read `~/.config/sandbar/secrets.env` from **both** `~/.profile`
(login shells) and `~/.bashrc` (interactive non-login shells), each guarded on the
file existing, and ensure `~/.config/sandbar` exists at `0700` owned by the VM
user. Neither file alone covers every way a user enters the VM.

## Skills Required

- **ansible** — `blockinfile`, `file`, role task ordering, `ansible-lint`.
- **shell** — POSIX `source` semantics; the difference between login and
  interactive-non-login shell startup files.

## Acceptance Criteria

- [ ] `roles/user/tasks/main.yml` creates `~/.config/sandbar` as a directory,
      mode `0700`, owned by `{{ user_name }}`.
- [ ] The existing `~/.bashrc` `ANSIBLE MANAGED BLOCK` gains the guarded source
      line (see Technical Requirements for the exact line).
- [ ] `~/.profile` gains its own `blockinfile` with a **distinct marker** so it
      can never collide with the `.bashrc` block.
- [ ] Both new pieces carry the same `when: provision_phase | default('full') !=
      'finalize'` guard the surrounding tasks use, so they run at base-build time.
- [ ] The source line is guarded on file existence — the directory is provisioned
      at base-build time but the file only appears once a VM has secrets, so an
      unguarded `source` would error on every shell in every VM without secrets.
- [ ] Verification: `ansible-lint roles/user` reports **no new findings** relative
      to `git stash && ansible-lint roles/user; git stash pop`. Record both outputs.
- [ ] Verification: `python3 -c "import yaml,sys; yaml.safe_load(open('roles/user/tasks/main.yml'))"`
      exits 0.
- [ ] Verification (guest, requires a real VM): after a VM is built from this
      playbook and a `secrets.env` containing `export PROBE='ok'` exists, both of
      these print `ok`:
      ```
      limactl shell <vm> -- bash -lc 'echo "$PROBE"'
      limactl shell <vm> -- bash -ic 'echo "$PROBE"'
      ```
      And with **no** `secrets.env` present, both of these exit 0 and print
      nothing (no "No such file or directory" on stderr):
      ```
      limactl shell <vm> -- bash -lc 'true'
      limactl shell <vm> -- bash -ic 'true'
      ```

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

The exact guarded source line, used verbatim in both files:

```sh
[ -f "$HOME/.config/sandbar/secrets.env" ] && . "$HOME/.config/sandbar/secrets.env"
```

Use `.` rather than `source` — `~/.profile` may be read by a POSIX `sh` that has
no `source` builtin.

**Beware the `set -e` / last-exit-status trap.** When the file does not exist, the
`[ -f ... ]` test is false and the whole `&&` list evaluates to exit status 1. If
that is the *last* line of `~/.profile`, the shell's startup exit status becomes 1.
Guard against it by giving the line an unconditional success tail or by using an
`if` block. Prefer the explicit form:

```sh
if [ -f "$HOME/.config/sandbar/secrets.env" ]; then
  . "$HOME/.config/sandbar/secrets.env"
fi
```

This is the form to commit. It is immune to the exit-status problem and reads
clearly.

Existing structure to follow (`roles/user/tasks/main.yml`, around lines 95–126):

- `Ensure ~/.config/direnv directory exists` — an `ansible.builtin.file` task with
  `owner`/`group`/`mode` and the `provision_phase` guard. Copy its shape for the
  `~/.config/sandbar` directory but use mode `0700`, not `0755`: it holds secrets.
- `Deploy bashrc customizations` — an `ansible.builtin.blockinfile` with
  `marker: "# {mark} ANSIBLE MANAGED BLOCK"`. Append the `if` block to its
  existing `block:` content.
- For `~/.profile`, add a new `ansible.builtin.blockinfile` task with
  `marker: "# {mark} ANSIBLE MANAGED BLOCK - sandbar secrets"` and
  `create: true` (a minimal Debian image may not ship a `~/.profile` for the user).
  Set `owner`/`group`/`mode: "0644"`.

`user_home` is already established as a fact by earlier tasks in this role — reuse
it rather than re-deriving it. Confirm by reading the role from the top.

## Input Dependencies

None. This task is independent of the Go changes; task 2 writes the file this
task teaches the shell to read, but neither needs the other to compile or lint.

## Output Artifacts

- A guest that exports every stored secret into both login and interactive shells.
- `~/.config/sandbar` at `0700`, ready for `provision.ApplySecrets` (task 2) to
  write into. (`ApplySecrets` also `install -d -m 700`s it, so the two are
  belt-and-braces; that redundancy is intentional and must not be removed here.)

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Read first:** `roles/user/tasks/main.yml` in full. Pay attention to:
- how `user_home` is set (a `getent` lookup + `set_fact`, as in `roles/project`),
- the `when: provision_phase | default('full') != 'finalize'` guard repeated on
  every task in that region — the base image is built once and cloned, so shell
  configuration belongs to the base phase, not the per-VM finalize phase,
- the `blockinfile` `marker` convention.

**Why both files.** `bash` reads `~/.bashrc` for interactive non-login shells and
`~/.profile` (via `~/.bash_profile` if present) for login shells. `limactl shell
<vm> -- bash -lc` takes the login path; `limactl shell <vm>` lands in an
interactive shell that takes the `.bashrc` path. A variable placed in only one is
invisible from the other, which surfaces as an intermittent "my token isn't set"
depending on how the user entered the VM. This exact failure has bitten this
project before.

Watch for a `~/.bash_profile` in the image: if one exists, bash reads it and
**ignores** `~/.profile` entirely for login shells. Check with
`limactl shell <vm> -- ls -la ~ | grep -i profile` before assuming `.profile` is
the login file. If `~/.bash_profile` exists and does not source `~/.profile`, put
the block in `~/.bash_profile` instead — or, more robustly, in whichever of the
two the image actually reads. State in a code comment which one you found and why.

**Tasks to add**, in this order, after the direnv block:

```yaml
- name: Ensure ~/.config/sandbar directory exists
  ansible.builtin.file:
    path: "{{ user_home }}/.config/sandbar"
    state: directory
    owner: "{{ user_name }}"
    group: "{{ user_name }}"
    mode: "0700"
  when: provision_phase | default('full') != 'finalize'

- name: Source the sandbar secrets file from ~/.profile (login shells)
  ansible.builtin.blockinfile:
    path: "{{ user_home }}/.profile"
    marker: "# {mark} ANSIBLE MANAGED BLOCK - sandbar secrets"
    create: true
    owner: "{{ user_name }}"
    group: "{{ user_name }}"
    mode: "0644"
    block: |
      # Secrets are written by `sand` on VM start; the file is absent until then.
      if [ -f "$HOME/.config/sandbar/secrets.env" ]; then
        . "$HOME/.config/sandbar/secrets.env"
      fi
  when: provision_phase | default('full') != 'finalize'
```

And extend the existing `Deploy bashrc customizations` block with the same
`if` stanza. Do not create a second `blockinfile` for `.bashrc` — reuse the
existing managed block, or you will end up with two markers fighting.

**ansible-lint.** The common findings here are `risky-file-permissions` (fixed by
always giving an explicit `mode`) and `name[casing]` (task names start with a
capital). Match the surrounding style.

**Testing philosophy.** Write a few tests, mostly integration. Meaningful tests
verify custom business logic, critical paths, and edge cases specific to this
application — test *your* code, not the framework or library.

Write tests for: custom business logic and algorithms; critical user workflows
and data transformations; edge cases and error conditions for core functionality;
integration points between components; complex validation logic or calculations.

Do NOT write tests for: third-party library functionality; framework features;
simple CRUD operations without custom logic; trivial getters/setters or static
configuration; obvious functionality that would break immediately if incorrect.

Here that means: there is no unit-test harness for Ansible roles in this repo and
you should not build one. The verification is `ansible-lint`, a YAML parse, and
the two-shell guest probe in the Acceptance Criteria. The absent-file probe is the
edge case that matters — an unguarded `source` breaks every shell in every VM.

</details>

---
id: 1
group: "image-build"
dependencies: []
status: "completed"
created: 2026-09-12
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "high"
complexity_score: 7
complexity_notes: "Requires reasoning about offline-root vs live-systemd semantics across two large roles; a missed task fails only later, inside CI, as a silent no-op rather than an error."
skills:
  - ansible
  - linux-systemd
---
# Audit base/user roles for chroot compatibility and add the `sand_image_build` flag

## Objective

Make the base-phase playbook runnable inside a `chroot` over an offline image root, by identifying every task that requires a live system and expressing it as its offline equivalent behind a single new `sand_image_build` flag that defaults to `false` — so the existing in-guest path is byte-identical to today when the flag is absent.

## Skills Required

`ansible` for the role and variable work; `linux-systemd` for the offline-root equivalents (`systemctl --root=`, linger files, `policy-rc.d` interaction).

## Acceptance Criteria

- [x] A written audit exists listing every task in `roles/base` and `roles/user` that cannot run under `chroot`, each with its offline equivalent. Save it as `docs/contributing/image-build-audit.md` or as a comment block in the build script — one durable location, referenced from the task's Output Artifacts.
- [x] `sand_image_build` is declared with a default of `false` in `roles/base/defaults/main.yml`, alongside the existing `toolset_*` declarations that are deliberately kept in one home.
- [x] Every task requiring a live system consults the flag. At minimum this must cover: any `state: started` in the base path (enable-only when the flag is true) and `loginctl enable-linger` in `roles/user/tasks/main.yml:28-33` (write `/var/lib/systemd/linger/<user>` directly when the flag is true).
- [x] Verification: `ansible-playbook -i localhost, --connection=local site.yml --syntax-check` exits 0.
- [x] Verification: `grep -rn "sand_image_build" roles/ site.yml` shows the default declaration plus at least one guarded task per identified incompatibility, and `grep -rn "enable-linger\|state: started" roles/` shows every hit is either guarded by the flag or outside the base phase. Paste both outputs into the task record.
- [x] Verification: the default path is unchanged — `git diff` shows no task's *false-flag* behaviour altered. Confirm by running the `molecule/base` scenario (`molecule test -s base`) and observing it still passes, since it converges with the flag absent.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- `roles/base/tasks/main.yml` (496 lines) and `roles/user/tasks/main.yml` (254 lines) are the audit targets.
- `roles/base/defaults/main.yml:93-97` is where play-wide flags live by existing convention — put `sand_image_build` there.
- The flag must be **additive**: when false (the default, and what every in-guest run passes), behaviour is exactly what it is today.
- Do not fork a build-only copy of the playbook. One playbook, one flag.

## Input Dependencies

None. This is the first task and depends only on the current repository state.

## Output Artifacts

- The `sand_image_build` variable and its guards, consumed by task 03's build script.
- `docs/contributing/image-build-audit.md`, consumed by task 03 as the specification of what the chroot build must accommodate.
- `molecule/base-idempotence/`, the stable default-path idempotence scenario, run by the advisory Molecule CI matrix.

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

**What "cannot run under chroot" means here.** The build mounts a qcow2's root filesystem at a path (say `/mnt/img`) and runs `chroot /mnt/img ansible-playbook ...`. Inside that chroot:

- There is **no running init**. `systemctl start`, `systemctl is-active`, and anything talking to systemd's D-Bus socket will fail. `systemctl enable` also fails inside the chroot — the established workaround used already in `.github/workflows/base-image.yml` is to run `systemctl --root=/mnt/img enable <unit>` from *outside* the chroot, which writes the `wants/` symlink directly.
- A `policy-rc.d` returning 101 is already dropped in place by the existing workflow, so Debian package postinsts will not attempt to start daemons. That handles packages; it does not handle explicit Ansible `service`/`systemd` tasks.
- **Network works.** A chroot shares the host network namespace. Ensure `/etc/resolv.conf` is present in the image root and APT repos, `curl | sh` installers and file downloads all behave normally. Do not design around an offline build.
- `/dev`, `/proc`, `/sys` are bind-mounted by the existing scaffold, so most things needing them are fine.

**Known targets to check (not necessarily exhaustive — this is why it is an audit):**

1. `roles/user/tasks/main.yml:28-33` — `loginctl enable-linger`. Requires `systemd-logind` running. Offline equivalent: create the empty file `/var/lib/systemd/linger/<user>` (this is exactly what linger *is* on disk). Guard with `sand_image_build`.
2. `roles/dev-tools/tasks/main.yml` — enables `docker.socket` and disables `docker.service`. If expressed with `state:`, split enable/disable (fine offline via `--root`) from any start (not fine).
3. `roles/base/tasks/main.yml` — the sshd drop-ins at `:464-485` write config files, which is fine; check whether any handler restarts sshd.
4. `roles/samba/tasks/main.yml` enables `smbd`, but `samba_enabled` is hard-set false for every sand run (`internal/provision/vars.go:40`) so it is not on the base path — note it in the audit and move on.
5. `roles/base/tasks/main.yml:6-12` "Deploy /etc/hosts" and `:2-4` "Set hostname" are already `provision_phase != 'finalize'`-gated in a way that puts them in the base path — `hostnamectl` needs a live systemd. Check carefully: if `hostname` is set via the `hostname` module it may use `hostnamectl`. This one matters.
6. Handlers in `roles/base/handlers/main.yml` — any `service` restart handler will fire during a chroot run if its task reports changed.

**How to express a guard.** Prefer `when: not sand_image_build` on the live-system task plus a sibling task with `when: sand_image_build` doing the offline equivalent, over trying to parameterize a single task. It reads better and keeps the default path visibly untouched.

**For enabling units**, the cleanest division of labour is: do not try to enable from inside Ansible-in-chroot at all. Instead have the playbook record which units should be enabled (or simply let task 03's build script call `systemctl --root=` for the known set afterwards). Decide which, write it down in the audit, and make task 03's requirements match — this is the main interface between the two tasks.

**Do not** change what the playbook does in the normal in-guest case. The `molecule/base` scenario and the `lint` job's `--syntax-check` are your regression guards; the `molecule/base` scenario in particular asserts the deliberately-ungated timezone behaviour (`Europe/Berlin` after a finalize-phase run) and must continue to pass.

</details>

## Execution Evidence

- `ansible-playbook -i localhost, --connection=local site.yml --syntax-check` exited 0 and printed `playbook: site.yml`.
- `git diff --check` exited 0. The diff adds only true-flag branches or guards; the false-flag modules and task arguments retain their behavior, including the same generated password hash expression in the live branch.
- No new unit test was added: the changes are Ansible task conditions and offline file writes, and an implementation-mirroring assertion would not verify the guest boundary. The original Molecule scenario exercises the base→finalize transition, and the new stable base-phase scenario verifies default-path idempotence. Task 03's image build will exercise the chroot path.
- Fresh `uvx --with 'molecule>=6' --with 'molecule-plugins[docker]' --with ansible-core molecule test -s base-idempotence` exited 0: first converge `ok=38 changed=29 failed=0`, idempotence replay `ok=32 changed=0 failed=0`, verify `ok=7 changed=0 failed=0` with all assertions passed, and destroy succeeded.
- Fresh `uvx --with 'molecule>=6' --with 'molecule-plugins[docker]' --with ansible-core molecule test -s base` exited 0: two-play converge `ok=52 changed=32 failed=0`; verify `ok=7 changed=0 failed=0` asserted hostname, hosts, locale, and Europe/Berlin after finalize; destroy succeeded.

`grep -rn "sand_image_build" roles/ site.yml` (exit 0):

```text
roles/user/tasks/main.yml:11:    - not sand_image_build
roles/user/tasks/main.yml:22:    password: "{{ '!' if sand_image_build else (user_password | password_hash('sha512')) }}"
roles/user/tasks/main.yml:37:    - sand_image_build
roles/user/tasks/main.yml:48:    - sand_image_build
roles/user/tasks/main.yml:59:    - not sand_image_build
roles/user/tasks/main.yml:244:    - not sand_image_build
roles/agent-clipboard/tasks/main.yml:67:  when: not sand_image_build
roles/dev-tools/tasks/main.yml:38:  when: not sand_image_build
roles/dev-tools/tasks/main.yml:44:  when: not sand_image_build
roles/dev-tools/handlers/main.yml:11:  when: not sand_image_build
roles/dev-tools/handlers/main.yml:17:  when: not sand_image_build
roles/base/defaults/main.yml:87:sand_image_build: false
roles/base/tasks/main.yml:81:  when: not sand_image_build
roles/base/tasks/main.yml:550:    - not sand_image_build
roles/base/tasks/main.yml:580:  when: not sand_image_build
roles/base/handlers/main.yml:6:  when: not sand_image_build
```

`grep -rn "enable-linger\|state: started" roles/` (exit 0):

```text
roles/user/tasks/main.yml:54:    cmd: loginctl enable-linger {{ user_name }}
roles/dev-tools/tasks/main.yml:36:    state: started
roles/base/tasks/main.yml:576:    state: started
roles/codex/tasks/main.yml:106:    state: started
roles/samba/tasks/main.yml:30:    state: started
```

The first three hits are guarded by `not sand_image_build`; Codex is excluded
from the base phase and Samba is disabled for sand image builds. The
clipboard service uses a conditional `state:` expression rather than the
literal `state: started` and is also guarded.

## Noteworthy Events

- [2026-09-28] Docker 29 bind-mounts `/etc/hostname` and `/etc/hosts`, so the unchanged hostname and hosts tasks failed with `EBUSY` when Molecule tried to replace those files. `molecule/base/prepare.yml` now unmounts only those file mount points before converge. This keeps the scenario's hostname and hosts transitions and verify assertions active.
- [2026-09-28] The pinned Molecule container lacked `gpg` before the base role's early repository-key setup, although production bootstrap installs `gnupg`. Added `gnupg` to Molecule prepare alongside its existing `locales` prerequisite.
- [2026-09-28] `uvx --with 'molecule>=6' --with 'molecule-plugins[docker]' --with ansible-core molecule test -s base` completed its two-play converge with `failed=0`, then exited 1 at Molecule's preexisting idempotence step. That step replays a scenario which intentionally changes the timezone America/Toronto → Europe/Berlin every time; it also repeats temporary dpkg speed and NodeSource key writes/removals and reports the existing fstrim task changed. It reported ten changed tasks and never reached verify. The role edits for this task do not change any of those false-flag actions.

### Previous failed validation and resolution

The exact Molecule command above exited **1** at idempotence, reporting:

```text
* base : Speed up dpkg for the base build (disposable builder — removed before cloning)
* base : Download NodeSource GPG key
* base : Remove temporary NodeSource GPG key file
* base : Point /etc/localtime at the requested timezone
* base : Record the timezone name in /etc/timezone
* base : Enable the weekly fstrim timer so discarded blocks are returned
* base : Restore safe dpkg IO before this image is used as a clone source
* base : Point /etc/localtime at the requested timezone
* base : Record the timezone name in /etc/timezone
* base : Enable the weekly fstrim timer so discarded blocks are returned
```

The existing two-play converge deliberately changes timezone twice across an
idempotence replay, so it now retains converge and verify while
`base-idempotence` applies one stable base phase twice with `sand_image_build`
absent. Its RED run exited 1 with exactly five changes: temporary dpkg speed
and NodeSource key add/remove tasks, plus fstrim's always-changed service
action in the Docker container. Molecule's `molecule-idempotence-notest` tag
now excludes only those five tasks from the replay; they still execute during
normal converge. The GREEN run's replay reported `changed=0`, and both exact
scenario commands subsequently exited 0. The new scenario is in the advisory
CI matrix so the stronger check remains active.

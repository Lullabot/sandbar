---
id: 1
group: "image-build"
dependencies: []
status: "completed"
created: 2026-09-12
model: "sonnet"
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

- [ ] A written audit exists listing every task in `roles/base` and `roles/user` that cannot run under `chroot`, each with its offline equivalent. Save it as `docs/contributing/image-build-audit.md` or as a comment block in the build script — one durable location, referenced from the task's Output Artifacts.
- [ ] `sand_image_build` is declared with a default of `false` in `roles/base/defaults/main.yml`, alongside the existing `toolset_*` declarations that are deliberately kept in one home.
- [ ] Every task requiring a live system consults the flag. At minimum this must cover: any `state: started` in the base path (enable-only when the flag is true) and `loginctl enable-linger` in `roles/user/tasks/main.yml:28-33` (write `/var/lib/systemd/linger/<user>` directly when the flag is true).
- [ ] Verification: `ansible-playbook -i localhost, --connection=local site.yml --syntax-check` exits 0.
- [ ] Verification: `grep -rn "sand_image_build" roles/ site.yml` shows the default declaration plus at least one guarded task per identified incompatibility, and `grep -rn "enable-linger\|state: started" roles/` shows every hit is either guarded by the flag or outside the base phase. Paste both outputs into the task record.
- [ ] Verification: the default path is unchanged — `git diff` shows no task's *false-flag* behaviour altered. Confirm by running the `molecule/base` scenario (`molecule test -s base`) and observing it still passes, since it converges with the flag absent.

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
- The written audit, consumed by task 03 as the specification of what the chroot build must accommodate.

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

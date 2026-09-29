# Chroot compatibility of the base playbook

The image builder runs `site.yml` inside a mounted Debian 13 root with
`provision_phase=base`, `sand_image_build=true`, and `samba_enabled=false`.
There is no guest init, logind, or running root filesystem in that chroot.
The regular VM path omits `sand_image_build`, whose role default is `false`.

## Live-system operations in the base path

| Task | Why it cannot run in the image chroot | Image-build equivalent |
| --- | --- | --- |
| `roles/base/tasks/main.yml`: announce hostname to DHCP | `systemctl is-active` and `networkctl renew` need a running guest networkd and interface. | Skip. The configured hostname and networkd files take effect at first boot. |
| `roles/base/tasks/main.yml`: remount `/` with noatime | The chroot's `/` is the host's mount of the image; remounting it affects the builder, not the future guest. | Keep the preceding `/etc/fstab` edit; it takes effect at first boot. |
| `roles/base/tasks/main.yml`: enable and start `fstrim.timer` | `systemd_service` cannot contact guest systemd. | Skip in Ansible; enable `fstrim.timer` against the offline root after the playbook. There is no timer to start until first boot. |
| `roles/base/handlers/main.yml`: reload sshd | The two sshd drop-ins notify this handler, but no guest sshd is running. | Skip the reload. The files are read when sshd first starts. |
| `roles/user/tasks/main.yml`: `loginctl enable-linger` | logind and its D-Bus socket do not exist. | Create `/var/lib/systemd/linger/` and the empty, root-owned `{{ user_name }}` file inside it. The user manager sees it at first boot. |
| `roles/agent-clipboard/tasks/main.yml`: enable and start the clipboard service | No guest systemd exists to reload, start, or restart the new unit. | Keep the unit file; enable `sand-image-clipboard.service` against the offline root. It starts at first boot. |
| `roles/dev-tools/tasks/main.yml`: start/enable `docker.socket`, disable `docker.service` | Both `systemd_service` calls require a live manager, including the enable-only call from inside the chroot. | Enable `docker.socket` and disable `docker.service` against the offline root. Socket activation begins at first boot. |
| `roles/dev-tools/handlers/main.yml`: reload systemd and restart Docker | A changed optional registry proxy drop-in notifies these handlers, but there is no manager or daemon to reload. | Skip both. The drop-in is read when the socket first activates Docker. `Update CA certificates` remains safe: it writes files only. |

The regular-path actions above are guarded by `not sand_image_build`; their
on-disk alternatives run only when `sand_image_build` is true. The offline
user creation sets a locked password (`!`), skips random password generation,
and does not print a password. Normal user creation and password output are
unchanged when the flag is absent.

## Build-script interface

After the chroot playbook finishes and before unmounting, run these commands
from the **builder host**, replacing `$MOUNT` with the mounted image root:

```sh
systemctl --root="$MOUNT" enable qemu-guest-agent.service
systemctl --root="$MOUNT" enable fstrim.timer
systemctl --root="$MOUNT" enable docker.socket
systemctl --root="$MOUNT" disable docker.service
systemctl --root="$MOUNT" enable sand-image-clipboard.service
```

The guest agent was already enabled this way by the older base-image workflow.
The four other commands preserve the service policy expressed by the base
roles. `fstrim.timer` is best effort in the live role, so the builder should
first check that the unit exists if it uses an image without the timer.

Also retain the builder's `policy-rc.d` returning 101 while apt installs
packages. It prevents Debian package post-install scripts from starting
daemons; it does not replace any of the explicit guards above. Do not
bind-mount the builder's `/run` into the chroot. Ansible's hostname module
uses its file strategy and writes `/etc/hostname` in the image when the
systemd runtime markers under `/run` are absent. The `/etc/hosts` template,
locale generation, timezone files, account/group edits, downloads, installers,
and certificate-store updates are file or process work and need no live guest
systemd. The builder must provide networking and a usable `/etc/resolv.conf`.

`roles/samba` has its own `smbd` start and restart handler; it is outside the
sand base image path because the builder passes `samba_enabled=false` (as sand
does for its VM builds). Finalize-only agent roles are outside this path too.

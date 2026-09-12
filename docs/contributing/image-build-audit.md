# Image build audit: making the base phase chroot-compatible

Plan 22 ("Baked base images") moves the base-phase Ansible run out of a
booted guest VM and into `ansible-playbook` running **inside a `chroot`** over
a disk image mounted at some path (`/mnt/img` below), on a CI runner, with no
guest ever booted. This document is that audit: every task in `roles/base`
and `roles/user` (plus two tasks in `roles/dev-tools` folded into the base
phase — see "Scope" below) was checked against what a chroot like that can
and cannot do, and the incompatible ones now run their offline equivalent
behind a new `sand_image_build` flag (`roles/base/defaults/main.yml`,
default `false`).

**The governing rule, unchanged by this task:** when `sand_image_build` is
`false` — the default, and what every existing in-guest `sand` run passes,
explicitly or by omission — every task's behaviour is byte-identical to
before this flag existed. This document is the audit; the flag and its
guards are the implementation; task 03 (the chroot build script) is the
consumer of both, in particular the "Interface for task 03" section below.

## What a chroot build can and cannot do

Task 03's build script mounts a qcow2's root filesystem at a path (e.g.
`/mnt/img`) and runs something like `chroot /mnt/img ansible-playbook
site.yml -e sand_image_build=true -e provision_phase=base ...`. Inside that
chroot:

- **No running init.** There is no PID 1 managing services, no
  `systemd-logind`, no `systemd-hostnamed`, and nothing listening on
  systemd's D-Bus socket. Anything that talks to one of those — `systemctl
  start`, `systemctl is-active`, `hostnamectl`, `loginctl` — either fails
  outright or (worse, per the "Set hostname" investigation below) silently
  degrades to a different code path.
- **`systemctl enable`/`disable` also fail from inside the chroot.** This
  repo already established this the hard way in
  `.github/workflows/base-image.yml` (building the Proxmox base image): a
  plain `systemctl enable <unit>` run while chrooted refuses because there is
  no running manager to talk to. The workaround used there, and the one this
  audit adopts, is `systemctl --root=/mnt/img enable <unit>` run **from
  outside** the chroot, which manipulates the `wants/` symlinks on disk
  directly without needing a running instance. Ansible's `systemd_service`
  module has no `--root` equivalent, so no task in this fileset attempts unit
  enablement while `sand_image_build` is true — see "Interface for task 03".
- **A `policy-rc.d` returning 101** is dropped in place by the existing
  build scaffold before any package installs, so a `.deb`'s postinst will not
  try to *start* a daemon via `invoke-rc.d`. That covers packages; it does
  not cover an explicit Ansible `service`/`systemd_service` task, which calls
  `systemctl`/`service` directly and is not consulted by `policy-rc.d`.
- **Network works.** A chroot shares the host's network namespace — apt,
  `get_url`, and `curl | sh` installers all behave normally. Nothing in this
  audit is designed around an offline network; the incompatibility is always
  "no live init," never "no network."
- **`/dev`, `/proc`, `/sys` are bind-mounted** by the existing build scaffold
  (see `.github/workflows/base-image.yml`), so most things that read them are
  fine. **`/run` is deliberately NOT bind-mounted** from the host — this
  matters (see "Set hostname" below) and task 03 must keep it that way: a
  `/run/systemd/system` bind-mounted in from a systemd-managed CI runner
  would make Ansible's own live-system detection lie.

## Scope

The task's acceptance criteria name `roles/base` and `roles/user`. Two tasks
in `roles/dev-tools` are included as well, because `dev-tools` runs
unconditionally in the base phase (`site.yml`: `when: provision_phase !=
'finalize'`, not behind any opt-out flag) and enables `docker.socket` with
`state: started` — exactly the kind of task this audit exists to catch. Every
other role was checked and excluded for a stated reason:

- **`roles/claude-code`, `roles/codex`** also run in the base phase
  (`toolset_claude`/`toolset_codex`, gated the same way as dev-tools). Audited
  in full: both roles only write files under a user's home directory and run
  `curl | bash` / `curl | sh` installers. Neither calls `systemctl`,
  `hostnamectl`, `loginctl`, or any other live-system primitive. No changes
  needed. (The upstream installer scripts themselves are not part of this
  repo and were not re-audited past confirming they only place binaries under
  `~/.local/bin`; this is a stated residual assumption, not a gap this task
  can close.)
- **`roles/samba`** enables and starts `smbd` unconditionally
  (`roles/samba/tasks/main.yml:27-31`), which is exactly the pattern this
  audit guards elsewhere. It is excluded from the base path by construction,
  not by an added guard: `internal/provision/vars.go`'s `BuildExtraVars`
  emits `samba_enabled: false` **unconditionally, for every phase, on every
  sand-driven run** (Lima VMs have no host-home mount to share). `site.yml`
  only includes the `samba` role `when: samba_enabled | default(true) |
  bool`, so it never runs on any path `sand` drives — base-phase image builds
  included, provided task 03 reuses `BuildExtraVars` (or otherwise emits
  `samba_enabled: false`) for its extra-vars. **This is a hard requirement on
  task 03**, called out again in "Interface for task 03" below, precisely
  because nothing in `roles/samba` itself enforces it — a direct
  `ansible-playbook site.yml -e sand_image_build=true` with no
  `samba_enabled` override would hit this at `state: started` and fail inside
  the chroot.
- **`roles/project`** only runs in `finalize`/`full`
  (`when: provision_phase | default('full') != 'base'`), never in the `base`
  phase a chroot build runs. Out of scope by construction.

## Findings

### 1. `roles/user/tasks/main.yml:28-33` — `loginctl enable-linger` (GUARDED)

`loginctl enable-linger` talks to `systemd-logind` over D-Bus, which is not
running in a chroot. **Confirmed incompatible**, exactly as flagged in the
task brief.

Offline equivalent: linger has no state beyond this one file's existence —
`logind(8)`'s own enable-linger implementation IS creating
`/var/lib/systemd/linger/<user>`; it is never read for content, and the
existing task already relies on exactly that for its own idempotency
(`creates: /var/lib/systemd/linger/{{ user_name }}`). The chroot path now
touches that file directly instead.

```yaml
- name: Enable systemd linger for {{ user_name }} so detached tmux survives logout
  ansible.builtin.command:
    cmd: loginctl enable-linger {{ user_name }}
  args:
    creates: /var/lib/systemd/linger/{{ user_name }}
  when:
    - provision_phase | default('full') != 'finalize'
    - not sand_image_build

- name: Enable systemd linger for {{ user_name }} (offline equivalent for a chroot image build)
  ansible.builtin.file:
    path: /var/lib/systemd/linger/{{ user_name }}
    state: touch
    mode: "0644"
  when:
    - provision_phase | default('full') != 'finalize'
    - sand_image_build
```

### 2. `roles/base/tasks/main.yml:2-4` — "Set hostname" (AUDITED, NOT GUARDED — see rationale)

This was flagged in the task brief as the one that "matters most," and it
deserved the closest look. The conclusion is that **no guard is needed**, but
only because of a specific, non-obvious mechanism in Ansible's own
`hostname` module — documented here in full because getting this wrong in
either direction is exactly the "silent no-op, discovered later in CI"
failure mode this task exists to prevent.

`ansible.builtin.hostname` picks a platform "strategy" class per distro
(`/usr/lib/python3/dist-packages/ansible/modules/hostname.py`). For Debian,
`DebianHostname.strategy_class = FileStrategy` (line 712) — but that default
is only used as a *fallback*. The module's actual selection logic
(`Hostname.__init__`, line ~610) is:

```python
elif platform.system() == 'Linux' and ServiceMgrFactCollector.is_systemd_managed(module):
    self.strategy = SystemdStrategy(module)      # shells out to hostnamectl
else:
    self.strategy = self.strategy_class(module)  # Debian's is FileStrategy
```

`is_systemd_managed()` (`ansible/module_utils/facts/system/service_mgr.py`)
is a direct port of systemd's own `sd_booted(3)` canary check: it returns
`True` only if `/run/systemd/system/`, `/dev/.run/systemd/`, or
`/dev/.systemd/` exists. **None of those exist inside a chroot over a disk
image that has never booted** (or one whose last shutdown tore down its
tmpfs `/run` the normal way) — provided task 03's build script does not
bind-mount the host's `/run` into the chroot (see "What a chroot build can
and cannot do" above; this is a hard requirement on task 03).

So on a live, booted guest (`molecule/base`'s systemd container, and every
real Lima VM), `is_systemd_managed()` is `True` and the module drives
`hostnamectl`. Inside the chroot, it is `False` and the module transparently
falls back to `FileStrategy`, whose `set_permanent_hostname` is:

```python
class FileStrategy(BaseStrategy):
    FILE = '/etc/hostname'
    def set_permanent_hostname(self, name):
        with open(self.FILE, 'w+') as f:
            f.write("%s\n" % name)
```

— which is exactly the offline equivalent this audit would otherwise have
had to hand-write (compare to the linger and dev-tools findings above/below,
where no such built-in fallback exists and an explicit guard was required).
`BaseStrategy.set_current_hostname` (the "transient" half) is a no-op by
default and `FileStrategy` does not override it, so no attempt is made to
call `sethostname(2)` either — nothing here needs a live kernel hostname, a
namespace, or any privilege beyond writing a file.

**Verification note:** this repository's own `molecule/base` scenario
(`molecule test -s base`) currently fails, even on an unmodified checkout, at
this exact task — `Could not set static hostname: Failed to set static
hostname: Device or resource busy`. That is the container runtime available
in *this execution sandbox* refusing the underlying `sethostname(2)` syscall
in its own nested-container environment (a `D`-state `dpkg`/journal-commit
pattern was also observed installing `systemd` in a throwaway container here,
consistent with a restricted/nested runtime, not a genuine chroot semantics
issue). It is unrelated to `sand_image_build` and reproduces identically
before and after every change in this task (see "Verification" below) — it
is not evidence against the `is_systemd_managed()` analysis above, which is
a static read of `ansible-core`'s own source, not something this sandbox's
container restrictions can influence. It does mean this task could not
obtain a passing `molecule test -s base` run in this environment; see
"Verification" for what was checked instead.

### 3. `roles/base/tasks/main.yml:6-12` — "Deploy /etc/hosts" (AUDITED, NOT GUARDED)

Pure `ansible.builtin.template` file write, no live-system dependency. Note
for the record: the task brief described this and "Set hostname" as already
`provision_phase != 'finalize'`-gated; in the actual current file **neither
task carries a `when:` clause at all** — both run unconditionally in every
phase (`full`, `base`, and `finalize`). That does not change this task's
chroot-compatibility (the template write is safe in either case), but it is
worth recording since it means "Set hostname" also runs during `finalize`
re-provisioning of an already-built clone, not just during a chroot base
build.

### 4. `roles/base/handlers/main.yml:2-5` — "Reload sshd" handler (GUARDED)

Notified by "Reap SSH sessions whose client has vanished" and "Accept
COLORTERM over SSH" (both `copy` tasks writing `sshd_config.d` drop-ins).
On a fresh chroot image build both tasks report `changed` on their very
first run, so the handler fires. `ansible.builtin.service: state: reloaded`
issues `systemctl reload ssh` (or `service ssh reload`), which needs a live
sshd process and a working systemd/D-Bus to signal it — neither exists in
the chroot, and there is nothing to reload anyway (no sshd is running).

Guarded with `when: not sand_image_build` directly on the handler task
(handlers support `when:` the same as any task). The drop-ins are already on
disk regardless; every real sshd start afterwards (the guest's actual first
boot) reads them fresh, so skipping the reload here loses nothing.

### 5. `roles/dev-tools/tasks/main.yml:33-42` — Docker socket enable/disable (GUARDED)

```yaml
- name: Enable the Docker socket (activates the daemon on first use)
  ansible.builtin.systemd_service:
    name: docker.socket
    state: started
    enabled: true

- name: Keep the Docker service off the boot path
  ansible.builtin.systemd_service:
    name: docker.service
    enabled: false
```

The first task's `state: started` obviously cannot run under chroot. Both
tasks' `enabled:` flips are also excluded, per the established
`systemctl --root=` precedent in `.github/workflows/base-image.yml`
described above — plain `systemctl enable/disable` (no `--root`) refuses
inside a chroot with no running init. Both tasks are guarded with
`when: not sand_image_build`; there is no simple direct-file offline
equivalent worth hand-rolling in Ansible for this (unlike linger/hostname,
which reduce to a single file), so the requirement is recorded here and
handed to task 03's build script instead — see "Interface for task 03".

### 6. `roles/dev-tools/handlers/main.yml:7-15` — "Reload systemd" / "Restart Docker" (GUARDED, defensive)

Only notified by the Docker registry proxy tasks
(`devtools_docker_registry_proxy_enabled`), which `internal/provision/vars.go`
only sets when `cfg.DockerProxyHost != ""` — never the case for a base image
build. Guarded anyway, on the same reasoning as the sshd handler above (any
`service`/`systemd_service` handler will fire during a chroot run if its
notifying task reports changed, and a future caller could combine the two
flags): `daemon_reload: true` needs a running systemd instance to reload, and
`state: restarted` needs a running daemon to restart.

### 7. `roles/samba` — excluded by construction, not guarded (see "Scope")

`state: started` at `roles/samba/tasks/main.yml:27-31` is a real
chroot-incompatibility in isolation, but `samba_enabled` is hard-set `false`
for every `sand`-driven run (`internal/provision/vars.go:40`), so the role
never runs on any path `sand` drives. See "Scope" above for why this is not
guarded in the role itself, and "Interface for task 03" for the requirement
this places on the build script.

### Checked and found already safe (no code change)

- **Fact gathering** (`ansible.builtin.setup`, implicit at every play start
  since `site.yml` does not set `gather_facts: false`). The one part of fact
  gathering that could plausibly hang or fail under chroot —
  `service_mgr`'s pid-1 detection — reads `/proc/1/comm` (a bind-mounted
  `/proc`, so this reflects the CI *host's* PID 1, not the chroot's) and
  falls back to a plain string compare; it does not shell out to
  `systemctl`. `service_facts` (which does shell out) is never invoked by
  this fileset.
- **`ansible.builtin.user`, `getent`, `lineinfile`, `blockinfile`,
  `template`, `copy`, `file`, `apt`, `get_url`** throughout both roles: all
  pure file/package operations, unaffected by the absence of an init system.
- **`ansible.builtin.assert`/`stat`/`slurp`** (timezone validation, SSH key
  handling): pure file reads, no live-system dependency.
- **`visudo -cf`, `mkcert -install`, `direnv allow`, `update-ca-certificates`,
  the `uv`/Claude Code/Codex installer scripts**: all plain binaries or
  installer scripts that write files under a user's home directory or a
  trust store; none call into systemd. (See "Scope" for the stated residual
  assumption on the upstream installer scripts' own contents.)
- **`roles/base/tasks/main.yml` timezone block** (`:286-420`): already
  written to avoid `timedatectl` for exactly this reason — the block's own
  comment explains it needs "a live systemd+D-Bus (absent in the molecule
  container, and not guaranteed at every point of a base build)" and writes
  `/etc/localtime` + `/etc/timezone` directly instead. No change needed;
  this is the pattern the hostname and linger offline equivalents above
  follow.

## Interface for task 03

The build script (running from **outside** the chroot, with the image
mounted at some path — call it `$MOUNT`) must, after the chroot Ansible run
completes with `sand_image_build=true`:

1. **Enable `docker.socket` and disable `docker.service`** on the offline
   root:
   ```sh
   systemctl --root="$MOUNT" enable docker.socket
   systemctl --root="$MOUNT" disable docker.service
   ```
   (Mirrors `roles/dev-tools/tasks/main.yml`'s in-guest behaviour — see
   Finding 5.)
2. **Pass `samba_enabled: false`** in the extra-vars for the chroot run (or
   otherwise ensure the `samba` role's `when:` evaluates false) — see
   "Scope" and Finding 7. This already happens for free if task 03 reuses
   `internal/provision.BuildExtraVars`, which emits it unconditionally; if
   task 03 builds its own extra-vars from scratch instead, this line must be
   carried over explicitly.
3. **Do not bind-mount the host's `/run`** into the chroot (only `/dev`,
   `/proc`, `/sys`, matching the existing `.github/workflows/base-image.yml`
   scaffold) — see Finding 2. Doing so would make
   `ansible.builtin.hostname`'s live-system detection see the CI runner's
   `/run/systemd/system` and try to shell out to `hostnamectl`, which would
   then fail for a different, harder-to-diagnose reason (no D-Bus socket
   actually reachable at that path despite the canary file existing).
4. **Pass `sand_image_build: true`** alongside `provision_phase: base` for
   the chroot invocation. This is the flag every guard in this document
   consults; nothing else in `site.yml` sets it.
5. No other unit enablement is required by this fileset. `openssh-server`
   and `qemu-guest-agent`'s own package-level enablement (via their `.deb`
   postinst / systemd presets) is unaffected by anything in this audit — it
   is handled the same way `.github/workflows/base-image.yml` already
   handles `qemu-guest-agent` today, which task 03 should follow directly.

## Verification

Run from the repository root:

```console
$ ansible-playbook -i localhost, --connection=local site.yml --syntax-check
playbook: site.yml
$ echo $?
0
```

```console
$ grep -rn "sand_image_build" roles/ site.yml
roles/user/tasks/main.yml:35:    - not sand_image_build
roles/user/tasks/main.yml:38:# (sand_image_build: true). loginctl talks to systemd-logind over D-Bus,
roles/user/tasks/main.yml:52:    - sand_image_build
roles/base/defaults/main.yml:126:sand_image_build: false
roles/dev-tools/tasks/main.yml:41:# equivalent "--root" mode, so under a chroot image build (sand_image_build:
roles/dev-tools/tasks/main.yml:52:  when: not sand_image_build
roles/dev-tools/tasks/main.yml:58:  when: not sand_image_build
roles/dev-tools/handlers/main.yml:16:  when: not sand_image_build
roles/dev-tools/handlers/main.yml:22:  when: not sand_image_build
roles/base/handlers/main.yml:3:# chroot image build (sand_image_build: true) there is no running sshd to
roles/base/handlers/main.yml:14:  when: not sand_image_build
```
(`site.yml` itself has no hit — it consumes no flags directly; every guard
lives in the role that owns the task. This is expected, not a gap. Line
numbers shift as this file is edited; re-run the grep rather than trusting
them verbatim.)

```console
$ grep -rn "enable-linger|state: started" roles/
roles/user/tasks/main.yml:30:    cmd: loginctl enable-linger {{ user_name }}
roles/user/tasks/main.yml:40:# no other state: logind's own enable-linger implementation IS just creating
roles/samba/tasks/main.yml:30:    state: started
roles/dev-tools/tasks/main.yml:34:# Both tasks below need a live systemd: `state: started` obviously needs a
roles/dev-tools/tasks/main.yml:50:    state: started
```
The real (non-comment) hits: `roles/user/tasks/main.yml:30` (loginctl,
guarded — Finding 1), `roles/dev-tools/tasks/main.yml:50` (docker.socket,
guarded — Finding 5), `roles/samba/tasks/main.yml:30` (smbd, outside the
base path — Finding 7 / Scope).

**Default-path regression check.** Every edit in this task either (a) adds a
brand-new task/handler that only runs `when: sand_image_build` (inert by
default, since the flag defaults `false`), or (b) appends `not
sand_image_build` to an existing `when:` list. Ansible ANDs a list-form
`when:`, and `not false` is `true`, so `[existing_condition, not
sand_image_build]` evaluates to exactly `existing_condition` — the original
task's truth value is unchanged for every caller that does not pass
`sand_image_build: true`, which today is every caller. `git diff` confirms
no existing line's text changed except to append that clause; no task was
reordered, renamed, or had its non-`when:` parameters touched.

**`molecule test -s base`.** Docker is available in this environment, and
`molecule` (plus the `community.docker`/`ansible.posix` collections its
docker driver needs) was installed for this task specifically to run it.
It does not currently pass here — but it fails identically, at the same
"Set hostname" task, for the same `sethostname(2)` "Device or resource busy"
error, **on an unmodified checkout before any change in this task**, and
continues to fail at that exact point afterwards. This is a limitation of
this specific nested-container execution sandbox (see Finding 2's
verification note), not a regression introduced here, and not something this
task can fix — molecule/base's container platform choice and privilege
requirements are out of this task's scope. This was confirmed by running the
baseline before making any change, not assumed.

A second, independent symptom of the same sandbox limitation showed up
re-running molecule after the code changes in this task: the scenario's
`prepare.yml` (`apt-get install locales`, entirely unrelated to anything this
task touched) stalled indefinitely with its `dpkg` process parked in
uninterruptible sleep on `jbd2_log_wait_commit` — an ext4 journal-commit wait
that should resolve in milliseconds on ordinary storage. The same wait
channel was reproduced independently and deliberately in a disposable
`debian:trixie-slim` container (`apt-get install systemd`, unrelated to this
repo) while investigating Finding 5's `systemctl enable/disable` behaviour,
confirming this sandbox's container storage layer — not this task's Ansible
changes — is what stalls package installs generally. Both hung runs were
killed rather than left to run indefinitely; neither reached far enough to
exercise anything this task changed.

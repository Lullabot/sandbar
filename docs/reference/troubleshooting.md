# Troubleshooting

Common problems with `sand` and how to resolve them.

## A stale base image

`sand` builds the heavy install (packages, Docker, Node,
Claude Code, …) once into a stopped base image (`sandbar-base` by default),
then clones every VM from it. Each base is stamped with the playbook version
it was built from, and `sand` rebuilds it automatically the next time you
create a VM if the current playbook has moved on — so most staleness is
self-healing.

What isn't automatic is a base you want to refresh for another reason —
newer OS packages, a rebuilt dependency, or you just want a clean floor. In
that case, delete the base yourself and let the next create rebuild it, or
pass `--rebuild` to force the rebuild-then-create in one step:

```bash
sand create --rebuild
```

`--rebuild` deletes and recreates the base image first, then makes the VM.
It always rebuilds, regardless of the staleness check.

## Proxmox: base provisioning hangs at "Install Claude Code"

**Symptom:** on the Proxmox backend, building the base image runs the playbook
fine until the `claude-code : Install Claude Code using the official installer`
task, where it hangs indefinitely with no output. On the guest, the
`claude … install` process sits pegged at ~100% CPU (state `R`) with no network
sockets and making almost no syscalls.

**Why:** it is not `sand`, apt, or the network — it is a Claude Code bug
([anthropics/claude-code#77208](https://github.com/anthropics/claude-code/issues/77208)).
Proxmox's default CPU model, `kvm64`, masks modern x86-64 instruction
extensions (no SSE4.2/POPCNT/AVX2) from the guest, and Claude Code ≥ 2.1.205 —
built on a newer Bun runtime that assumes those instructions — livelocks in
userspace at 100% CPU during startup instead of failing with an error. `curl`
and the installer's own download succeed first (that host is reached over IPv4
with a full instruction set on the *host* side), so provisioning gets all the
way to the `claude install` step before wedging. It bites Proxmox specifically
because Lima runs guests with host-CPU passthrough, so the Lima backends never
see a feature-masked CPU.

**Fix:** `sand` now creates its Proxmox VMs with the CPU type set to `host`
(full host-CPU passthrough), which exposes the real instruction set and is the
fix the upstream issue confirms. You get this automatically once the base is
rebuilt on a current `sand` — delete the base and recreate, or `sand create
--rebuild`. If you provision VMs outside `sand`, set their CPU type to `host`
(or any model exposing SSE4.2/POPCNT/AVX2, e.g. `x86-64-v2-AES`) rather than
leaving the `kvm64` default. A quick check on a guest: `lscpu | grep -i sse4_2`
— if it prints nothing, that VM has the feature-masked CPU that triggers the
hang.

## Proxmox: a create fails with `exit status 255`

**Symptom:** a create dies part-way through provisioning with an error ending in
`exit status 255`, and the streamed output simply stops — the last thing shown is
an Ansible `TASK [...]` banner, with no `fatal:` line, no `PLAY RECAP`, and no
error from the task itself. `sand` then removes the partial VM.

**Why:** 255 is `ssh`'s own status, not Ansible's. `sand` runs each provisioning
phase as a single ssh session to the guest, and a playbook that genuinely fails a
task exits 2 (or 4 for unreachable, 250 for an internal error) — statuses you
would see reported verbatim. 255 means the layer underneath failed instead: the
connection to the guest dropped, or the remote command was killed by a signal.
The task banner on screen is simply the last one printed before the channel went
away; it is not the task that failed.

Whether `ssh` printed anything of its own is a useful first split. `sand` merges
ssh's stderr into the same stream as the guest's output, and ssh reports a
connection it *noticed* failing (`connect to host … failed`, `client_loop: send
disconnect: Broken pipe`, `Connection reset by peer`) even at the quiet log level
`sand` uses. A 255 with one of those lines is a connection that broke. A 255 with
no ssh message at all points instead at the remote command being killed by a
signal, which OpenSSH reports as 255 and says nothing about.

Since `sand` deletes a partially-built VM, the guest's own logs — the best
evidence for which of those happened — go with it. Two opt-ins change that:

```bash
# Keep the failed VM instead of purging it, so you can inspect the guest.
SAND_KEEP_FAILED=1 sand create …

# Write ssh's own protocol log to a file (one per target, appended across every
# connection to it) instead of to the output stream.
SAND_SSH_DEBUG=1 sand create …                 # ~/.cache/sandbar/ssh-debug/
SAND_SSH_DEBUG=/path/to/dir sand create …      # or a directory you choose
```

With both set, the failure names the log file it wrote, and the guest is still
running. On it, `journalctl -u ssh`, `dmesg`, and `ps` for the `ansible-playbook`
process answer the question the exit status cannot: whether the guest-side run
was still alive when the client gave up (the connection died) or was already gone
(the process died).

A kept VM is **not** usable, holds its VMID, and blocks its own name until you
remove it with `qm destroy <vmid> --purge`. If the create got as far as cloning
your project, it also holds the clone token in the guest's per-org `.env` — so
destroy it once you're done rather than leaving it around.

`SAND_KEEP_FAILED` applies to a create's finalize — everything from the moment
the clone exists. A failure earlier than that (the base template build, or the
clone itself) has no VM to keep. `sand reset` already leaves its VM in place when
the finalize fails, so it needs no opt-in.

`SAND_SSH_DEBUG` is not Proxmox-specific: it logs every ssh `sand` runs, on any
backend. It does not change scp transfers, which have nowhere to put a log but
the payload stream.

## `limactl list` fails while a VM is being cloned or deleted

**Symptom:** the fleet briefly disappears from the board, or a headless
`sand` command reports it can't list instances — even though nothing is
actually broken.

**Why:** this is an upstream Lima behavior
([lima-vm/lima#5236](https://github.com/lima-vm/lima/issues/5236)).
`limactl clone` creates an instance's directory before it writes that
directory's `lima.yaml`, and `limactl delete` removes the `lima.yaml` before
it removes the directory. In either window, `limactl list` doesn't skip the
half-written instance — it aborts on the first one it can't load and prints
nothing, so *every* instance vanishes from the listing, not just the one
mid-clone or mid-delete. The window is roughly 40–60 seconds for a clone of a
large base image (i.e. most of a create or reset) and sub-second for a
delete.

**Fix:** none needed. `sand` recognizes this specific failure and keeps
showing the fleet it already has, with a one-time notice, instead of
flashing empty or reporting every VM as failed. If you see a message about a
VM being cloned or deleted, it's transient — wait for the clone or delete to
finish and the listing recovers on its own. `limactl shell`, `start`, and
`stop` are unaffected; only enumeration (`limactl list`) is subject to this.

## A build failed and the tile is red

A red tile means the last provision on that VM failed. Its Ansible log is
still available — press `l` on the tile to reopen it, whether the build is
still running or already finished. You don't need to stay on the progress
view to keep a build going: leaving it (or starting another VM) doesn't
cancel anything, builds keep running in the background, and the tile itself
carries the progress bar and pass/fail state so you can check back later.

## A build looks stuck on one task

Ansible prints a task's banner and then nothing at all until that task returns,
so a slow task and a wedged one look identical on the tile: the same name, the
same bar. To tell them apart, a building tile shows the **current task's elapsed
time**, right-aligned on the `ansible: …` row, once that task has been running
more than ten seconds:

```
ansible: project · 72/72           8m14s
```

A number appearing there means the task is genuinely slow, not that anything is
wrong — under ten seconds nothing is shown, so the row stays quiet through the
hundreds of sub-second tasks a normal run steps through. A timer that keeps
climbing well past what the task should need is the signal worth acting on;
press `l` on the tile to read the log.

Also note the bar reaching 100% does **not** mean the build is done — it tracks
which task the run is on, and the last task still has to finish. A full bar with
a climbing timer is a run working on its final task.

The most common cause of a genuinely slow clone is submodules: `sand` clones
recursively, so a small project can pull in a very large one. `rtl_433` is 5 MB
and its test-data submodule is 1.2 GB — a multi-minute clone, all of it under a
single silent task banner.

## Out of disk on the Lima volume

Each VM clone is grown to its configured `--disk` size, and that space has
to actually exist on the volume backing Lima's instance store — cloning
several large VMs can exhaust it even though each individual VM's `--disk`
looked reasonable at create time.

Check free space before it becomes a problem: the TUI header shows free disk
on that volume live, alongside CPU and memory usage, so you can see it
shrinking before a clone fails outright. If you're low, the usual fix is to
delete VMs (or a stale base image) you no longer need with `limactl delete`,
or lower the `--disk` size on future creates.

## Lima is too old to support `clone`

`sand` depends on `limactl clone` to make cloning a base image fast. If your
installed Lima predates that command, `sand` detects it up front and refuses
with an explicit "your Lima is too old" error rather than failing partway
through a build. Upgrade Lima and try again:

```bash
brew upgrade lima
```

If you didn't install Lima via Homebrew, use whatever method you used
originally — see the [Lima installation docs](https://lima-vm.io/docs/installation/).

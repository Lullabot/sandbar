# Troubleshooting

Common problems with `sand` and how to resolve them.

## A stale base image

`sand` builds the heavy install (packages, Docker, Node,
Claude Code, …) once into a stopped base image (`claude-base` by default),
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

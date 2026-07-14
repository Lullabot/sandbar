# Your First VM

## The interactive way

Run `sand` with no arguments to open the TUI board:

```bash
sand
```

Press `n` to create a new VM, fill in the form, and confirm. Once it's
running, select its tile and press `S` to get a shell inside it.

## The headless way

To create a VM without the TUI:

```bash
sand create
```

Then connect with:

```bash
sand shell NAME
```

See the [CLI Reference](../using-sand/cli-reference.md) for the full flag
list.

## What to expect the first time

The very first VM you create builds a shared base image (`claude-base`),
which does a full, identity-free install and takes a while. Every VM you
create after that clones the base image instead of reinstalling everything,
so it's fast. See [How Provisioning Works](how-it-works.md) for why it's
built this way.

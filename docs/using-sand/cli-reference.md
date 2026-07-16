# CLI Reference

There are four entry points:

- [`sand`](#sand) — no arguments — launches the interactive TUI.
- [`sand create`](#sand-create) — headless, non-interactive VM provisioning.
- [`sand shell NAME`](#sand-shell-name) — attach to a running VM's persistent
  tmux session.
- [`sand version`](#sand-version-sand-version) / `sand --version` — print
  the build identity.

Any other first argument is an unknown subcommand and exits `2`.

## `sand`

Run with no arguments, `sand` launches the interactive TUI: it lists
instances, streams a build's progress, and drives the same create/recreate/
delete/start/stop lifecycle as the headless commands below. See
[The TUI](tui.md) for the keybindings and screens.

## `sand create`

```
Usage: sand create [flags]

Headlessly provision a Claude Code development VM: no TUI, no prompts. Every
flag has a default: --git-name/--git-email fall back to the host's git config
(user.name/user.email), so on a machine with git configured `sand create`
needs no flags. If neither the flags nor the host git config supply an
identity, sand errors rather than fabricate a commit author. Flags mirror the
original bash provisioner's, minus --ref (the playbook is embedded in this
binary, so there is no ref to pin).
```

It never prompts: every flag has a default (or falls back to something on the
host), and a missing *required* value — git identity — is a validation error,
not a prompt.

### Flags

| Flag | Type | Default | Description |
|---|---|---|---|
| `--name` | string | `claude` | Lima instance name. |
| `--base-name` | string | `sandbar-base` | Base image instance name; clones are made from this shared, long-lived image. |
| `--hostname` | string | same as `--name` | VM hostname. Empty means `EffectiveHostname()` falls back to `--name`. |
| `--user` | string | the **host username** (`id -un`, then `$USER`, then `claude`) | Primary VM user. Lima creates a guest user matching the host username, so this mirrors that — it is never sent empty, since an empty `user_name` would override the Ansible user role's own default and break in-guest user creation. |
| `--git-name` | string | host `git config user.name` | git `user.name` written into the VM. See [git identity](#-git-name-git-email-fall-back-to-host-git-config) below. |
| `--git-email` | string | host `git config user.email` | git `user.email` written into the VM. See [git identity](#-git-name-git-email-fall-back-to-host-git-config) below. |
| `--cpus` | string (parsed as int) | `2` | vCPUs. Must be a positive integer. |
| `--memory` | string | `8GiB` | RAM, e.g. `8GiB`. |
| `--disk` | string | `100GiB` | Disk size, e.g. `100GiB`. See [disk sizing](#disk-sizing) below. |
| `--locale` | string | `en_US.UTF-8` | System locale. |
| `--domain` | string | `lan` | Domain suffix. |
| `--docker-proxy-host` | string | *(empty — disabled)* | Docker registry pull-through proxy host. Optional; when set, `sand` also forces on `devtools_docker_registry_proxy_enabled`. |
| `--clone-url` | string | *(empty — no clone)* | HTTPS repo to clone into the VM. Optional. |
| `--clone-token` | string | *(empty)* | Token for `--clone-url` (e.g. a GitHub PAT). Optional; see [credential handling](#-clone-token-is-a-credential) below. |
| `--recreate` | bool | `false` | If `--name` already exists **and is sand-managed**, delete and re-clone it. |
| `--rebuild` | bool | `false` | Delete and rebuild the base image first, then create. |
| `--profile` | string | the last-used [Connection Profile](connection-profiles.md), else `local` | Which connection profile to create the VM on. Only that one profile is built and preflighted — the rest of your fleet is untouched. A named profile that doesn't exist, or is disabled, is a validation error. |

### There is no `--ref` flag

If you go looking for one — the original bash provisioner had `--ref` to pin
the git ref of a checked-out playbook — it does not exist here, deliberately.
`cmd/sand/create.go` explains why in a comment next to where the flags are
registered: the playbook is embedded in the `sand` binary at build time
(`playbook_embed.go`), so there is no separate ref left to pin. Whichever
`sand` binary you run *is* the playbook version.

### `--git-name` / `--git-email` fall back to host `git config`

Neither flag is required. If you omit `--git-name`, `sand` reads
`git config user.name` on the host; if you omit `--git-email`, it reads
`git config user.email`. On a machine that already has a git identity
configured, `sand create` with no flags at all is enough.

`sand` only errors when **both** the flag and the host git config are empty
for a given field — it refuses to fabricate a commit author. The error names
the missing field and tells you to pass the flag or set it with
`git config --global user.name "..."` (or `user.email`).

### `--clone-token` is a credential

`--clone-token` (and the rest of the create-time variables) is never placed on
a command line inside the guest. `sand` streams the rendered Ansible
extra-vars — including the token, when set — over stdin into `/dev/shm`
(tmpfs) inside the VM, writes it with mode `0600`, and removes it in an `EXIT`
trap once the provisioning run for that phase finishes. It never touches the
VM's persistent disk and never appears in a process listing.

### `--rebuild` vs `--recreate`

These sound similar and do different things to different objects:

- **`--rebuild`** deletes and rebuilds the shared **base image** (`--base-name`,
  default `sandbar-base`) before creating. Use it when the base itself needs to
  pick up a playbook or dependency change that a VM cloned from it right now
  is not going to get, or if the base image is corrupted. It is independent of
  `--recreate` and the two may be combined.
- **`--recreate`** deletes and re-clones **this VM** (`--name`) from the
  (possibly still-old) base image. Use it to throw away one VM's disk and get
  a clean clone without touching anything else. It is gated: `sand` refuses to
  recreate a target that is not already a sand-managed VM, since recreate
  would otherwise delete and replace *any* instance it is pointed at,
  sand-managed or not.

### Disk sizing

The base image is always built at a fixed **20GiB floor**
(`vm.BaseDiskFloor`), regardless of `--disk` — `--disk` sizes the *clone*, not
the base. Each clone is then grown from that floor up to `--disk` once, before
its first start (`limactl edit --set '.disk=...'`).

Because the underlying qcow2 disk can grow but not shrink live, a `--disk`
smaller than the 20GiB floor is not something you can actually get: asking for
less does not shrink the clone below the floor it started at.

### `samba_enabled` does not apply here

Lima's Debian image role supports Samba-based host-home sharing, and its own
Ansible defaults may say otherwise, but `sand` forces
`samba_enabled: false` for every VM it creates (`internal/provision/vars.go`)
— there is no host-home mount to share in the first place (see
[Files & shells](files-and-shells.md)). If you see `samba_enabled` mentioned
anywhere in the underlying role's defaults, it does not apply to anything
`sand create` does.

### Examples

```sh
# Minimal — host git identity, all other defaults.
sand create

# Clone a private repo into the VM at create time.
sand create --name myproj --clone-url https://github.com/org/repo.git \
  --clone-token "$GITHUB_TOKEN"

# Non-default resources, explicit identity.
sand create --name big --cpus 8 --memory 16GiB --disk 200GiB \
  --git-name "Jane Dev" --git-email jane@example.com

# Create on a specific connection profile (see Connection Profiles).
sand create --profile work
```

### Verified `--help` output

```
$ sand create --help
Usage: sand create [flags]

Headlessly provision a Claude Code development VM: no TUI, no prompts. Every
flag has a default: --git-name/--git-email fall back to the host's git config
(user.name/user.email), so on a machine with git configured `sand create`
needs no flags. If neither the flags nor the host git config supply an
identity, sand errors rather than fabricate a commit author. Flags mirror the
original bash provisioner's, minus --ref (the playbook is embedded in this
binary, so there is no ref to pin).

Examples:
  sand create                                                   # host git identity
  sand create --git-name "Your Name" --git-email you@example.com
  sand create --profile work                                    # create on the "work" connection profile

Flags:
  -base-name string
    	Base image instance name (default "sandbar-base")
  -clone-token string
    	Token for the repo above (optional; GitHub uses it — never placed on argv inside the guest)
  -clone-url string
    	HTTPS repo to clone into the VM (optional)
  -cpus string
    	vCPUs (default "2")
  -disk string
    	Disk size, e.g. 100GiB (default "100GiB")
  -docker-proxy-host string
    	Docker registry pull-through proxy host (optional)
  -domain string
    	Domain suffix (default "lan")
  -git-email git config user.email
    	git user.email (default: host git config user.email)
  -git-name git config user.name
    	git user.name (default: host git config user.name)
  -hostname string
    	VM hostname (default: same as --name)
  -locale string
    	System locale (default "en_US.UTF-8")
  -memory string
    	RAM, e.g. 8GiB (default "8GiB")
  -name string
    	Lima instance name (default "claude")
  -profile string
    	Connection profile to create on (default: the last-used profile, else "local")
  -rebuild
    	Delete and rebuild the base image first, then create
  -recreate
    	If the named instance exists and is sand-managed, delete and re-clone it
  -user string
    	Primary VM user
```

(`--user` has no printed default because it is resolved to the host username
*after* flags are parsed, not at registration time — see the flags table
above.)

## `sand shell NAME`

Attach a shell to `NAME`'s persistent tmux session in the guest. This is the
same attach path the TUI's `S` key uses, so the two entrypoints never drift.

```
Usage: sand shell NAME [--profile <name>]

Attach a shell to NAME's persistent tmux session in the guest.

  C-a c   new window          C-a d   detach
  C-a |   split vertically    C-a S   split horizontally

Detaching — or just closing the terminal — leaves the session and everything
running in it alive; attach again with this same command and it is all still
there. Note C-a is tmux's prefix here, so it no longer moves the cursor to the
start of the line.

A second terminal running this command shares the same windows but keeps its
own current one, so two terminals can look at two different windows of the
same VM.

The named VM must already exist and be running (see 'sand' to list instances,
or 'sand create' to make one). If NAME is managed under more than one
connection profile, --profile picks which one to attach to.
```

`NAME` is required (exactly one positional argument); `--profile` may appear
before or after `NAME`. `sand shell` refuses a VM that does not exist or is
not running.

### Cross-profile resolution

Because the same VM name can exist under more than one
[Connection Profile](connection-profiles.md), `sand shell NAME` resolves
which one you mean like this:

1. **`--profile <name>`** given explicitly — used directly. An unknown or
   disabled profile name is a hard error.
2. With no `--profile`: if only one connection profile is enabled, `sand
   shell` uses it directly (this is also what a single-profile setup — the
   out-of-the-box default — always does, so nothing changes if you never
   create a second profile).
3. With more than one enabled profile: `sand shell` looks up which enabled
   profile's registry actually owns a VM named `NAME`. Zero owners is "no
   such VM"; exactly one owner is used automatically; more than one owner
   (the same name exists on two profiles) is an error asking you to pass
   `--profile` to disambiguate, and lists the profile names it's ambiguous
   between.

## `sand version` / `sand --version`

Prints the build identity and exits. Both spellings do the same thing, and
`--version` is checked before anything else in `sand`'s argument dispatch, so
it works even without `limactl` installed.

A released binary prints the version GoReleaser stamped in at build time
(`-ldflags "-X main.version=..."`). A binary built from source instead prints
the git revision Go's toolchain embeds automatically, with a `-dirty` suffix
if the working tree had uncommitted changes at build time — for example:

```
$ sand --version
07bae1a-dirty
```

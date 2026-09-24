# CLI Reference

There are nine entry points:

- [`sand`](#sand) — no arguments — launches the interactive TUI.
- [`sand create`](#sand-create) — headless, non-interactive VM provisioning.
- [`sand reset NAME`](#sand-reset-name) — rebuild an existing VM from its base
  image, optionally preserving selected directories or the whole guest home.
- [`sand template`](#sand-template) — snapshot, list, and delete golden VM
  templates.
- [`sand shell NAME`](#sand-shell-name) — attach to a running VM's persistent
  tmux session.
- [`sand paste-image NAME`](#sand-paste-image-name) — stage an image from the
  host clipboard on a running VM's guest clipboard.
- [`sand land NAME`](#sand-land-name) — list a VM's git checkouts, or open a
  draft PR, a branch page, or a [browser review](review.md).
- [`sand publish NAME PATH [ISSUE]`](#sand-publish-name-path-issue) — publish
  a checkout's local commits to a drupal.org issue fork.
- [`sand version`](#sand-version-sand-version) / `sand --version` — print
  the build identity.

Any other first argument is an unknown subcommand and exits `2`.

The CLI commands also have TUI actions: `n` creates a VM, `R` resets it,
`S` opens a shell, `l` opens Landing, and `v` pastes an image from the board.
The Landing pane also offers publishing to drupal.org. The CLI and TUI share
the checks and operations behind these actions.

## `sand`

Run with no arguments, `sand` launches the interactive TUI: it lists
instances, streams a build's progress, and drives the same create/reset/
delete/start/stop lifecycle as the headless commands below. See
[The TUI](tui.md) for the keybindings and screens.

## `sand create`

```
Usage: sand create [flags]

Headlessly provision a development VM for coding agents: no TUI, no prompts. Every
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
| `--name` | string | `claude` | VM name. Checked against the target backend's own naming rule before anything is built — see [VM names](#vm-names). |
| `--base-name` | string | `sandbar-base` | Base image instance name; clones are made from this shared, long-lived image. |
| `--hostname` | string | same as `--name` | VM hostname. Empty means `EffectiveHostname()` falls back to `--name`. |
| `--user` | string | the **host username** (`id -un`, then `$USER`, then `claude`) | Primary VM user. A guest user matching the host username is created for you, so this mirrors that — it is never sent empty, since an empty `user_name` would override the Ansible user role's own default and break in-guest user creation. |
| `--git-name` | string | host `git config user.name` | git `user.name` written into the VM. See [git identity](#-git-name-git-email-fall-back-to-host-git-config) below. |
| `--git-email` | string | host `git config user.email` | git `user.email` written into the VM. See [git identity](#-git-name-git-email-fall-back-to-host-git-config) below. |
| `--cpus` | string (parsed as int) | `2` | vCPUs. Must be a positive integer. |
| `--memory` | string | `8GiB` | RAM, e.g. `8GiB`. |
| `--disk` | string | `100GiB` | Disk size, e.g. `100GiB`. See [disk sizing](#disk-sizing) below. |
| `--locale` | string | `en_US.UTF-8` | System locale. |
| `--timezone` | string | *the timezone this host is in* | IANA timezone for the guest, e.g. `America/Toronto`. See [timezone](#-timezone-follows-the-host) below. |
| `--domain` | string | `lan` | Domain suffix. |
| `--docker-proxy-host` | string | *(empty — disabled)* | Docker registry pull-through proxy host. Optional; when set, `sand` also forces on `devtools_docker_registry_proxy_enabled`. |
| `--clone-url` | string | *(empty — no clone)* | HTTPS repo to clone into the VM. Optional. |
| `--clone-token` | string | *(empty)* | Token for `--clone-url` (e.g. a GitHub PAT). Optional; see [credential handling](#-clone-token-is-a-credential) below. |
| `--recreate` | bool | `false` | Delete and re-clone `--name` if it is **sand-managed**. The older spelling of [`sand reset NAME`](#sand-reset-name), which does the same thing and can additionally preserve state — see [`--rebuild` vs `--recreate`](#-rebuild-vs-recreate). |
| `--rebuild` | bool | `false` | Delete and rebuild the base image first, then create. |
| `--template` | string | *(empty)* | Clone from the named [golden template](golden-templates.md), bypassing the shared base. Mutually exclusive with `--rebuild`, `--recreate`, and an explicit `--base-name`. |
| `--profile` | string | the last-used [Connection Profile](connection-profiles.md), else `local` | Which connection profile to create the VM on. Only that one profile is built and preflighted — the rest of your fleet is untouched. A named profile that doesn't exist, or is disabled, is a validation error. |
| `--with-claude` | bool | remembered, initially `false` | Install current Claude Code in this VM. |
| `--with-codex` | bool | remembered, initially `false` | Install current Codex in this VM. |
| `--with-opencode` | bool | remembered, initially `false` | Install current OpenCode in this VM. |
| `--with-pi` | bool | remembered, initially `false` | Install current Pi in this VM. |
| `--with-ddev` | bool | `true` | Install DDEV in the base image. |
| `--with-go` | bool | `true` | Install the Go toolchain in the base image. |
| `--with-java` | bool | `true` | Install a headless JDK in the base image. |

Agent flags configure the individual VM. Omitted flags use the last submitted
agent selections, shared across profiles; explicit flags (including `=false`)
win. All four may be off. See [preference migration](../reference/files-and-state.md#coding-agent-preferences-and-migration)
for existing installations. Reset uses the selections recorded for that VM.
Likewise `--recreate` adopts that VM's recorded agents unless explicit agent
flags override them. Reset and recreate do not change the global preferences.

The dependency flags `--with-ddev`, `--with-go`, and `--with-java` configure
the shared base image. Omitted dependency flags adopt its version stamp;
explicit flags override it. Changing agents alone does not rebuild the base.

When `--template` is set, the saved template is the clone source and the
shared base is not inspected or rebuilt. The new VM records that provenance,
so `sand reset NAME` continues cloning from the same template.

### VM names

Lima and Proxmox accept different VM names:

| Name | Lima (local or remote) | Proxmox |
|---|---|---|
| `dev-box` | Allowed | Allowed |
| `test_vm` | Allowed | Rejected: underscores are not allowed in DNS names. |
| `web--1` | Rejected: separators must be single characters. | Allowed |
| `-web`, `web-` | Rejected | Rejected |
| `my vm` | Rejected | Rejected |

For a name accepted by both, use ASCII letters and digits separated by
single hyphens, with a letter or digit at each end and no more than 63
characters. For example: `dev-box`.

`sand create` and the TUI create form check the name before building and
explain any invalid character or format. Reset operations keep the existing
name and skip this check, so a VM created under older naming rules can
still be rebuilt.

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

### `--timezone` follows the host

VMs used to run in **UTC** — not by choice, but because nothing set a timezone
at all and the Debian/Ubuntu cloud images ship UTC. That made every log line,
`ls -l`, `date`, and commit timestamp inside a VM disagree with the machine you
were reading them on.

`sand` now reads the timezone of the host it is running on and provisions the
guest to match, so `sand create` with no flags gives you a VM whose clock
agrees with your own. Pass `--timezone` with an IANA name
(`--timezone Asia/Tokyo`) to override.

The host's zone is detected from, in order: `$TZ` (including the `:Zone` and
`:/path/to/zoneinfo/Zone` forms, and an empty `TZ=`, which POSIX defines as
UTC), `/etc/timezone`, and the target of the `/etc/localtime` symlink — which
covers Linux and macOS. If none of those give a usable answer, `sand` falls
back to `Etc/UTC`, which is exactly the old behaviour, and says so on stderr
rather than quietly handing you a VM with the wrong clock.

Legacy aliases (`US/Eastern`, `Canada/Eastern`, `Japan`) live in Debian's
separate `tzdata-legacy` package, which the base image does not install. Where
your host has one, `sand` resolves it to the canonical name by following the
host's own tzdata symlink, so `US/Eastern` reaches the guest as
`America/New_York` and simply works.

What happens to a zone the guest genuinely doesn't have depends on **who chose
it**:

- **You named it** with `--timezone` — the run stops with an error. You asked
  for a specific zone, and finishing would hand you a VM that is silently not
  in it.
- **`sand` detected it** from your host — the run continues, prints a warning,
  and leaves the guest's existing zone. Your create can't be broken by
  something you never asked for.

Malformed names (a leading `/`, a `..`, shell metacharacters) are rejected up
front by every entrypoint, before they can reach the playbook.

The timezone is applied in **both** provisioning phases, so a VM cloned from a
base image built before this feature existed — or built while you were in a
different timezone — still lands in the right zone. It is deliberately not part
of the base image's version stamp, so changing timezone does not force a base
rebuild.

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
- **`--recreate`** deletes and re-clones **this VM** (`--name`). It uses the
  VM's recorded settings for flags you omit, including its base image,
  resources, hostname, Git identity, and clone URL. Explicit flags override
  those settings; for example, `--recreate --disk 200GiB` changes the disk
  size. The target must already be managed by `sand`.

  Prefer [`sand reset NAME`](#sand-reset-name) in new scripts. It performs
  the same rebuild and also lets you preserve guest data. `--recreate`
  always deletes everything on the guest disk.

  You cannot pass `--clone-url` with `--recreate`: a rebuild keeps the
  recorded project URL. Create another VM to work on a different repository.

  Tokens are never recorded in the managed index. If the recorded clone URL
  points to a private repository, pass `--clone-token` again. Without it,
  `sand` warns that the clone may fail. See
  [credential handling](#-clone-token-is-a-credential).

### Disk sizing

The base image is always built at a fixed **20GiB floor**
(`vm.BaseDiskFloor`), regardless of `--disk` — `--disk` sizes the *clone*, not
the base. Each clone is then grown from that floor up to `--disk` once, before
its first start (on a Lima profile, `limactl edit --set '.disk=...'`; on
Proxmox, a disk resize through the API).

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

Headlessly provision a development VM for coding agents: no TUI, no prompts. Every
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
    	VM name (default "claude")
  -profile string
    	Connection profile to create on (default: the last-used profile, else "local")
  -rebuild
    	Destroy the base image and rebuild it from scratch before creating (a stale base is otherwise converged in place)
  -recreate
        Delete and re-clone the named instance if it is sand-managed. The older spelling of 'sand reset NAME', which does the same thing and can also preserve agent settings, the project, selected paths, or the whole home directory
  -timezone string
    	IANA timezone for the guest, e.g. America/Toronto (default: the timezone this host is in)
  -user string
    	Primary VM user
  -with-claude
        Install Claude Code when creating this VM (default: last submitted selection)
  -with-codex
        Install OpenAI Codex when creating this VM (default: last submitted selection)
  -with-ddev
    	Install DDEV in the base image (default true)
  -with-go
    	Install the Go toolchain in the base image (default true)
  -with-java
    	Install a headless JDK in the base image (default true)
  -with-opencode
        Install OpenCode when creating this VM (default: last submitted selection)
  -with-pi
        Install Pi when creating this VM (default: last submitted selection)
```

(`--user` has no printed default because it is resolved to the host username
*after* flags are parsed, not at registration time — see the flags table
above.)

## `sand reset NAME`

Rebuild a VM managed by `sand` from its base image. The name, project URL,
and recorded settings are kept; **guest files are deleted unless you choose
a preserve option**. This is the CLI equivalent of `R` in the TUI
([Resetting a VM](tui.md#resetting-a-vm)).

```sh
sand reset web                                    # clean rebuild, same settings
sand reset web --preserve-agents                  # keep coding-agent state
sand reset web --preserve-agents --preserve-project
sand reset web --preserve '~/src/app' --preserve '~/scratch/spike'
sand reset web --preserve-home                    # keep the guest home
sand reset web --cpus 8 --memory 16GiB            # change CPU and memory
```

`sand` refuses to reset a VM it does not manage. It checks the VM's ownership
marker first, then the managed index. You can reset a VM created by another
`sand` controller on the same host.

| Flag | What survives |
|---|---|
| `--preserve-agents` | Settings and files for Claude Code, Codex, OpenCode, and Pi. |
| `--preserve-project` | The project's organisation directory, including the checkout, uncommitted work, and `.env`. Skips cloning again if the checkout is present, so a private repository needs no clone token. |
| `--preserve PATH` | A directory inside the guest home, including a Git checkout or linked worktree. Repeat for more directories. |
| `--preserve-home` | The guest home, including everything above, except `~/.ssh/authorized_keys`. |

`--preserve` accepts an absolute guest path (`/home/you/src/app`), a quoted
tilde path (`'~/src/app'`), or a home-relative path (`src/app`). Quote `~`
so your workstation's shell does not expand it to your host home directory.
Run [`sand land NAME`](#sand-land-name) to list cached checkout paths. Paths
outside the guest home are rejected before the VM is deleted.

Use `--preserve-home` to keep your files while updating a working VM. The
home is copied back before the provisioning playbook runs, so Ansible can
update the files it manages. The rebuilt VM keeps its new
`~/.ssh/authorized_keys` so `sand` can still connect. Preserving the whole
home copies more data than preserving individual directories.

Preserved data passes through a private (`0700`) directory on your
workstation. After a successful reset, `sand` removes that copy. If the
reset fails before attempting to delete the VM, it also removes the copy:
the original VM still has the data. If deletion has been attempted, `sand`
keeps the archives and prints their path for recovery.

**Do not preserve data from a VM you suspect is compromised.** The copy can
include credentials and anything an agent wrote in the selected directories.
See [Security Model](../reference/security-model.md).

Files outside the directories you preserve are deleted. Nothing outside the
guest home, such as `/srv` or `/opt`, can be preserved by these options.

### Watching a copy run

Large directories can take several minutes to back up and restore. `sand`
reports progress in the CLI output, the TUI progress log, and the VM tile:

```
==> Backing up home…
==> Backing up home: 910 MB, 76 MB/s
==> Backed up home: 1.6 GB in 24s, 68 MB/s
…
==> Restoring home: 59% of 1.6 GB, 82 MB/s
==> Restored home: 1.6 GB in 21s, 78 MB/s
```

Backup progress shows bytes copied and transfer rate. Restore progress also
shows a percentage, because the completed archive's size is known. Copies
that finish before the first reporting interval have no progress updates.

Archives are compressed inside the source VM using `zstd` when available,
or the slower `gzip` otherwise. `sand` reports when it falls back to gzip.
An older source VM may lack zstd; refreshing the base image affects future
clones, not the VM being backed up. `sand create --rebuild` rebuilds the base
before creating a VM.

### Flags

| Flag | Type | Default | Description |
|---|---|---|---|
| `--preserve-agents` | bool | `false` | Keep settings and files for Claude Code, Codex, OpenCode, and Pi. |
| `--preserve-project` | bool | `false` | Keep the project's per-org directory. |
| `--preserve` | string (repeatable) | *(none)* | Keep one more directory inside the guest home. Pass it once per directory. |
| `--preserve-home` | bool | `false` | Keep the entire guest home directory; implies the three flags above. |
| `--cpus` | string (parsed as int) | *this VM's* | vCPUs. |
| `--memory` | string | *this VM's* | RAM, e.g. `16GiB`. |
| `--disk` | string | *this VM's* | Disk size. A clone's disk can grow but never shrink, so a smaller value is not something you can actually get (see [disk sizing](#disk-sizing)). |
| `--hostname` | string | *this VM's* | VM hostname. |
| `--user` | string | *this VM's* | Primary VM user. |
| `--git-name` / `--git-email` | string | *this VM's* | git identity written into the guest. |
| `--locale` | string | *this VM's* | System locale. |
| `--timezone` | string | *this VM's* | IANA timezone. Naming one explicitly makes an unknown zone fatal in the guest, exactly as it does on `sand create`. |
| `--domain` | string | *this VM's* | Domain suffix. |
| `--docker-proxy-host` | string | *this VM's* | Docker registry pull-through proxy host. |
| `--clone-token` | string | *(empty)* | Token for this VM's recorded repo. Tokens are never stored in the managed index, so pass it again to re-clone a private repo — unless `--preserve-project` is keeping the checkout, in which case nothing is cloned. |
| `--profile` | string | *the profile that owns NAME* | Which [Connection Profile](connection-profiles.md) `NAME` lives on. Only needed when the same name exists under more than one enabled profile. |

`NAME` is required (exactly one positional argument), and flags may appear
before or after it.

Omitted flags use the VM's recorded settings. Explicit flags override them,
and the new settings become the defaults for the next reset.

### There is no `--clone-url`

A reset keeps the VM's recorded project URL. This ensures the project named
by a preserve option is the project restored after the rebuild. To work on
another repository, create another VM with `sand create`.
`sand create --recreate --clone-url ...` is also rejected, and the TUI locks
the repository URL during a reset.

There is no `--base-name`: a reset uses the VM's recorded base image. There
is also no `--rebuild`, which replaces the shared base image and belongs to
`sand create`.

### Secrets are re-applied

A reset ends by writing the VM's host-stored [secrets](secrets.md) into the
rebuilt guest, so the VM comes back with the environment it had. `sand create`
does the same after a build, and additionally records `--clone-token` as the
VM's `GH_TOKEN` secret so it can be rotated later without a rebuild — the same
thing the TUI has always done with the create form's token.

### Verified `--help` output

```
$ sand reset --help
Usage: sand reset NAME [flags]

Delete a sand-managed VM and clone it fresh from its base image, keeping its
name, its project, and every setting it was built with. This is the headless
spelling of the TUI's R (Reset).

Everything inside the guest is lost unless you ask for it back:

  --preserve-agents    keep settings, credentials, sessions, and history for
                       Claude Code, Codex, OpenCode, and Pi
  --preserve-project   keep the cloned project's per-org directory (the checkout,
                       its uncommitted work, and the .env alongside it)
  --preserve PATH      keep one more directory inside the guest home — any git
                       checkout or worktree, whether sand cloned it or you did.
                       Repeatable. Run 'sand land NAME' to list what this VM
                       holds. Paths may be absolute (/home/you/src/app), tilde
                       (~/src/app) or home-relative (src/app).
  --preserve-home      keep the WHOLE home directory, then re-run the playbook
                       on top of it. This is the one to use when the VM is fine
                       and you only want an up-to-date build; it implies every
                       flag above.

All of these copy data out of the VM to this host and back in afterwards. Do NOT
preserve anything from a VM you believe is compromised.

Every other flag you omit is taken from the VM's own recorded settings, so
'sand reset web' means "give me this VM back". Pass one to change it:
'sand reset web --disk 200GiB' resizes on the way through.

There is no --clone-url: a reset rebuilds the project this VM already has. To
work on a different repo, create another VM with 'sand create'.

Examples:
  sand reset web                                  # clean rebuild, same settings
  sand reset web --preserve-agents                # keep coding-agent state
  sand reset web --preserve-agents --preserve-project
  sand reset web --preserve ~/src/app --preserve ~/scratch/spike
  sand reset web --preserve-home                  # keep everything, rebuild the OS
  sand reset web --cpus 8 --memory 16GiB          # rebuild bigger

Flags:
  -clone-token string
    	Token for this VM's recorded repo (tokens are never stored in the index; pass it again for a private repo)
  -cpus string
    	vCPUs (default: whatever this VM has)
  -disk string
    	Disk size, e.g. 100GiB (default: whatever this VM has; a clone's disk can grow but never shrink)
  -docker-proxy-host string
    	Docker registry pull-through proxy host (default: whatever this VM has)
  -domain string
    	Domain suffix (default: whatever this VM has)
  -git-email string
    	git user.email written into the VM (default: whatever this VM has)
  -git-name string
    	git user.name written into the VM (default: whatever this VM has)
  -hostname string
    	VM hostname (default: whatever this VM has)
  -locale string
    	System locale (default: whatever this VM has)
  -memory string
    	RAM, e.g. 8GiB (default: whatever this VM has)
  -preserve PATH
    	Keep one more directory PATH inside the guest home (repeatable); run 'sand land NAME' to list this VM's checkouts
  -preserve-agents
        Keep settings and files for Claude Code, Codex, OpenCode, and Pi across the rebuild
  -preserve-home
    	Keep the ENTIRE guest home directory across the rebuild (implies the other --preserve-* flags)
  -preserve-project
    	Keep the cloned project's per-org directory (checkout + .env) across the rebuild
  -profile string
    	Connection profile NAME lives on (only needed when NAME exists under more than one enabled profile)
  -timezone string
    	IANA timezone for the guest (default: whatever this VM has)
  -user string
    	Primary VM user (default: whatever this VM has)
```

## `sand template`

Golden templates are named snapshots of managed VMs. They are scoped to a
Connection Profile, just like VMs.

```sh
sand template snapshot dev golden
sand template list
sand create --name next --template golden
sand template delete golden
```

`snapshot` briefly stops a running source so the clone is consistent, then
restores its original power state. `list` reports size, source, creation date,
and whether the recorded playbook version is current. `delete` warns about
dependent VMs but does not delete them; those VMs keep running but cannot be
reset from the removed template. All subcommands accept `--profile`.

See [Golden Templates](golden-templates.md) for the complete workflow.

## `sand shell NAME`

Attach a shell to `NAME`'s persistent tmux session in the guest. This is the
same attach path the TUI's `S` key uses, so the two entrypoints never drift.

```
Usage: sand shell NAME [--profile <name>] [--cc]

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

--cc attaches in tmux control mode instead. In a terminal that speaks the
protocol, each guest window becomes a native tab, so C-a c opens a real tab
rather than a window drawn inside this one. iTerm2 is the reference
implementation; WezTerm implements a subset; the list is not exhaustive and
sand does not detect your terminal, it just starts a tmux -CC client.
Run it from a plain terminal window: a host tmux pane strips the control-mode
handshake, so --cc refuses when $TMUX is set.

The named VM must already exist and be running (see 'sand' to list instances,
or 'sand create' to make one). If NAME is managed under more than one
connection profile, --profile picks which one to attach to.
```

`NAME` is required (exactly one positional argument); `--profile` and `--cc`
may each appear before or after `NAME`. `sand shell` refuses a VM that does
not exist or is not running.

`--cc` attaches in tmux control mode, so a terminal that speaks the protocol
renders the guest's tmux windows as native tabs. It refuses when `$TMUX` is
set, because a host tmux pane strips the handshake control mode needs — see
[Native terminal tabs with `tmux -CC`](files-and-shells.md#native-terminal-tabs-with-tmux-cc)
for both ways to get native tabs and why you can only have one of them at a
time.

### Cross-profile resolution for `sand shell`

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

## `sand paste-image NAME`

Stage the host clipboard's image on a running VM's guest clipboard, ready for
Ctrl-V inside Claude Code, Codex, OpenCode, or Pi in the guest.

```
Usage: sand paste-image NAME [--profile <name>]

Read the host clipboard image and stage it on NAME's guest clipboard at
<guest-home>/.sand/clip/latest.png, ready for Ctrl-V inside the guest.

The named VM must already exist and be running (see 'sand' to list instances,
or 'sand create' to make one). If NAME is managed under more than one
connection profile, --profile picks which one to target.

If the host clipboard holds no image, nothing is staged and the command
exits non-zero.
```

`NAME` is required (exactly one positional argument); `--profile` may appear
before or after `NAME`. The command requires a running VM. If the host
clipboard contains only text or is empty, it reports "no image on clipboard"
and exits with a non-zero status.

### How it works

When you run `sand paste-image`, sand reads the clipboard **image only** on
the machine running `sand` (your workstation), verifying an image type is
advertised before fetching any bytes. The image is then written into the guest
at a single-slot path in one step. Read-only `xclip` and `wl-paste` shims serve
agents that invoke clipboard commands; a private headless X display serves
agents such as Codex that use the X11 clipboard API directly.

**Security:** The feature is structured to prevent clipboard **text** from
leaking into the guest. It never reads clipboard text; it gates the clipboard
read on an advertised `image/*` type, and the guest shims have no text-serving
path at all.

### Cross-profile resolution for `sand paste-image`

Like `sand shell`, `sand paste-image NAME` resolves which connection profile
you mean using the same logic described above under `sand shell`'s
[Cross-profile resolution](#cross-profile-resolution-for-sand-shell).

## `sand land NAME`

List `NAME`'s git checkouts and their branch/push/PR state, or act on one.
This is the same detection and the same `gh` actions the TUI's `l` (Land)
key uses — see [Landing](files-and-shells.md#landing).

```
Usage: sand land NAME [PATH] [--pr | --web | --review [--clean]] [--profile <name>]

List NAME's git checkouts and their branch/push/PR state, or act on one:

  sand land NAME                list checkouts + branch/push/PR state
  sand land NAME PATH --pr      open a one-shot draft PR for PATH's pushed branch
  sand land NAME PATH --web     open PATH's branch (or PR) in a browser
  sand land NAME PATH --review  review PATH's changes in a browser, served from the VM
  sand land NAME PATH --review --clean
                                the same, discarding any review already saved there

--pr uses the workstation's own 'gh' (never the guest's token). Without gh
it prints the compare URL and, on a terminal, offers to open it; piped or
scripted, it exits non-zero with the URL on stderr so automation can react.
--web never needs gh: it opens a constructed GitHub URL, which redirects to
an existing PR for the branch on its own.

--review needs no pushed branch, no remote and no gh at all: it runs a review
server inside the VM against PATH, opens it in a browser on this machine, and
blocks until you finish the review — which writes review.xml into PATH inside
the VM, where the agent can read it. Nothing leaves the VM. The review tool is
part of every base image; a base older than the tool itself picks it up on the
next 'sand create'.

A review.xml already in PATH is carried into the new review, so comments you
wrote earlier are there to keep, edit or drop. Nothing ever removes that file
on its own, so --clean is how you start over: it deletes the saved review and
its walkthrough sidecar first. A repository cloned during provisioning already
has the review tool's assistant skills. For a repository cloned later, run
`self-review-install-skills PATH` inside the VM; it installs the skills and
keeps them and the review files out of `git status`.

The named VM must already exist and be running (see 'sand' to list
instances, or 'sand create' to make one). If NAME is managed under more than
one connection profile, --profile picks which one to act on.
```

With no `PATH` or flags, `sand land NAME` prints a table (`PATH KIND BRANCH
PUSH PR`) of every checkout the sweep found, including a count of the commits
that exist nowhere but the VM for an unpushed branch (`unpushed (+3)`) and the
PR's number/state when one exists (`#42 open (draft)`).

A branch whose commits are all published somewhere, but which no longer
matches its own pushed copy — the usual result of a rebase — reads `diverged`
rather than `unpushed (+0)`: there is nothing there to lose.

`--pr PATH`, `--web PATH` and `--review PATH` require a `PATH` from that
listing, and are mutually exclusive. `--clean` modifies `--review`: it
discards any review already saved in that checkout before starting, and is
refused (rather than ignored) alongside any other action.

- **`--pr`** and **`--web`** both refuse a checkout that isn't pushed or has
  no recognized remote — there's nothing to open a PR or browser page
  against yet.
- **`--pr`** opens a one-shot **draft PR** via the workstation's own `gh`.
  Without `gh` installed, it instead prints the branch's compare URL: on a
  real terminal it offers to open that URL in a browser (`y`/`N` prompt); in
  a script or pipe (no terminal) it does not prompt — it exits non-zero with
  the compare URL as the only text on stderr, so automation can capture and
  act on it.
- **`--web`** is **gh-free by construction**: it never calls `gh` at all. It
  opens a constructed GitHub compare URL in a browser, which GitHub's own
  routing redirects to an existing open PR for that branch when one exists.
- **`--review`** has no pushed-branch or remote precondition at all —
  reviewing uncommitted or unpushed work is the point. See
  [Reviewing changes in a browser](review.md) for the full workflow.

`sand land` does not commit or push. With `--pr`, it reads the guest's
metadata and calls `gh` on the workstation. With `--review`, the server runs
inside the VM and sends the diff to your browser over a loopback connection.
It does not copy a checkout onto your workstation. See
[Reachability](review.md#reachability).

Submitting a review saves `review.xml` in the guest checkout, or at the
`output-file` path configured for self-review. The agent can read that file
directly. `self-review-install-skills` adds the default review files and
installed skills to the guest user's active global Git excludes file. See
[Keeping review files out of commits](review.md#keeping-review-files-out-of-commits)
if the repository already tracks those files.

## `sand publish NAME PATH [ISSUE]`

Publish `PATH`'s local commits (inside the VM named `NAME`) to the
drupal.org issue fork for issue `ISSUE`, using the workstation's own
drupal.org token — never a credential inside the VM. This is the same
publication logic (`internal/drupalorg`) the TUI's Landing pane offers as
its `publish to drupal.org` row; see
[Publishing to drupal.org](drupalorg-publishing.md) for what it does, what
the confirmation shows, and what to know before you rely on it.

```
Usage: sand publish NAME PATH [ISSUE] [--yes] [--allow-outside-issue-namespace] [--profile <name>]

Publish PATH's local commits (inside the VM named NAME) to the drupal.org
issue fork for issue ISSUE, using the workstation's own drupal.org token —
never a credential inside the VM. Prints the destination and every commit
and file that will change, then asks for confirmation before writing
anything; declining publishes nothing.

ISSUE may be omitted when PATH was cloned from its issue fork: such a
checkout's origin remote is "issue/<module>-<ISSUE>", which already names
the issue, so it is read from there. Give ISSUE explicitly to override that,
or when the checkout's remote is the canonical "project/<module>" repository
and so names no issue at all.

The named VM must already exist and be running (see 'sand' to list
instances, or 'sand create' to make one). If NAME is managed under more than
one connection profile, --profile picks which one to act on.
```

`ISSUE` is optional. If you cloned the issue fork — the normal way to work
an issue — the checkout's own `origin` already spells the issue out
(`git@git.drupal.org:issue/<module>-<ISSUE>.git`, or the HTTPS spelling
`https://git.drupalcode.org/issue/<module>-<ISSUE>.git`), and `sand publish`
reads it from there, printing the number it derived before the confirmation
so you can see what it settled on:

```
$ sand publish web /home/u/dubbot
sand publish: issue 3619578, read from this checkout's remote
```

Pass `ISSUE` yourself to override that, or when the checkout was cloned from
the canonical `project/<module>` repository — that remote names no issue, so
there is nothing to derive and `sand publish` says so rather than guessing.

`sand publish` refuses immediately, before touching the VM or drupal.org at
all, if no workstation drupal.org PAT is on file — see
[Setup](drupalorg-publishing.md#setup) for where that file goes. Given a
running VM and a PAT, it resolves the destination, collects `PATH`'s change
set, prints the full confirmation (every commit, author, and file
change — never a summary), and then requires an explicit human act before
publishing anything:

- **`--yes`** confirms non-interactively. It is the only sanctioned
  non-interactive route, and it is never read from an environment variable
  — pass it only after you've reviewed the printed confirmation yourself.
  Without it, a non-terminal `stdin` (a script or pipe) refuses outright
  rather than publishing silently; on a real terminal you're prompted
  `[y/N]` instead.
- **`--allow-outside-issue-namespace`** is the only way to let the commit
  destination fall outside the `issue/<module>-<issue>` fork namespace —
  and so the only way to publish straight to a canonical drupal.org project
  rather than an issue fork. This is not the normal path; see
  [Publishing to drupal.org](drupalorg-publishing.md#what-actually-gets-published-and-how)
  for why that guard exists.
- **`--profile`** picks which connection profile `NAME` is resolved on, the
  same as every other command that names a VM.

After a successful publish, `sand publish` fetches the fork branch in the
guest and reports how your checkout compares — the replay creates new commits
with new SHAs, so the two diverge by construction. When their content is
identical and your tree is clean, it offers to reset the checkout onto the
published commits. That offer is a **separate** question from `--yes`, which
confirms only the publish; without a terminal the command to run is printed
instead. See
[Your checkout after a publish](drupalorg-publishing.md#your-checkout-after-a-publish).

If every local commit is already in the checkout's upstream branch or the
canonical project's base branch, `sand publish` reports nothing to publish
and exits without prompting. It first reads the destination anonymously
from drupal.org to identify that base branch.

If the range contains a **merge commit**, `sand publish` names it and stops
without publishing anything. Rebase onto the project's base branch before
your first publish. See
[Rebase onto the base branch](drupalorg-publishing.md#rebase-onto-the-base-branch-dont-merge-it-in).

The printed report lists every change-set commit in order — its status
(`landed`, `already-present`, `failed`, or `not-attempted`) and its SHA on
the fork where it has one — followed by the merge request's URL (opened, or
already open) and any warnings. A replay that fails partway is reported
exactly as far as it got; re-running `sand publish` is how you recover, and
[A failure partway](drupalorg-publishing.md#a-failure-partway-leaves-earlier-commits-public-and-there-is-no-rollback)
explains why that — rather than repairing the fork by hand — is the
supported path.

Publication only ever appends to the fork's branch; `sand publish` has no
force push and cannot rewrite or remove a commit it already published. That
also means rewriting your local history after a publish is not supported —
an amended commit whose message and author are unchanged is reported
`already-present` and its amendment is never sent. See
[There is no force push](drupalorg-publishing.md#there-is-no-force-push-the-fork-branch-only-ever-grows).

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

# CLI Reference

There are six entry points:

- [`sand`](#sand) — no arguments — launches the interactive TUI.
- [`sand create`](#sand-create) — headless, non-interactive VM provisioning.
- [`sand reset NAME`](#sand-reset-name) — rebuild an existing VM from its base
  image, optionally keeping the Claude login or the project.
- [`sand shell NAME`](#sand-shell-name) — attach to a running VM's persistent
  tmux session.
- [`sand paste-image NAME`](#sand-paste-image-name) — stage an image from the
  host clipboard on a running VM's guest clipboard.

- [`sand land NAME`](#sand-land-name) — list a VM's git checkouts, or open a
  draft PR / browser for one of them.
- [`sand version`](#sand-version-sand-version) / `sand --version` — print
  the build identity.

Any other first argument is an unknown subcommand and exits `2`.

Each headless command is the same verb as a TUI key, under the same name and
sharing the same implementation: `sand create` is the form behind `n`, `sand
reset` is `R`, `sand shell` is `S`, `sand land` is `l`, `sand paste-image` is
`v`. Whichever you reach for, the gates, defaults and bookkeeping are the same.

## `sand`

Run with no arguments, `sand` launches the interactive TUI: it lists
instances, streams a build's progress, and drives the same create/reset/
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
| `--timezone` | string | *the timezone this host is in* | IANA timezone for the guest, e.g. `America/Toronto`. See [timezone](#-timezone-follows-the-host) below. |
| `--domain` | string | `lan` | Domain suffix. |
| `--docker-proxy-host` | string | *(empty — disabled)* | Docker registry pull-through proxy host. Optional; when set, `sand` also forces on `devtools_docker_registry_proxy_enabled`. |
| `--clone-url` | string | *(empty — no clone)* | HTTPS repo to clone into the VM. Optional. |
| `--clone-token` | string | *(empty)* | Token for `--clone-url` (e.g. a GitHub PAT). Optional; see [credential handling](#-clone-token-is-a-credential) below. |
| `--recreate` | bool | `false` | Delete and re-clone `--name` if it is **sand-managed**. The older spelling of [`sand reset NAME`](#sand-reset-name), which does the same thing and can additionally preserve state — see [`--rebuild` vs `--recreate`](#-rebuild-vs-recreate). |
| `--rebuild` | bool | `false` | Delete and rebuild the base image first, then create. |
| `--profile` | string | the last-used [Connection Profile](connection-profiles.md), else `local` | Which connection profile to create the VM on. Only that one profile is built and preflighted — the rest of your fleet is untouched. A named profile that doesn't exist, or is disabled, is a validation error. |
| `--with-claude` | bool | `true` | Install Claude Code in the base image. |
| `--with-codex` | bool | `false` | Install OpenAI Codex in the base image — the one **opt-in** toolset flag. |
| `--with-ddev` | bool | `true` | Install DDEV in the base image. |
| `--with-go` | bool | `true` | Install the Go toolchain in the base image. |
| `--with-java` | bool | `true` | Install a headless JDK in the base image. |

The `--with-*` flags configure the **shared base image**, not the individual
VM. A flag you don't pass adopts whatever the existing base was actually
built with (read back from its version stamp), so you only need to state a
selection once; passing a flag explicitly always wins.

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
- **`--recreate`** deletes and re-clones **this VM** (`--name`) from the
  (possibly still-old) base image. It is the older spelling of
  [`sand reset NAME`](#sand-reset-name) and does exactly what that command does
  with neither preserve flag: the same managed-VM gate, the same recorded
  settings, the same bookkeeping. Prefer `sand reset` in new scripts — it is the
  same verb the TUI calls Reset, and it is the only spelling that can keep
  anything from inside the guest.

  A recreate rebuilds a VM `sand` already knows, so **every flag you leave off
  comes from that VM's own recorded settings** — its base image, sizing,
  hostname, git identity and clone URL — rather than from this command's
  defaults. That makes `sand create --recreate --name mybox` mean "give me this
  VM back", not "give me a default VM with this name". A flag you *do* pass
  still wins, so `--recreate --disk 200GiB` remains the way to resize one.

  `--clone-url` is the exception: `--recreate --clone-url ...` is **refused**.
  A rebuild keeps the VM's project, and asking to change it in the same breath
  used to leave the old checkout stranded beside a freshly cloned different
  repo. Rebuild the VM as it is with `sand reset mybox`, or create another VM
  for the other repo. (The TUI's reset form locks the same field.)

  The one thing that is *not* remembered is `--clone-token`: tokens are never
  written to the managed index (see [credential
  handling](#-clone-token-is-a-credential)). If the recorded `--clone-url`
  points at a private repo, pass `--clone-token` again or the clone inside the
  VM will fail; `sand` warns when it reuses a recorded URL with no token.

  What a recreate does **not** do is preserve anything inside the guest: the
  disk is deleted. To keep the Claude Code login or the project checkout across
  a rebuild, use [`sand reset`](#sand-reset-name)'s `--preserve-claude` /
  `--preserve-project`, or the TUI's equivalent toggles ([The
  TUI](tui.md#resetting-a-vm)).

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
    	Destroy the base image and rebuild it from scratch before creating (a stale base is otherwise converged in place)
  -recreate
    	Delete and re-clone the named instance if it is sand-managed. The older spelling of 'sand reset NAME', which does the same thing and can also preserve the Claude login or the project across the rebuild
  -timezone string
    	IANA timezone for the guest, e.g. America/Toronto (default: the timezone this host is in)
  -user string
    	Primary VM user
  -with-claude
    	Install Claude Code in the base image (default true)
  -with-codex
    	Install OpenAI Codex in the base image
  -with-ddev
    	Install DDEV in the base image (default true)
  -with-go
    	Install the Go toolchain in the base image (default true)
  -with-java
    	Install a headless JDK in the base image (default true)
```

(`--user` has no printed default because it is resolved to the host username
*after* flags are parsed, not at registration time — see the flags table
above.)

## `sand reset NAME`

Delete a sand-managed VM and clone it fresh from its base image, keeping its
name, its project, and every setting it was built with. This is the headless
spelling of the TUI's `R` ([Resetting a VM](tui.md#resetting-a-vm)) — same
gate, same defaults, same preserve options.

```sh
sand reset web                                    # clean rebuild, same settings
sand reset web --preserve-claude                  # keep the Claude Code login
sand reset web --preserve-claude --preserve-project
sand reset web --cpus 8 --memory 16GiB            # rebuild bigger
```

It is **gated**: `sand` refuses a target that is not a sand-managed VM, since a
reset clones from a sandbar base image and would otherwise replace whatever
instance it was pointed at. Ownership is resolved from the VM's provenance
marker first, then the managed index — the same resolution `sand shell` uses,
so a VM created by another controller on the same host is still resettable.

Everything inside the guest is destroyed unless you ask for it back:

| Flag | What survives |
|---|---|
| `--preserve-claude` | `~/.claude` and `~/.claude.json` — the Claude Code login and its history. |
| `--preserve-project` | The cloned project's per-org directory: the checkout, its uncommitted work, and the `.env` beside it. Also **skips the re-clone**, so a private repo needs no token. |

Both copy that data out of the VM to a private (`0700`) host temp directory and
restore it into the rebuilt VM, then delete the copy. **Do not preserve
anything from a VM you believe is compromised** — you would be copying its
Claude Code login and its project token onto your workstation. See
[Security Model](../reference/security-model.md).

Nothing outside `~/.claude` and the cloned project's org directory survives: a
second org's checkouts, other forges, `~/.ssh`, `~/.config/gh`, shell history
and anything under `/srv` are all gone with the disk.

### Flags

| Flag | Type | Default | Description |
|---|---|---|---|
| `--preserve-claude` | bool | `false` | Keep the Claude Code login and history. |
| `--preserve-project` | bool | `false` | Keep the project's per-org directory. |
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

**A flag you omit means "whatever this VM already was"**, taken from its own
recorded settings rather than from any default — which is what makes `sand
reset web` mean "give me this VM back". A flag you pass wins, and the result is
recorded, so the *next* reset defaults to what this one produced.

### There is no `--clone-url`

A reset rebuilds the VM it is pointed at, project included. Pointing it at a
different repo would make it a different VM: the preserve option is named for
the org directory the VM *has*, so a changed URL means asking to keep a tree
and having the old one discarded, and even without preserving you would get the
new repo cloned beside a stranded old checkout. To work on another repo, make
another VM with `sand create`. `sand create --recreate --clone-url ...` is
refused for the same reason, and the TUI's reset form locks the same field.

There is no `--base-name` either (the base comes from the VM's own provenance
record — resetting onto a different base is a create), and no `--rebuild`: that
one acts on the **shared** base image every other VM clones from, so it stays on
`sand create` where it cannot be a side effect of rebuilding one VM.

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

  --preserve-claude    keep ~/.claude and ~/.claude.json (the Claude Code login
                       and its history)
  --preserve-project   keep the cloned project's per-org directory (the checkout,
                       its uncommitted work, and the .env alongside it)

Both copy data out of the VM to this host and back in afterwards. Do NOT
preserve anything from a VM you believe is compromised.

Every other flag you omit is taken from the VM's own recorded settings, so
'sand reset web' means "give me this VM back". Pass one to change it:
'sand reset web --disk 200GiB' resizes on the way through.

There is no --clone-url: a reset rebuilds the project this VM already has. To
work on a different repo, create another VM with 'sand create'.

Examples:
  sand reset web                                  # clean rebuild, same settings
  sand reset web --preserve-claude                # keep the Claude login
  sand reset web --preserve-claude --preserve-project
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
  -preserve-claude
    	Keep ~/.claude and ~/.claude.json (Claude Code login + history) across the rebuild
  -preserve-project
    	Keep the cloned project's per-org directory (checkout + .env) across the rebuild
  -profile string
    	Connection profile NAME lives on (only needed when NAME exists under more than one enabled profile)
  -timezone string
    	IANA timezone for the guest (default: whatever this VM has)
  -user string
    	Primary VM user (default: whatever this VM has)
```

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
Ctrl-V inside Claude Code in the guest.

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
at a single-slot path in one step, where a pair of lightweight shims named
`xclip` and `wl-paste` serve it to Claude Code's native paste probe.

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
Usage: sand land NAME [PATH] [--pr | --web] [--profile <name>]

List NAME's git checkouts and their branch/push/PR state, or act on one:

  sand land NAME                list checkouts + branch/push/PR state
  sand land NAME PATH --pr      open a one-shot draft PR for PATH's pushed branch
  sand land NAME PATH --web     open PATH's branch (or PR) in a browser

--pr uses the workstation's own 'gh' (never the guest's token). Without gh
it prints the compare URL and, on a terminal, offers to open it; piped or
scripted, it exits non-zero with the URL on stderr so automation can react.
--web never needs gh: it opens a constructed GitHub URL, which redirects to
an existing PR for the branch on its own.

The named VM must already exist and be running (see 'sand' to list
instances, or 'sand create' to make one). If NAME is managed under more than
one connection profile, --profile picks which one to act on.
```

With no `PATH` or flags, `sand land NAME` prints a table (`PATH KIND BRANCH
PUSH PR`) of every checkout the sweep found, including an ahead count for an
unpushed branch (`unpushed (+3)`) and the PR's number/state when one exists
(`#42 open (draft)`).

`--pr PATH` and `--web PATH` require a `PATH` from that listing, and are
mutually exclusive. Both refuse a checkout that isn't pushed or has no
recognized remote — there's nothing to open a PR or browser page against
yet.

- **`--pr`** opens a one-shot **draft PR** via the workstation's own `gh`.
  Without `gh` installed, it instead prints the branch's compare URL: on a
  real terminal it offers to open that URL in a browser (`y`/`N` prompt); in
  a script or pipe (no terminal) it does not prompt — it exits non-zero with
  the compare URL as the only text on stderr, so automation can capture and
  act on it.
- **`--web`** is **gh-free by construction**: it never calls `gh` at all. It
  opens a constructed GitHub compare URL in a browser, which GitHub's own
  routing redirects to an existing open PR for that branch when one exists.

`sand land` never pushes, commits, or otherwise touches the checkout's
working state — it only reads what the guest already has and, for `--pr`,
calls `gh` on the workstation.

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

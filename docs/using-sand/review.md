# Reviewing changes in a browser

Claude Code can write a lot of code in a VM before you've looked at any of
it. Reviewing it the usual way means pushing a branch and opening a PR —
fine once the work is done, but awkward for a mid-flight look at
uncommitted or unpushed changes. `sand land NAME PATH --review` skips all of
that: it opens the real diff of that checkout in a browser, running entirely
against what's on disk in the VM right now.

Under the hood this is [`@self-review/serve`][serve], the browser front end
of the [self-review][upstream] project, installed in the VM and pointed at
one checkout. `sand` supplies the plumbing — starting it, bridging its port,
opening your browser, cleaning up after it — and nothing else.

[serve]: https://www.npmjs.com/package/@self-review/serve
[upstream]: https://github.com/e0ipso/self-review

## Getting a VM that has it

Nothing. It's part of every base image — not a `--with-*` selection, because
a pinned 17 MB npm package with no build step, which runs only when you ask
for a review, isn't worth a knob. (The tools that do have knobs are the ones
that cost hundreds of megabytes: Go, Java, Codex.)

If your base predates the tool, the next `sand create` picks it up on its own:
adding it changed the playbook, and a base built from an older playbook is
brought up to date in place before the VM is cloned from it. One slower
create, then back to normal.

## Opening a review

From the command line, against a checkout `sand land` already knows about
(run `sand land NAME` with no path to list them):

```sh
sand land NAME PATH --review
```

or, in the TUI, press `l` on a VM's tile to open the [Landing pane](files-and-shells.md#landing),
select a checkout row, and press `v`. Either entry point runs the same code
underneath, so the two are equivalent — the CLI blocks the terminal while a
review is open, and the row shows `reviewing…` (followed by the URL, once the
server has reported one) while the TUI one is.

`--review` has none of `--pr`'s or `--web`'s preconditions: no pushed
branch, no configured remote, no `gh`. Reviewing uncommitted or unpushed
work is the point.

What happens next:

1. `sand` starts the review server inside the VM, in that checkout. The diff
   it reviews starts at the point your own work begins: the parent of the
   oldest commit that exists on no remote, which is what the change would
   land as, uncommitted and untracked files included. When every commit is
   already published, it falls back to the nearest merge base with a trunk
   branch (`main`, `master`, or the remote's own default).

    sand prints the base it chose, its age and its size before the server
    starts — `everything since 6ac1f20b1e77 (2026-09-18, 5 commits, 12
    files)` — so an unexpected range is visible immediately rather than after
    a browser tab opens onto it. A range so large the review tool cannot load
    it is refused outright, with that same line naming the commit to check.
2. The server picks a free port inside the VM and prints the URL it's on.
   `sand` reads that port back, makes it reachable from your workstation
   (see [Reachability](#reachability)), waits for it to answer, and opens it
   in your default browser.
3. The page renders the checkout's file tree and diff.

If no browser opens — a headless SSH session, a locked-down desktop — the
URL is printed and the review stays up. Open it by hand.

Two `sand land --review` commands, against checkouts on the same VM or on
different ones, can run at the same time: each server picks its own port, so
they don't collide. The **TUI runs one review at a time** — pressing `v`
while another is still in flight (including one you just cancelled, which
takes a moment to tear down) says so in the session log and does nothing
else.

## Finishing a review

Leave comments in the browser as usual, then click **Finish Review**. The
server writes `review.xml` into that checkout, *inside the VM*, and exits.
The command that opened the review notices the exit, reports where the file
landed, and returns control of your terminal (or, in the TUI, clears the
row's `reviewing…` state):

```
review written to /home/claude/checkouts/my-repo/review.xml in myvm
```

Closing the browser tab does **not** finish the review — nothing tells the
server you left, and your comments live only in that page until you submit
them. Ctrl-C (or leaving the TUI's Landing pane) tears the session down and
discards them, which is the same trade the upstream tool makes.

## Picking up where you left off

Nothing ever deletes `review.xml`. Upstream has no notion of a review being
"done" — finishing one writes the file and stops the process, and that is the
whole lifecycle. So a checkout you have reviewed before still has that file
in it the next time you look.

`sand` carries it in. Start a review on a checkout that already holds a
`review.xml` and its comments are loaded, along with which files you had
marked as viewed:

```
carrying in the comments already in review.xml
[serve] Resumed 7 comments and 4 viewed files from /home/claude/…/review.xml
```

You can then keep them, edit them, or delete them in the browser, and
**Finish Review** writes the result back over the same file.

To **start over instead**, discarding what was saved:

| Where | How |
| ----- | --- |
| TUI's Landing pane | `V` (shift-V) on the checkout's row |
| CLI | `sand land NAME PATH --review --fresh` |

Both delete `review.xml` and its `review.guide.xml` sidecar before the server
starts. `V` asks first — those comments exist nowhere else, and it asks
whether or not a file is actually there, because checking would cost a round
trip into the VM on a keypress.

## The assistant skills

Upstream ships three skills that bracket a review, and `sand` installs them
into the checkout as the review starts:

| Skill | What it does |
| ----- | ------------ |
| `self-review-critique` | Pre-generates a review for you to curate, rather than starting from a blank page |
| `self-review-guide` | Writes the `review.guide.xml` walkthrough sidecar that turns the file tree into ordered reading groups |
| `self-review-apply` | Reads a finished `review.xml`, prioritises the comments, and makes the changes |

They land in `.agents/skills/` inside the checkout — upstream's own
vendor-neutral layout, which Claude Code, Codex and OpenCode all read. That
is per-checkout rather than installed once per VM because
[upstream issue #162](https://github.com/e0ipso/self-review/issues/162) is
still open, and because the skills reference their own schema files by a
checkout-relative path, so a copy anywhere else would break their
instructions.

The version is pinned to the same release as the review server, and the whole
set is baked into the base image, so installing costs no network at review
time.

A typical loop, all inside the guest:

```
/self-review-critique --staged     # in the agent: pre-generate a review
```

then `v` in the Landing pane to curate what it wrote, and afterwards:

```
/self-review-apply review.xml      # in the agent: act on what you kept
```

!!! note "A copy your project tracks is never touched"

    If the repository already has its own `.agents/skills/self-review-*`
    under version control, `sand` leaves it exactly as it is. Overwriting a
    tracked file would put an edit you never asked for into your working
    tree.

### None of it shows up in `git status`

`review.xml`, `review.guide.xml` and the three installed skill directories
are added to the **guest user's global git excludes**
(`~/.config/git/ignore`), not to any project's `.gitignore` — `sand` does not
write a tracked file into your repository to tidy up after itself.

Git reads that file with no `core.excludesFile` set at all, so nothing
conflicts with a value you have configured yourself.

## Feeding it back to the agent

`review.xml` lands in the checkout it reviewed, inside the VM — the same
place Claude Code is already working. Point the agent at it (`cat
review.xml` in the guest shell, or just tell it the file exists) and it can
read your comments directly from the working tree it's sitting in. Nothing
is copied to your workstation; the round trip stays entirely inside the VM
except for the browser tab rendering it.

## Reachability

The server inside the VM always binds `127.0.0.1` — never a VM-wide
address — so a review is never reachable from anything on the VM's network.
It also refuses any request that doesn't name a loopback host, which is what
stops a web page you happen to be visiting from reaching it. How the
connection gets there depends on where the VM lives:

- **Local Lima** needs nothing extra: Lima already forwards every guest
  loopback port to the same port on your machine's own loopback (the same
  mechanism [Web Servers and Ports](web-servers.md) describes), so the
  server is reachable the moment it starts listening.
- **Remote Lima and Proxmox** each start a short-lived `ssh -L` process for
  the duration of the review, bridging a free port on your workstation's
  loopback to the remote host's (where Lima has already landed the guest
  port) or straight to the guest (Proxmox). It's torn down — along with the
  guest server — when the review ends or you cancel with Ctrl-C.

The reviewed code itself never crosses that boundary: only the rendered diff
and your comments do, over a connection that terminates on your own machine's
loopback.

!!! warning "On a shared remote Lima host, the review is readable by that host's other users"

    The local-Lima case really is workstation-only. Remote Lima is not, and
    the difference is worth knowing before you review sensitive work.

    Lima's automatic forwarding runs on the machine the VM lives on. On a
    remote host it therefore publishes the guest's port to *that host's*
    `127.0.0.1` — which is where sandbar's `ssh -L` then connects. For as long
    as the review is open, anyone else logged into that remote host can reach
    it: `curl http://127.0.0.1:<port>/api/diff` returns the full diff,
    including your uncommitted work, with no authentication. The review server
    has no notion of accounts.

    This does not apply to local Lima (the "remote host" is your own machine)
    and does not apply to Proxmox, where the tunnel runs straight to the
    guest's own sshd with no intermediate loopback publication. If you share a
    remote Lima host with people who should not read the work in progress,
    review it from a VM on a host you do not share.

### If the review never becomes reachable

On **local Lima** the guest and host port numbers are necessarily the same,
because that's how Lima's forwarding works — and the guest picks that number
itself, from whatever was free *in the VM*. If something on your own machine
already holds it, Lima can't bind the forward and the review times out
waiting to answer. `sand` says so, and names the port. Running the review
again picks a different one; there's nothing to configure, because the
upstream server offers no way to request a particular port.

## Limits

Resuming is looked up by the **default** output path only. A project that
redirects `output-file` in its `.self-review.yaml` (or you, in your own
`~/.config/self-review/config.yaml`) has a path that only upstream's config
precedence can resolve, so `sand` leaves that review alone rather than
guessing at it and loading the wrong one.

Everything else the browser UI does — expanding context around a hunk, image
and attachment previews, applying a suggestion, and walkthrough guides picked
up from a `review.guide.xml` sitting next to the output path — is upstream's
and works here, because this is upstream's own server rather than a
re-implementation of it.

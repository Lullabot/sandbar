# Reviewing changes in a browser

Claude Code can write a lot of code in a VM before you've looked at any of
it. Reviewing it the usual way means pushing a branch and opening a PR —
fine once the work is done, but awkward for a mid-flight look at
uncommitted or unpushed changes. `sand land NAME PATH --review` skips all of
that: it opens the real diff of that checkout in a browser, running entirely
against what's on disk in the VM right now.

## Getting a VM that has it

The review UI is an **opt-in** tool, like Codex: it isn't provisioned by
default, because building it costs base-image time that most VMs don't need
to pay.

```sh
sand create --with-review
```

or enable "Install self-review web UI" in the TUI's create form. Like every
`--with-*` flag it configures the **shared base image**: turning it on (or
off) for a VM whose base was built without it invalidates that base, so the
next create rebuilds it before cloning. See
[`--with-*` flags](cli-reference.md#sand-create) in the CLI reference.

## Opening a review

From the command line, against a checkout `sand land` already knows about
(run `sand land NAME` with no path to list them):

```sh
sand land NAME PATH --review
```

or, in the TUI, press `l` on a VM's tile to open the [Landing pane](files-and-shells.md#landing),
select a checkout row, and press `v`. Either entry point runs the same code
underneath, so the two are equivalent — the CLI blocks the terminal while a
review is open, and the row shows `reviewing… (browser open)` while the TUI
one is.

`--review` has none of `--pr`'s or `--web`'s preconditions: no pushed
branch, no configured remote, no `gh`. Reviewing uncommitted or unpushed
work is the point.

What happens next:

1. `sand` starts a review server inside the VM, pointed at that checkout.
   The diff it reviews defaults to the checkout's branch against its merge
   base with the repository's default branch — what the change would land
   as, uncommitted changes included.
2. It makes the server's port reachable from your workstation (see
   [Reachability](#reachability) below) and opens it in your default
   browser.
3. The page renders the checkout's file tree and diff — the same review UI
   this feature is built on, just pointed at a real VM checkout instead of a
   local clone.

Two checkouts — on the same VM or different ones — can be under review at
the same time; each gets its own server and its own port, so they don't
interfere with each other.

## Finishing a review

Leave comments in the browser as usual, then click **Finish Review**. The
server writes `review.xml` into that checkout, *inside the VM*, and exits.
The command that opened the review notices the exit, reports where the file
landed, and returns control of your terminal (or, in the TUI, clears the
row's `reviewing…` state):

```
review written to /home/claude/checkouts/my-repo in myvm
```

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
How the connection reaches it depends on where the VM lives:

- **Local Lima** needs nothing extra: Lima already forwards every guest
  loopback port to the same port on your machine's own loopback (the same
  mechanism [Web Servers and Ports](web-servers.md) describes), so the
  server is reachable the moment it starts listening.
- **Remote Lima and Proxmox** each start a short-lived `ssh -L` process for
  the duration of the review, bridging your workstation's loopback to the
  remote host's (where Lima has already landed the port) or straight to the
  guest (Proxmox). It's torn down — along with the guest server — when the
  review ends or you cancel with Ctrl-C.

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

## What this isn't (yet)

This is a v1 of the review workflow. A few things the underlying review UI
supports upstream are not wired up here: there's no "expand context" beyond
what the diff already shows, no image previews, no accompanying walkthrough
guide, and no `--resume-from` to reopen a previous review. `sand` just
serves the diff and writes the comments back to `review.xml`.

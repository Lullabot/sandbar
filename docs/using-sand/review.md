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

Nothing, for a base built from scratch: it's installed by default, like Claude
Code and DDEV. It's a pinned 17 MB npm package with no build step, which is
cheap enough that every base carries it rather than you discovering at review
time that this one doesn't.

If you don't want it, it's an opt-out like the rest of the tool-set:

```sh
sand create --with-review=false
```

or clear "Install browser review UI" in the TUI's create form. Like every
`--with-*` flag it configures the **shared base image**, so turning it off
(or back on) invalidates that base and the next create reprovisions it
before cloning. See [`--with-*` flags](cli-reference.md#sand-create).

!!! warning "An existing base needs `--with-review` once, explicitly"

    A `--with-*` flag you don't pass adopts whatever the **existing base**
    was built with (see [`--with-*` flags](cli-reference.md#sand-create)),
    and a base stamped before this tool existed recorded a tool-set without
    it. So a plain `sand create` — and `sand create --rebuild`, which reads
    that same stamp before it destroys anything — keeps the base without the
    review tool, and `--review` then fails with "command not found" in the
    guest. Ask for it once, explicitly:

    ```sh
    sand create --with-review NAME
    ```

    (or tick "Install browser review UI" in the TUI's create form). That
    invalidates the base, so the create converges it in place and installs
    the tool. One slower create, then back to normal — later creates adopt
    the new stamp, which now records it.

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
   it reviews defaults to the checkout's branch against its merge base with
   the repository's default branch — what the change would land as,
   uncommitted and untracked files included.
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

`sand` doesn't expose `--resume-from`, so each review starts fresh rather
than carrying a previous `review.xml`'s comments back in. Everything else
the browser UI does — expanding context around a hunk, image and attachment
previews, applying a suggestion, and walkthrough guides picked up from a
`review.guide.xml` sitting next to the output path — is upstream's and works
here, because this is upstream's own server rather than a re-implementation
of it.

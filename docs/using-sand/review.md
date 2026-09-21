# Reviewing changes in a browser

Use `sand land NAME PATH --review` to review a checkout's diff in your
browser, including uncommitted and unpushed changes. The review server runs
inside the VM. Your browser displays the diff, and your comments are saved
back into the checkout for the agent to read.

The browser interface comes from [`@self-review/serve`][serve], part of the
[self-review][upstream] project. `sand` starts the server, forwards its port,
opens your browser, and stops the session when you finish.

[serve]: https://www.npmjs.com/package/@self-review/serve
[upstream]: https://github.com/e0ipso/self-review

## Getting a VM that has it

The review tool is included in every base image and runs only when you open
a review. There is no installation flag to enable.

If your base image predates the tool, the next `sand create` updates the base
before cloning a new VM. Existing VMs need to be reset from an updated base
or replaced to get it.

## Opening a review

The VM must be running. Run `sand land NAME` to list its known checkouts,
then use a path from that list:

```sh
sand land NAME PATH --review
```

In the TUI, press `l` on the VM's tile to open the
[Landing pane](files-and-shells.md#landing), select a checkout, and press `v`.
The CLI keeps the terminal occupied until the review ends. The TUI marks the
row `reviewing…` and adds the URL when the server is ready.

A review does not require a pushed branch, a configured remote, or `gh`.

1. `sand` chooses the diff's base commit. It uses the parent of the oldest
   commit absent from all remote-tracking branches in the guest. If it
   cannot use that parent, it tries the nearest common ancestor with `main`,
   `master`, or the checkout's recorded default branch. If neither method
   finds a base, the review server uses its default range. The diff also
   includes uncommitted and untracked files.

    Before starting the server, `sand` prints the base commit, its date,
    and the number of commits and files to review. For example:
    `everything since 6ac1f20b1e77 (2026-09-18, 5 commits, 12 files)`.
    If the range is too large for the review tool, `sand` stops and names
    the base commit so you can check it.
2. `sand` starts the server in the checkout. The server chooses a free port
   inside the VM. `sand` makes that port reachable from your workstation
   (see [Reachability](#reachability)), waits for a response, and opens your
   default browser.
3. The browser displays the checkout's file tree and diff.

If no browser opens, open the printed URL yourself. The review stays running.

You can run multiple CLI reviews at once, on the same VM or different VMs.
The **TUI runs one review at a time**. Trying to review another checkout
while a session is running or still shutting down leaves a message in the
session log.

## Finishing a review

Leave comments in the browser, then click **Finish Review**. The server
writes `review.xml` into the checkout inside the VM and exits. The CLI
reports the path and returns control of your terminal; the TUI clears the
row's `reviewing…` state.

```
review written to /home/claude/checkouts/my-repo/review.xml in myvm
```

Closing the browser tab does **not** finish the review or save your comments.
Comments stay in the page until you submit them. Pressing Ctrl-C in the CLI
stops the session without saving them.

In the TUI, **leaving the Landing pane does not end the review**. You can
press `esc`, open a shell with `S`, or visit another VM while the browser
review stays open. To cancel, return to the checkout's row and press `v`;
the footer reads `v cancel review`. Quitting `sand` also stops the review,
with a confirmation naming the checkout.

## Picking up where you left off

A finished review leaves `review.xml` in the checkout. When you open another
review there, `sand` loads the saved comments and viewed-file markers:

```
carrying in the comments already in review.xml
[serve] Resumed 7 comments and 4 viewed files from /home/claude/…/review.xml
```

You can keep, edit, or delete the comments. **Finish Review** overwrites the
same file with the result.

To discard the saved review and start over:

| Where | How |
| ----- | --- |
| TUI's Landing pane | Press `V` (shift-V) on the checkout's row, labelled `clean review` in the footer. |
| CLI | Run `sand land NAME PATH --review --clean`. |

Both delete `review.xml` and the accompanying `review.guide.xml` before
starting the server. The TUI asks for confirmation, even if neither file
exists. The CLI deletes them without a prompt.

## The assistant skills

`sand` installs three self-review skills into the checkout when a review
starts:

| Skill | What it does |
| ----- | ------------ |
| `self-review-critique` | Drafts review comments for you to check and edit. |
| `self-review-guide` | Writes a `review.guide.xml` walkthrough that groups files in reading order. |
| `self-review-apply` | Reads a finished `review.xml`, prioritises the comments, and makes the changes. |

The skills go in `.agents/skills/` inside the checkout. Their instructions
refer to schema files at that location. They are included in the base image
and match the review server's version, so copying them needs no network
access.

A typical workflow inside the guest:

```
/self-review-critique --staged     # in the agent: draft a review
```

Press `v` in the Landing pane to review and edit those comments, then run:

```
/self-review-apply review.xml      # in the agent: apply the saved feedback
```

!!! note "Skills tracked by your project are kept"

    If the repository already tracks its own `.agents/skills/self-review-*`
    files in Git, `sand` leaves those skills unchanged.

<a id="none-of-it-shows-up-in-git-status"></a>

### Keeping review files out of commits

The base image adds `review.xml`, `review.guide.xml`, and the three skill
directories to the guest user's global Git ignore file,
`~/.config/git/ignore`. It does not change your project's `.gitignore`.

If you have set `core.excludesFile` or use a different Git configuration
location, add the patterns to that ignore file too. Ignore rules do not
hide files that Git already tracks.

## Feeding it back to the agent

Tell the agent to read `review.xml` in the checkout, or use
`/self-review-apply review.xml`. The saved feedback is already inside the VM,
where the agent is working. You do not need to download or copy it.

## Reachability

The server listens on the VM's loopback address, `127.0.0.1`. `sand` makes
it reachable at a loopback address on your workstation:

- **Local Lima** automatically forwards the guest port to the same port
  on your workstation. See [Web Servers and Ports](web-servers.md).
- **Remote Lima** uses a temporary SSH tunnel from your workstation to the
  remote host's loopback port, where Lima has forwarded the guest port.
- **Proxmox** uses a temporary SSH tunnel directly to the guest's loopback
  port.

The guest server and any SSH tunnel stop when you finish or cancel the
review. The diff and your comments travel between the VM and your browser.
`sand` does not copy a checkout onto your workstation or push it to a forge.

!!! warning "Other users of a shared remote Lima host can read the review"

    Lima forwards the review port to `127.0.0.1` on the host running the VM.
    Anyone logged into that host can access the review while it is open,
    including the full diff and uncommitted work. The server does not require
    authentication.

    For local Lima, that host is your workstation. Proxmox connects directly
    to the guest, with no review port on the Proxmox host. If other users of
    a remote Lima host should not see your work, review it on a host you do
    not share.

### If the review never becomes reachable

Local Lima must use the same port number on the guest and workstation. If
another application on your workstation already uses the port the guest
selected, forwarding fails and the review times out. `sand` reports the
port. Run the review again to select another port; the review server has no
option to request a specific one.

## Limits

Automatic resumption uses only the default `review.xml` path. If you set
`output-file` in `.self-review.yaml` or
`~/.config/self-review/config.yaml`, `sand` does not load the saved review
automatically. A clean review also deletes only the default `review.xml`
and `review.guide.xml` files.

The browser uses self-review's interface, including extra context around
changes, image and attachment previews, suggestions, and walkthroughs from
`review.guide.xml` beside the output file.

# Browser review of changes

`self-review-serve` serves a checkout's diff as a review page. It is what
`sand land NAME PATH --review` runs on the user's behalf, and it is installed
in this guest, so you can start it yourself and hand the user a URL. Run it
in the checkout, in its own tmux window or in the background, because it
keeps running until the review is finished:

    self-review-serve [GIT-DIFF-ARGS]

With no arguments it reviews only unstaged changes. To cover a branch's
commits as well, pass a base, for example `self-review-serve main` or
`self-review-serve HEAD~3`. Add `--resume-from review.xml` to carry in an
earlier review's comments.

It chooses a free port, binds `127.0.0.1`, and prints
`Review ready at http://127.0.0.1:PORT/` to stderr. When the user clicks
**Finish Review**, it writes `review.xml` in the checkout and exits; read
that file for their comments. Closing the tab saves nothing.

Whether that URL opens on the workstation depends on the profile type in
[this-vm.md](this-vm.md):

- `local`: Lima forwards the port, so the printed URL works as-is.
- `proxmox` or `remote-ssh`: the server listens only on the guest's
  loopback, which the workstation cannot reach. Stop it and give the user
  `sand land NAME PATH --review` to run on their workstation instead; it
  starts the same server, sets up the forward, and opens the browser.

For a repository cloned after the VM was created,
`self-review-install-skills PATH` installs the review tool's agent skills.

More: <https://lullabot.github.io/sandbar/latest/using-sand/review/>

# Publishing to drupal.org

How a change set you made in a `sand` VM gets from a guest checkout to a
merge request against a canonical drupal.org project — for someone who
knows Drupal contribution, not `sand` internals.

## What this is

A `sand` guest can clone and work against a drupal.org issue fork exactly
as you would on your own machine, using plain `git`. What it cannot do is
push: no guest ever holds a drupal.org credential (see
[Security Model](../reference/security-model.md#publishing-to-drupalorg-agent-decides-what-host-decides-where)
for why). Instead, `sand` reads the guest's already-committed local commits
and **publishes them from your workstation**, one commit at a time, using a
drupal.org personal access token (PAT) that never leaves your machine.

You reach this two ways:

- **The Landing pane's `publish to drupal.org` row.** Focus a running VM,
  press `l`, and any checkout pushed to a `git.drupalcode.org` remote offers
  this action (`enter`/`o`) once a workstation PAT is on file. If no PAT is
  on file, the row instead says `on git.drupalcode.org · no drupal.org PAT
  on file, publish disabled` — see [setup](#setup) below.
- **`sand publish NAME PATH ISSUE`** from the command line, for scripting or
  for a bigger confirmation than the TUI's pane can show at once (see
  [`sand publish`](cli-reference.md#sand-publish-name-path-issue) in the CLI
  reference).

Both surfaces do exactly the same thing: they share one implementation of
the destination rules, the confirmation, and the report, so neither can
behave differently from the other.

## Setup

Publication needs a drupal.org account PAT on your workstation, at:

```
${XDG_CONFIG_HOME:-~/.config}/sandbar/drupalorg.token
```

Create it yourself — `sand` never writes this file — with the token as its
only content, and make sure it's mode `0600`:

```console
$ mkdir -p ~/.config/sandbar
$ vi ~/.config/sandbar/drupalorg.token   # paste your token, save
$ chmod 600 ~/.config/sandbar/drupalorg.token
```

`sand` refuses to read a file that's readable by group or other, and
refuses to publish at all — before touching the VM, the checkout, or
drupal.org — if the file doesn't exist. See
[Files and State](../reference/files-and-state.md#host-paths) for why this
path is a fixed convention rather than something you configure.

The token must be a real drupal.org account PAT, created the same way you'd
create one for pushing from your own machine. `sand` does not (and, for
reasons covered [below](#why-publication-is-host-side), cannot easily) issue
you a narrower one.

## What actually gets published, and how

For a checkout `PATH`, publication:

1. Reads the checkout's origin remote and upstream branch to work out which
   module it belongs to and how many local commits are ahead.
2. Collects those commits — in order, oldest first — as an inert list of
   commit messages, authors, and file actions (create/update/delete/move,
   with resulting content). **Merge commits are skipped**: a merge carries
   no file changes of its own, and the destination API this uses has no way
   to express a second parent — the changes a merge brought in still travel,
   as the ordinary commits that made them.
3. Resolves where those commits go: the drupal.org **issue fork** for the
   issue number you give it (`issue/<module>-<nid>`), read anonymously, with
   its canonical parent project (e.g. `project/<module>`) derived from that
   fork's own `forked_from_project` — never guessed, and never something the
   guest's output can influence. By default, a commit destination outside
   the `issue/` namespace is refused outright, because that would mean
   writing straight to a canonical project; overriding that (`sand publish
   --allow-outside-issue-namespace`) is available but is not the normal
   path, and its own guard rail exists for exactly the reason this whole
   design does.
4. Shows you a **confirmation**: the destination and branch, the merge
   request's target, and then every commit in order — its message, its
   author, and every file it touches, with the resulting content shown in
   full (large files are elided with a marked count, never silently
   summarized). Nothing before this point has written anything, on the
   guest or on drupal.org.
5. Waits for an explicit **yes**. On the CLI this is a `y`/`N` prompt, or
   `--yes` after you've reviewed the printed confirmation yourself — never
   an environment variable, and a non-terminal `stdin` without `--yes`
   refuses outright rather than guessing. In the TUI it's the pane's
   `[y] yes [n] cancel` prompt. Declining publishes nothing.
6. **Replays each commit as its own API call** onto the fork — see
   [Replay, not squash](#replay-not-squash-your-local-history-is-what-lands)
   — and then opens a merge request against the canonical parent, or reuses
   one that's already open for that branch.
7. Reports what happened: one line per commit, in order, naming its status
   (`landed`, `already-present`, `failed`, or `not-attempted`) and its SHA
   on the fork where it has one, followed by the merge request's URL and any
   warnings.

## Four things you'll otherwise learn the hard way

### Replay, not squash — your local history is what lands

A publish does not squash your work into one commit and does not push a ref
the way `git push` would. It **replays each local commit as its own,
separate write** to drupal.org's content API, in the order you made them.
The practical upshot: however you organized your work locally — one commit
per fix, a handful of intermediate commits you'd normally rebase away,
whatever your habit is — is exactly what shows up on the fork and in the
merge request. There is no local-history cleanup step performed for you.
If you want a tidier history on drupal.org, tidy it in the guest (`git
rebase -i`, etc.) *before* you publish, the same as you would before any
other push.

### A failure partway leaves earlier commits public, and there is no rollback

Because each commit is its own API call, a replay of five commits where the
third one fails leaves the first two **already public on the fork, and
unrevocable** — there is no rollback, and nothing about this design
attempts one. This is stated plainly rather than hidden: the report always
names exactly what landed, what failed and why, and what was never
attempted, so you know precisely where things stand.

**Recovery is re-running the publish**, not manually fixing up the fork.
`sand publish` (or the Landing row) resumes automatically: it reads what's
already on the branch, recognizes the commits that already landed, and
sends only the remainder — whether the failure was transient (a network
blip, a rate limit) or you fixed something in the guest first. You do not
need to, and should not try to, repair the fork by hand.

### There is no force push — the fork branch only ever grows

`sand publish` never pushes a ref. Every commit goes through drupal.org's
content API, which can only **append** a commit to a branch: there is no
non-fast-forward update, no rewind, and no branch deletion anywhere in this
mechanism. Nothing `sand` can do will rewrite or remove something already
published.

That is deliberate — one human `y` authorizes the writes in front of it, and
a force push would let a single approval destroy public history that an
earlier approval created. But it does mean **rewriting local history after
you have published is not a supported operation**, and it goes wrong in two
different ways depending on what you rewrote:

- **Amend a commit's content while leaving its message and author alone**,
  and resumption recognizes it as already landed. Your amendment is *not*
  published. It's reported as a skipped commit, so it is visible in the
  output — but the fork's copy now quietly differs from yours, and nothing
  will reconcile them for you.
- **Change a commit message, or reorder, drop, or insert a commit**, and the
  match against what's on the branch breaks. The replay then re-sends commits
  the fork already has. It cannot rewrite them, so you get some mixture of
  duplicate commits appended on top and outright failures partway: a create
  for a path that now exists is rejected, and an update whose recorded parent
  has moved comes back as *"the fork moved underneath this publish;
  re-derive the change set and retry"*.

So: **tidy your history in the guest before the first publish, not after**
(see [Replay, not squash](#replay-not-squash-your-local-history-is-what-lands)).
Once commits are public on the fork, treat that history as fixed and add to
it rather than rewriting it.

If you genuinely need published history changed — a secret committed by
mistake, a series too tangled to live with — that is out-of-band work this
tool deliberately does not do for you. Do it from your workstation with your
own git and credentials (`git push --force` to the issue fork), or close the
merge request and delete the branch through drupal.org's web UI. The branch
is derived from the issue rather than from your local state, so a later
`sand publish` targets the same `<module>-<nid>` branch on
`issue/<module>-<nid>` either way. A fresh publish onto a branch you deleted
recreates it from the fork's default branch and replays your change set from
scratch, which is the one clean way back to a history you chose.

### Commits published this way are not GPG-signed

`git push` can carry a GPG-signed commit end to end. Publishing through the
content API cannot: a commit created this way is **not signed**, regardless
of whether your usual git setup signs commits. The commit's author is
whatever the guest recorded; its committer of record on drupal.org is
always the owner of the PAT that published it, not you or the agent that
wrote the code. If your contribution workflow depends on signed commits
landing on a fork, this mechanism does not provide that — pushing over git
yourself remains the only way to get a signed commit onto drupal.org.

## This is a human-initiated action

drupal.org's PAT policy permits automation "of an individual action a user
could already perform" — a single human, confirming a single publish, using
their own credential, is squarely that. It is not a standing, unattended
integration: nothing in `sand` runs a publish without the confirmation step
above, and the token is never used for anything but the one authenticated
call a confirmed publish makes. That framing is `sand`'s own reading of the
policy, offered so you can judge it against your own drupal.org account's
standing — **the Drupal Association has not been formally consulted** about
this specific tool, and this is not a claim that they have endorsed it.

## Why publication is host-side

The obvious alternative — give the guest a drupal.org credential and let it
`git push` directly, the same way Landing lets a guest push to GitHub with
its own least-privilege token — does exist as a *manual* option: you can
create a drupal.org **fine-grained personal access token bounded to a
single issue fork** through GitLab's web UI and place it in the guest
yourself, the same way you'd hand-configure any other credential. That
narrower token genuinely works for the one fork it names.

`sand` does not automate that, for one concrete reason: **GitLab exposes no
API that can create a fine-grained token.** Its self-service endpoint,
`POST /user/personal_access_tokens`, accepts only the `k8s_proxy` and
`self_rotate` scopes and takes no access-boundary parameter at all, so there
is nothing for `sand` to call. Creation is a web-UI act; only *rotation* is
automatable. That is a property of GitLab itself, not of drupal.org.

Two other routes to a narrow token are closed as well, and they are closed
for different reasons worth keeping straight:

- A **project access token** is a different mechanism, and it does have a
  creation endpoint — but it requires Maintainer on the project, and
  drupal.org grants everyone only Developer on an issue fork, including a
  module's own maintainer on a fork they created themselves. drupal.org
  additionally blocks that endpoint at its edge, for every caller (along
  with deploy tokens and deploy keys — see
  [Security Model](../reference/security-model.md#publishing-to-drupalorg-agent-decides-what-host-decides-where)).
- A **classic** personal access token cannot be scoped to a project at all.

So a fine-grained token has to be made by hand, in the web UI, one issue
fork at a time — and a manual web-UI trip per issue is exactly the friction
publication exists to remove. Host-side publication, with no drupal.org
credential in the guest at all, ever, is what `sand` builds instead.

This is worth stating precisely, because "the secure option exists but
cannot be automated" is a much less obvious conclusion than "the secure
option does not exist" — and because the condition for reopening the
decision follows from it: if GitLab ever exposes fine-grained token creation
by API, or makes a group-level boundary selectable by non-members, the
in-guest loop becomes cheap and this choice should be revisited.

This page deliberately does **not** walk through setting up that manual,
per-fork token yourself as a supported escape hatch. Before that can be
written up as safe, a specific negative control needs to have been run and
passed: a token bounded to one issue fork must be shown to **fail** to push
to a canonical `project/<module>` (as opposed to only its own fork). That
control has not been run — it needs a real drupal.org account and a
hand-made fine-grained token — so the instructions are withheld rather than
published untested. If you set this up for yourself in the meantime, know
that you are relying on drupal.org's per-fork token scoping doing what it's
documented to do, unverified by this project.

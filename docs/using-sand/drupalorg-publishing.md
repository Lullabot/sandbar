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
  press `l`, and any checkout on a drupal.org remote — `git.drupalcode.org`
  over HTTPS or `git.drupal.org` over SSH — offers this action (`enter`/`o`)
  once a workstation PAT is on file. If no PAT is on file, the row instead
  says `on drupal.org · no PAT on file, publish disabled` — see
  [setup](#setup) below.
- **`sand publish NAME PATH [ISSUE]`** from the command line, for scripting or
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

1. Reads the checkout's origin remote and upstream branch to identify the
   module and issue. See
   [Where the issue number comes from](#where-the-issue-number-comes-from).
2. Looks up the issue fork (`issue/<module>-<nid>`) anonymously on drupal.org.
   The fork's `forked_from_project` identifies the canonical parent project,
   such as `project/<module>`. By default, `sand` refuses to write outside
   the `issue/` namespace. The CLI flag `--allow-outside-issue-namespace`
   explicitly overrides that restriction.
3. Collects local commits, oldest first, excluding commits already in the
   checkout's upstream branch or the canonical project's base branch. The
   collected data contains commit messages, authors, and file changes,
   including their content. If the range contains a **merge commit**, `sand`
   names it and stops without publishing anything. See
   [Rebase onto the base branch](#rebase-onto-the-base-branch-dont-merge-it-in).
4. Shows you a **confirmation**: the destination and branch, the merge
   request's target and **title** (see
   [How the merge request is titled](#how-the-merge-request-is-titled)), and
   then every commit in order — its message, its
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
7. Reconciles your checkout with what just landed — see
   [Your checkout after a publish](#your-checkout-after-a-publish).
8. Reports what happened: one line per commit, in order, naming its status
   (`landed`, `already-present`, `failed`, or `not-attempted`) and its SHA
   on the fork where it has one, followed by the merge request's URL and any
   warnings.

## Where the issue number comes from

The normal way to work a drupal.org issue is to clone its **issue fork**, so
the checkout's own `origin` is already `issue/<module>-<nid>` —
which names both the module and the issue. Publication reads the number
straight out of that remote, and neither surface asks you for it:

- **The Landing pane** resolves the remote first and goes directly to the
  confirmation. The issue prompt appears only if the remote cannot answer.
- **`sand publish NAME PATH`** takes no `ISSUE` argument in this case, and
  prints the number it derived (`issue 3619578, read from this checkout's
  remote`) above the confirmation.

You are asked only when the checkout was cloned from the canonical
`project/<module>` repository instead. That remote names no issue, so there
is nothing to derive:

- **The Landing pane** shows its issue prompt. A bare number, a `#`-prefixed
  one, and a pasted drupal.org issue URL are all accepted.
- **`sand publish`** needs the `ISSUE` argument, and says so rather than
  guessing.

Deriving the issue removes a **question**, never a **confirmation**. The
destination it works out is still rendered in full, and still has to be
accepted, before anything is written — see step 4 above. If the derived
destination is not the one you want, decline: on the CLI, re-run with an
explicit `ISSUE`; in the Landing pane, the prompt is seeded with the derived
number whenever the flow returns to it, so you can edit it there.

## How the merge request is titled

A merge request is titled after its **issue**, using drupal.org's own
convention:

```
Issue #3619578: Fix very slow Overview page loads
```

The title is read from the issue node on drupal.org — an anonymous,
credential-free lookup of a public page, on the same host you filed the issue
on. It is never taken from the commits: a title derived from the payload
would put a guest agent's prose at the top of a permanent, public proposal,
and would change between a first publish and a resumed one. That is the same
rule the destination itself follows.

If drupal.org can't vouch for a title, the merge request falls back to the
branch name (`dubbot-3619578`) — the title publication used before. That
happens when:

- the lookup fails or times out (drupal.org is down, slow, or its edge
  refuses the request), or
- the node isn't an issue at all, or
- **the issue belongs to a different project than the one you're publishing
  to.** An issue number that resolves to some other project's issue is not
  this publication's issue whatever its number, and a confidently wrong title
  is worse than a terse one.

None of these fail the publish. The lookup is bounded separately from the
rest of the flow and is allowed to come back empty; a title is a convenience,
and nothing about it blocks a publish. Whichever title you end up with is
printed in the confirmation before you accept it, so you always see the
headline your proposal will carry.

## Your checkout after a publish

Because publication **replays** commits rather than pushing them, every
commit that lands is a brand-new commit object: a new SHA, a new committer of
record (the owner of the PAT), a new timestamp. Your checkout still holds the
originals. So the moment a publish succeeds, your local branch and the fork
hold **the same content under completely different commits, with nothing in
common**.

That is expected, and resuming a publish already copes with it — commits are
matched by message and author, never by SHA, which is what makes resumption
work at all. But your checkout doesn't know, and left alone it goes wrong in
the usual way: `git log origin/<branch>` shows a stale picture, and a later
`git pull` merges the two histories into a pile of duplicated commits.

So after every successful publish, `sand` **fetches** the fork branch in the
guest and tells you where you stand:

```
this checkout and the fork hold the same content under different commits
(3 local, 3 published) — publication replays commits rather than pushing
them, so the SHAs differ
  published: 891e13e…
  local:     9c2d5f2…
```

The fetch is a read. It moves no branch and touches no file, so it happens
without asking. Its value is that `git diff HEAD FETCH_HEAD` and `git log
origin/<branch>` now tell you the truth about what is public.

### Adopting the published commits

When — and only when — all three of these hold:

- your history and the fork's differ, **and**
- their **content is identical**, **and**
- your working tree is **clean**,

`sand` offers to reset your branch onto the published commits:

```
Reset this checkout onto the published commits? [y/N]
```

In the TUI this arrives as the usual `[y] yes  [n] cancel` confirmation, on
whichever screen the finished publish left you on — the run's progress screen,
the board, or the Landing pane.
Saying yes runs `git reset --hard` onto the fetched commits, leaving your
checkout exactly matching what is public. Nothing is lost: the content is
identical by construction, and the only things discarded are the local commit
objects whose changes are already public under other SHAs.

Say no and nothing happens. This is a **separate decision from the publish**,
with its own answer — `--yes` confirms the publish and never the reset, and
without a terminal `sand publish` prints the command instead of running it.

The offer is withheld, with the reason printed, when:

- **you have uncommitted changes to tracked files.** Publication carries
  committed commits only, so the uncommitted remainder was deliberately left
  behind — a reset would destroy it. Commit or stash it, then publish again.
- **the content genuinely differs.** That is not a SHA-divergence artifact;
  you have real local changes the fork doesn't, and a reset would discard
  them.

**Untracked files do not withhold the offer.** These checkouts live in a VM
whose whole job is running agents over them, so untracked scratch files are the
normal state rather than a warning sign. They also cannot be harmed here: the
offer is only ever made when the fork's tree is *identical* to yours, and a file
that is untracked is in neither tree, so the reset has nothing to collide with.
They are counted in the summary and left exactly where they are.

The reset re-checks both conditions **inside the guest**, immediately before
it acts. A VM runs agent code that can write files at any moment, so a guard
evaluated on the host when the question was asked is a guard against the
past; if anything changed while you were deciding, the reset refuses instead.

Note that this only ever moves you **onto** the fork. The fork branch can
only grow (see [There is no force push](#there-is-no-force-push-the-fork-branch-only-ever-grows)),
so there is no version of this that rewrites drupal.org to match you.

## Publication limits and recovery

### Rebase onto the base branch — don't merge it in

Before your first publish, update an issue branch by rebasing onto the
canonical project's base branch. Replace `<module>` and `2.x` below with
your project and its base branch:

```console
$ git fetch https://git.drupalcode.org/project/<module>.git 2.x
$ git rebase FETCH_HEAD
```

`sand publish` uses an API that creates commits from file changes and cannot
represent a merge commit's second parent. If the commits to publish include
a merge, `sand` names it and stops before writing anything.

To select your work, `sand` excludes commits already in the checkout's
upstream branch and the canonical project's base branch. This keeps
upstream commits introduced by a rebase out of your publication. It reads
the base branch from `project/<module>` because the issue fork's copy may
be out of date.

The guest must have the canonical base branch's latest commit locally.
The fetch above supplies it. If it is missing, publication stops with an
explanation; `sand` does not fetch it during collection.

Once you have published, keep that history and add new commits. Rebasing
published commits can prevent publication from resuming correctly. See
[There is no force push](#there-is-no-force-push-the-fork-branch-only-ever-grows).

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

If published history needs to change, you must repair it outside `sand`.
For example, you may need to remove an unrelated commit before resuming a
publish. Two recovery options are:

- **Force-push from your workstation**, using your own Git and credentials.
  After checking which commit should be the branch's new tip, you can move
  the branch back to it:

    ```console
    $ git push --force <fork-remote> <good-sha>:refs/heads/<module>-<nid>
    ```

    This rewrites the branch while keeping the existing merge request and
    its discussion.

- **Delete the fork branch through drupal.org's web UI and publish again.**
  `sand` recreates `<module>-<nid>` from the canonical project's base branch
  and replays your local change set. Deleting the source branch closes the
  old merge request; the next publish opens a new one, leaving its review
  discussion on the old request.

<a id="sand-publish-refuses-a-branch-it-cannot-line-up-with"></a>

### When publication cannot resume

To resume, `sand` matches the first commits in your change set against the
most recent commits on the fork branch. If it finds previously published
commits but cannot match that sequence at the branch tip, it stops before
writing. For example, an unrelated commit added after your last publish can
produce:

> the fork branch's newest commit ("…") is not part of this change set, but
> 3 of its commits are already on the branch.

Use one of the recovery options above to repair the fork. This refusal
writes nothing, so the failed attempt itself needs no cleanup.

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

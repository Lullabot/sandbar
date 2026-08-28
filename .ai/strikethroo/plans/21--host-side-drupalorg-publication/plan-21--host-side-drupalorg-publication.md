---
id: 21
summary: "Publish agent-authored drupal.org contributions from the workstation over GitLab's content API, so no drupal.org credential ever exists inside a VM"
created: 2026-08-27
---

# Plan: Host-Side Drupal.org Publication via the GitLab Content API

## Original Work Order

This plan is an **extraction from plan 20**, whose work order asked for a
drupal.org contribution workflow that is simple for a Drupal developer. The
credential half of that work order is quoted below, unmodified:

> There's another issue with gitlab authentication. drupal.org works with
> repository forks - each issue gets its own unique repository to work in. That's
> great on one hand - it means that gitlab api tokens are issue, and not project
> scoped. However, it also means that every new issue needs a new gitlab
> fine-grained token, and the UI for creating those is pretty rough. It also means
> if worktrees are used, each one of them needs its own token (instead of per
> module or project).
>
> Ideally nothing is specific to a harness like claude code, so users can work well
> with other tools as they choose.

The extraction itself was directed separately:

> Yes, let's extract it into a new plan. It can be plan 21, but I'll implement it
> before doing plan 20.

Plan 20 retains the environment half — the ddev-drupal-contrib bootstrap, its
trigger surfaces, per-checkout ddev identity, the issue-number entry point, and
reset. This plan owns everything to do with credentials and publication.

## Plan Clarifications

Five refinement sessions on plan 20 established this design. The rows below are
the ones that decided *this* plan; they are reproduced here because this document
must stand alone, and because plan 20 is no longer the authority on any of them.

### Refinement session 1 — the seam, 2026-08-26

| Question | Answer | Source |
| --- | --- | --- |
| drupal.org's PAT docs say tokens are user-account scoped and push access to issue forks is granted per user, so one PAT could cover every fork. Is the per-issue token model a hard constraint or a chosen posture? | **Deliberate least-privilege.** One account-wide PAT would work technically, but an agent-controlled VM must not hold push access to the whole drupal.org account. | User |
| Where should creating and placing per-fork tokens happen? | **Host mints, guest receives** — conditional on confirming a GitLab API exists for it. *Superseded by refinement 3.* | User |
| Where does the host-held account PAT live? | `internal/profiles/token.go` establishes the pattern — config records only the *path* to a credential, and the loader refuses a file readable by group or other. The drupal.org PAT follows it. | Auto-resolved from codebase |
| Does the plan correctly distinguish GitLab's token scopes from access levels? | **No — the original plan conflated them.** They are independent axes deciding different things. Corrected. | Auto-resolved; defect in prior revision |
| *"How could use the drupalorg cli inside the VM, when the token minting needs to be on the user's workstation?"* | A valid contradiction. Resolved by evidence: the host needs **neither** the `drupalorg` CLI nor an authenticated API call to resolve a fork. See Background. | User challenge, resolved by verification |

### Refinement session 2 — multiple projects per VM, 2026-08-26

| Question | Answer | Source |
| --- | --- | --- |
| Does the multi-module case break the credential design? | No — it argues *for* it. All canonical drupal.org modules share one org directory, so a directory-scoped scheme would have given them a single shared token. | Auto-resolved from codebase |
| Can the publication entry point identify the right module in a multi-module VM? | **No — defect in the prior revision**, which derived the module from the VM's create-time `CloneURL`. It must be read from the targeted checkout instead. Corrected. | Defect in prior revision |
| Must each task be verified before it is considered complete? | **Yes.** Every task runs `/code-review --fix` and then `/simplify` before it may be marked complete. Recorded as a per-task completion gate under Self Validation. | User |

### Refinement session 3 — the probe came back negative, 2026-08-27

| Question | Answer | Source |
| --- | --- | --- |
| Did the validation probe settle the two open drupal.org unknowns? | **Yes, both negative.** `access_level = 30` (Developer) on a real issue fork, below the Maintainer that creating a project access token requires; and `POST .../access_tokens` is blocked at drupal.org's edge for everyone. Per-issue-fork *project access* tokens are impossible. | Probe output |
| Could scripting the GitLab web UI work around the blocked endpoint? | **No.** The obstacle is the role, not the API: at Developer there is no access-tokens page to script. It would also require the developer's SSO session or password — a credential more dangerous than the one being avoided. | User proposal, resolved by verification |
| What is the actual requirement behind wanting narrow tokens? | *"Developers may have push access to highly used projects. An agent with a PAT could push to a project with bad code and affect 10s of thousands of sites."* Blast radius containment, not tidiness. | User |
| How should the credential half proceed given that? | **Host publishes; guest holds nothing.** No credential of any kind inside the VM. | User |
| *"Do we have to push with the git protocol? Do we have to have literal files on disk? What if as a part of land we then used the gitlab API to create a commit?"* | **Yes, and it is better than the git-based version.** Verified that drupal.org routes `POST /repository/commits`, `/repository/files`, `/repository/branches`, and `/merge_requests` to GitLab while blocking only the credential-minting endpoints. One call creates the branch and lands the change set. The host needs no git repository, no checkout, and no git objects. | User idea, verified |
| Does the guest still need any drupal.org credential? | **No.** Anonymous requests returned 200 for merge requests, pipelines, branches, trees, commits, issues, and raw files. The entire read loop works unauthenticated. | Auto-resolved by verification |

### Refinement session 4 — research contradicted an assumption, 2026-08-27

| Question | Answer | Source |
| --- | --- | --- |
| Is it true that GitLab PATs can never be project-scoped? | **No — the plan was wrong.** That holds for *classic* PATs only. **Fine-grained access tokens** went generally available on GitLab Self-Managed in **19.2**, carry a project/group/instance **access boundary**, include a `Code: Push` permission, are **Free tier**, and intersect with the user's own permissions — so **Maintainer is not required**. Verified against git.drupalcode.org's own served documentation; the instance runs GitLab 19.x. | Research, verified independently |
| Can fine-grained tokens be minted via API? | **No.** `POST /user/personal_access_tokens` accepts only `k8s_proxy` and `self_rotate` and has no boundary parameter. Creation stays a web-UI act; only rotation is automatable. | Verified |
| Was the edge allowlist a security boundary? | **No — the plan overstated it.** `POST /api/graphql` is not blocked, and git push over HTTPS bypasses the REST allowlist entirely. DA staff note their load balancer cannot inspect request bodies. The allowlist must not be treated as containment. | Research, corrected |
| Does the DA's automation policy forbid AI-assisted merge requests? | **No.** That policy governs site access and automation registration, and contains no rule about merge requests, commits, or pushes. Disclosure attaches to *approved automation tools*. A human reviewing and publishing the agent's work is "an individual action a user could already perform". Per-MR AI disclosure is proposed in governance issue 3565917 but is **not ratified policy**. | Research |

### Refinement session 5 — the token boundary is per-project, 2026-08-27

| Question | Answer | Source |
| --- | --- | --- |
| Does git.drupalcode.org's token UI expose fine-grained tokens at all? | **Yes.** The Generate token dropdown offers "Fine-grained token", the form exposes Group and project access, and resource `Code` -> permission `Push` is present. | User, on a real account |
| Can the boundary select a single issue-fork project? | **Yes**, and a token so bounded was created and used to push to an issue fork successfully. | User, verified by pushing |
| Can the boundary select the `issue` group ("Issue forks")? | **No — projects only.** Corroborated by the access probe: `group_access` is `null` on an issue fork, so the developer's access there is a direct project membership and they are not a member of the `issue` group — and GitLab offers only boundaries the user belongs to. | User; corroborated by probe |
| Does a higher access level on a maintained module reopen project access tokens? | **No, and the finding is structural rather than sampled.** The developer is a full maintainer of dubbot, created a brand-new issue and its fork, and still holds `access_level: 30` (Developer) on that fork. If the parent project's own maintainer is Developer on a fork they created themselves, no drupal.org user can hold Maintainer on an issue fork. | User probe |
| Which credential design is built? | **Host-side publication only.** The per-fork fine-grained token is recorded as a manual escape hatch a developer may set up for themselves, but no sand machinery is built for it. | User decision |
| What remains unverified about the token option? | The negative control — that a token bounded to one issue fork **fails** to push to a canonical `project/<module>` — has not been run. It must pass before the escape hatch is documented as safe. | Unresolved |

### Refinement session 6 — extraction to a separate plan, 2026-08-27

| Question | Answer | Source |
| --- | --- | --- |
| Should the publication feature be extracted from plan 20 into its own plan? | **Yes.** Refinement 5 declined a split because two mutually-exclusive credential designs were blocking decomposition, and choosing one removed that blocker. That reasoning did not address the grounds that decide it now: plan 20 itself certified, before the split, that the two mechanisms "share no code and can be built and tested independently"; every external blocker (three unanswered Drupal Association questions, the host PAT, a real account for end-to-end tests) sits entirely on the publication side; the two halves have qualitatively different test regimes, since publication validation creates **public, permanent** merge requests on drupal.org while the environment half runs wholly inside a local VM; and a combined blueprint would run to roughly twenty tasks under a heavy per-task gate. | User decision |
| Which plan is implemented first? | **This one — plan 21 — before plan 20.** | User |
| Does that ordering change anything? | **Yes, two things.** The escape-hatch documentation and its negative control move here rather than staying with plan 20, because this plan both ships first and owns the credential reasoning; a contributor meets the "why can't I just push?" question the moment publication exists. And fork resolution by convention — needed by this plan's destination selection and by plan 20's issue entry point — is built here, with plan 20 reusing it. | Derived from the ordering |
| Does this plan depend on plan 20 in any way? | **No.** Publication takes a change set from any drupal.org checkout in a guest; it does not care whether that checkout is a bootstrapped ddev project. End-to-end validation uses a hand-made checkout in a VM, since the automatic bootstrap does not exist yet. | Derived |
| Was the per-task completion gate carried over? | **Yes**, unchanged. It applies to every task generated from this plan. | Carried from refinement 2 |

### Refinement session 7 — surfaces, config home, and commit shape, 2026-08-28

| Question | Answer | Source |
| --- | --- | --- |
| The plan says a developer publishes "with one host-side command" but never names the surface, and `land` already has two. Which does publication get? | **Both**, mirroring `land`: a `cmd/sand/publish.go` CLI entry point and a TUI action, over a shared `internal/drupalorg` package. | User |
| Is the TUI half a new pane? | **No — it fills an arm the Landing pane already has.** `classifyLandRow` (`internal/ui/landing.go`) already carries a "non-GitHub forge: state shown, no one-key action (deferred: glab)" arm, and `checkouts.Checkout` already records `Forge`. A `git.drupalcode.org` checkout flows into that dead arm today. The row model, job registry, and confirmation idiom all carry over. | Auto-resolved from codebase |
| Where is the host PAT's path recorded? `TokenFile` is a *Proxmox-profile* field and `profiles.yaml` is a per-location store, so the plan's stated pattern had no home for a workstation-global credential. | **A fixed conventional path**, `${XDG_CONFIG_HOME:-~/.config}/sandbar/drupalorg.token`, with `token.go`'s mode refusal. No config key, no schema, no form field — nothing to mis-record and nothing to migrate. A new global config file was rejected as scope creep under PRE_PLAN. | User |
| Does one publish produce one commit or several? | **Several — each local commit is replayed** as its own `POST /repository/commits`, preserving the guest's history and messages on the fork. The plan had listed this as an unresolved gap to be decided deliberately. | User |
| What does replay cost? | **Partial failure becomes a first-class outcome.** N sequential calls means commit 2 of 3 can fail with commit 1 already public and unrevocable. Accepted deliberately; the design must report exactly what landed and be resumable rather than pretend it can roll back. | Derived from the decision |
| Does the merge-request call work as the plan described it? | **No — the plan was wrong.** It said the MR opens "against the issue branch". Verified on `project/dubbot`: every drupal.org MR is cross-project, `source_project_id` 241450 -> `target_project_id` 106348, `target_branch` `2.x`. The call needs `target_project_id` pointed at the canonical project. | Defect in prior revision, corrected by probe |
| Does that break the `issue/`-namespace guard? | **No, but the guard needed scoping.** It governs where *commits* land, which stays inside `issue/`. An MR necessarily names the canonical project as its target — that is metadata about where a proposal is directed, not a write to that project's code. The target is derived from the fork's own `forked_from_project`, read anonymously, so no guest data reaches it. | Derived; corroborated by probe |
| Is creating the branch the normal case? | **No — the branch almost always already exists.** drupal.org creates the issue branch with the fork: `issue/drupal-3181657` already carries `3181657-fix-views-timezone` and `3181657-test-only`. `start_branch` against an existing branch errors, so the normal path omits it. | Probe |
| Can the payload express the change sets it must carry? | **No — defect in the prior revision.** "Paths and their resulting contents" cannot express a deletion, which has no content, nor a rename, though the content API has `delete` and `move` actions. Corrected to an ordered list of commits, each carrying file actions with an explicit kind and encoding. | Defect in prior revision |
| Does the host call GitLab over HTTP or shell out to `glab`? | **`net/http`**, on the typed-client pattern `internal/pve` already establishes. Success criterion 4 says the host needs no Drupal tooling and no git; a mandatory `glab` install would sit badly with that, and would need an availability probe of the kind `landgh` carries only because `gh` may be absent. `glab` stays a dependency of the probe script alone. | Auto-resolved from codebase |
| Where does commit authorship come from under replay? | **Per-commit, from the guest's local history** — by necessity, since replaying a commit without its author would rewrite it. This is not a destination and does not weaken the split of authority; the committer of record is the PAT owner regardless. Stated rather than left implicit. | Derived from the replay decision |

## Executive Summary

A Drupal developer working in an agent-controlled VM needs a way to publish work
to drupal.org. The obvious answer — put a credential in the VM — is unacceptable,
and the reason is specific rather than general: **a Drupal developer often holds
push access to modules installed on tens of thousands of sites.** A single bad
push is a supply-chain event affecting people who never opted into anyone's AI
experiment. The credential must therefore be narrow, or it must not be there.

drupal.org makes narrow credentials unobtainable in any automatable form, and this
plan's probes established that rather than assuming it. Project access tokens
require Maintainer, and an issue fork grants Developer to *everyone* — the parent
project's own maintainer included, on a fork they created themselves — so that
route is closed permanently rather than closed to one account. drupal.org
additionally blocks `POST /projects/:id/access_tokens` at its edge, for every
caller and every project. Fine-grained access tokens *can* be bounded to a single
issue fork, and one was verified pushing successfully; but the boundary picker
offers individual projects rather than the `issue` group, and no API can create
one, so the developer would hand-build a token in GitLab's web UI for every issue
— the exact friction the work order set out to remove.

So the guest is given **no credential at all**, and publication moves to the
workstation. The guest clones anonymously, reads merge requests, pipelines, branch
lists, trees, commits, and raw files anonymously — all verified — and commits only
to its own local git. Publication happens on the host through GitLab's content API
rather than the git protocol: each local commit is replayed as its own
`POST /repository/commits` onto the issue branch — which drupal.org has usually
created already, alongside the fork — and a merge request is opened if one does not
yet exist. That merge request is cross-project by drupal.org's construction: it runs
from the fork to the canonical project, whose identity the host reads anonymously
from the fork's own `forked_from_project`. The host therefore needs no git
repository, no checkout and no git objects, which preserves the no-code-on-host
boundary `internal/landgh` exists to protect — nothing transits but inert data the
host forwards over HTTPS without ever writing it to a working tree or executing it.

The security property is a **split of authority rather than the strength of a
secret**: the agent decides content, the host decides destination. The target
project and branch are host-side arguments that never appear in anything the guest
produces. There is no credential in the VM to steal and no destination field in
the payload to poison, so a compromised agent cannot reach a canonical project — it
can only propose file contents for a target the human already chose, in a form the
human can read before it is sent. That reviewability is a real gain over a git
push, which publishes first and shows a diff afterwards.

## Context

### Current State vs Target State

| Current State | Target State | Why? |
| --- | --- | --- |
| An agent that can push holds a credential reaching every project the developer can push to, including modules on tens of thousands of sites | No drupal.org credential exists inside the VM at all | Blast radius. The only narrower credential drupal.org offers must be hand-made per issue and cannot be minted by API, so the safe amount that is also automatable is none |
| Publishing requires the git protocol, a checkout, and credentials wherever the push happens | The host publishes over HTTPS through GitLab's content API — no git repository, no checkout, no git objects | Keeps agent-written code off the workstation, and removes the need for any guest credential |
| An agent could push anywhere its credential reaches | The agent decides content; the host decides destination | A structural boundary: no credential in the VM to steal, no destination field in the payload to poison |
| A push publishes first and shows a diff afterwards | The payload is a readable list of paths and resulting contents, reviewable before it is sent | Human review of agent work should happen before publication, not after |
| Resolving an issue fork appears to need drupal.org tooling, which lives only in the guest | Fork URLs are derived by convention and confirmed anonymously, from the host, with no credential and no Drupal tooling | Resolves the apparent contradiction between "the host holds the credential" and "the `drupalorg` CLI is in the guest" |
| A contributor who discovers that a fine-grained per-fork token works will assume it was overlooked | Documentation says it works, says what it costs, and says what accepting it gives up | Left unexplained, this invites someone to wire a token into a guest and quietly delete the design's central property |
| sandbar has no host-side credential outside the guest-facing secrets store | The account PAT follows `internal/profiles/token.go` — a recorded path, one loader, a hard refusal of over-permissive modes | `internal/secrets` exists to deliver secrets *into* guests, which is exactly what this credential must never do |
| Publishing means a `git push`, which discards nothing and reports nothing until it is done | Each local commit is replayed onto the issue branch through the content API, and what landed is reported precisely when part of a replay fails | History is worth preserving on a shared issue fork, and a public, unrevocable partial result must be named rather than hidden behind a generic error |
| Non-GitHub checkouts appear in the Landing pane with no action available | A `git.drupalcode.org` row offers publication, filling the arm `classifyLandRow` already reserves for a non-GitHub forge | The pane already detects the forge and already shows the row; only the action is missing |
| A task could be marked complete without a review or simplification pass | No task completes until `/code-review --fix` and then `/simplify` have run and their tests re-run | Correctness and quality gates belong at task boundaries, not as one sweep at the end |

### Background

Every finding below was established by probe, by empirical test, or by reading the
codebase. Several overturned the design that seemed obvious at the start, and they
are recorded with how they were established because more than one earlier revision
was wrong in a way that changed the design.

**CLASSIC GitLab personal access tokens cannot be restricted to specific projects
— but fine-grained access tokens can (corrected 2026-08-27).** An earlier revision
stated flatly that GitLab PATs are always account-wide. That is true of *classic*
PATs, and it is what GitLab's classic PAT documentation says, but it was wrong as
a general claim and it drove several downstream design decisions.

GitLab **fine-grained access tokens** are a separate mechanism, introduced as a
beta in 18.10 and *"generally available on GitLab Self-Managed in GitLab 19.2"*.
They carry an explicit **access boundary** — a single project, a group, or the
whole instance — and a permission set that includes *"Push to a project
repository"* under resource `Code`. They are **Free tier** and available on
**GitLab Self-Managed**, and their permissions are intersected with the user's
own, so **Maintainer is not required**: Developer on an issue fork is enough.

This was verified against git.drupalcode.org itself, which serves its own
version's documentation: `/help/auth/tokens/fine_grained_access_tokens.md` and
`.../fine_grained_access_tokens_other.md` both return 200 while a control path
404s, and `/help/update/versions/gitlab_19_changes.md` exists while
`gitlab_20_changes.md` does not — so the instance runs GitLab 19.x, at or past
the version where this went generally available.

One consequence is limiting and permanent: fine-grained tokens **cannot be
created through the API**. The self-service endpoint
`POST /user/personal_access_tokens` accepts only `k8s_proxy` and `self_rotate`
scopes and has no boundary parameter, so creation remains a web-UI act. Only
rotation is automatable.

**The boundary is per-project, not per-group (verified in the UI, 2026-08-27).**
An earlier revision hoped that because the `issue` namespace is a real group (`GET
/api/v4/groups/issue` returns `{"id":49196,"name":"Issue forks","path":"issue"}`),
one token bounded to that group would reach every issue fork while being unable to
touch any canonical `project/<module>` — one token, created once, with the
catastrophic case removed by construction. It does not work. On a real account the
Generate token dropdown *does* offer **Fine-grained token**, the form *does* expose
resource `Code` -> permission `Push`, and a boundary of a **single issue-fork
project** works — a token so bounded was created and verified by pushing to that
fork. But the boundary picker offers individual projects only; the `issue` group is
not selectable. The access probe explains why: `group_access` is `null` on an issue
fork, so a developer's Developer role there is a *direct project membership* and
they hold no membership in the `issue` group — and GitLab offers only boundaries
the user belongs to.

The practical consequence is decisive. A fine-grained token is a **per-issue-fork
credential that must be hand-built in the web UI for every issue**. Its blast
radius is excellent — one fork, tighter than the group option would have given —
but it restores in full the friction the work order named at the outset, and no
amount of sand automation can remove it, because there is no minting API to call.
That is what settled the design on host-side publication rather than on a narrower
token.

**Per-issue-fork *project access* tokens are impossible on drupal.org (proven by
probe, both halves negative).** Note the qualifier: what is closed here is the
*project access token* specifically. A per-fork **fine-grained** token is possible,
as recorded above — but it must be created by hand, once per issue.

*Nobody holds Maintainer on an issue fork — not even the parent project's own
maintainer.* A first probe returned `access_level = 30` (Developer) on an issue
fork of a module where the developer was an ordinary contributor, which left open
the possibility that a maintainer would fare better on their own module. A second
probe closed that door: on `issue/dubbot-3619492` — a brand-new issue fork, of a
module the developer fully maintains, created by that developer moments earlier —
the access level is still `30`, with `group_access: null`. If the parent project's
own maintainer is merely a Developer on a fork they created themselves, the role is
a property of how drupal.org provisions issue forks rather than of any account's
standing. GitLab requires Maintainer or Owner to create a project access token **by
any route, including the web UI**, so this path is closed for every drupal.org
user, permanently. A per-fork project access token therefore cannot be minted, and
cannot be created by hand either. Scripting the GitLab web UI — considered
explicitly — does not help: at Developer there is no access-tokens settings page to
drive, so the obstacle is the role, not the API. Such a script would also need the
developer's SSO session or password, a credential strictly more dangerous than the
token it was trying to avoid.

**The minting API exists and has a natural privilege ceiling — but is unreachable
on drupal.org, and is recorded here only as history (from GitLab API
documentation).** Two independent findings kill it: nobody holds Maintainer on an
issue fork, and drupal.org blocks the endpoint at its edge. Nothing in the current
design calls it. `POST /projects/:id/access_tokens` accepts `name`, `scopes[]`,
`expires_at`, and an optional `access_level`, and returns the token value in the
response. It must be called with a *personal* access token — "You cannot
authenticate with a project access token" — so a minted per-fork token can never
mint further tokens. The caller cannot exceed their own access level but may mint
below it. Default maximum lifetime is 365 days; rotation and revocation are also
API operations. Project access tokens require Premium on GitLab.com but are
available under any license on self-managed instances, and drupal.org is
self-managed, so the feature is not license-blocked there.

**drupal.org runs a per-path, per-method allowlist in front of the GitLab API, and
it blocks exactly the credential-minting endpoints.** This was mapped
unauthenticated, so it is a property of the platform rather than of one account:

| Endpoint | Result |
| --- | --- |
| `POST /repository/commits` | routed to GitLab |
| `POST /repository/files/:path`, `PUT` same | routed to GitLab |
| `POST /repository/branches` | routed to GitLab |
| `POST /merge_requests`, `POST /issues` | routed to GitLab |
| `GET /access_tokens` | routed to GitLab |
| `POST /access_tokens` | **blocked at the edge** |
| `POST /deploy_tokens` | **blocked at the edge** |
| `POST /deploy_keys` | **blocked at the edge** |

A blocked request never reaches GitLab at all: it returns a byte-identical 56 KB
HTML 404 from the drupal.org Drupal site, on every project tried, authenticated or
not. A routed request returns GitLab JSON. The policy this encodes is coherent and
worth stating plainly, because it shapes the entire design: **drupal.org refuses
to let anyone mint credentials through the API, while permitting every
content-write operation.** Contribution by API is the path the platform
deliberately leaves open.

`probe-drupalorg-api.sh`, alongside this plan, re-runs that map. It is one of the
validation steps below, and it exists so that a change in drupal.org's posture is
noticed deliberately rather than discovered as a publication bug.

**The edge allowlist is NOT a security boundary (corrected 2026-08-27).** An
earlier revision described the allowlist as a "coherent policy" that blocks
credential minting while permitting content writes. The first half of that reading
was too generous, and this plan does not lean on it. Two findings undercut it:
`POST /api/graphql` is **not** blocked and returns 200 unauthenticated, and git
push over HTTPS (`git-receive-pack`) reaches GitLab directly, bypassing the REST
allowlist entirely. Drupal Association staff have explained why in their own
infrastructure issue — protecting GraphQL writes the same way "would require
inspecting the request body, which is not a capability of our load balancer."

The allowlist is therefore best understood as a control on one specific REST path,
not as containment. Nothing here may treat it as a barrier that would stop a leaked
credential: **if a credential ever enters a guest, the allowlist will not save
you.** That reinforces, rather than weakens, the decision to keep credentials out
of the guest — the guarantee has to come from the credential's own boundary or
from its absence, never from drupal.org's edge.

**Everything an agent needs to read works with no credential (verified).**
Against a public fork, unauthenticated requests returned 200 for merge requests,
pipelines, branches, repository trees, commit lists, issues, and raw file contents.
Combined with anonymous `git clone`, this means a guest can do the entire
development loop — including reading CI results and merge-request feedback —
without ever authenticating. Only writes need a credential, and those are the
host's job.

**Issue forks are derivable and anonymously readable (verified empirically).**
A `ls-remote` against `git.drupalcode.org/issue/drupal-3181657.git` succeeded with
no credential presented, returning the fork's branches including one named for the
issue. Three consequences follow, and together they resolve what looked like an
architectural contradiction — that the host must act on a fork while the
`drupalorg` CLI lives only in the guest. The fork URL is derivable from the module
name (read from the targeted checkout) plus the issue number, by drupal.org's
documented `PROJECT-ISSUE_NUMBER` naming. Its existence and its branch list can be
confirmed anonymously, needing no token and no API access. And GitLab's project
endpoints accept a URL-encoded project path, so no project-ID lookup is needed
either. The host therefore requires no PHP runtime and no Drupal tooling, and
authenticates for one purpose only: publishing.

This also rules out a **guest-to-host broker**, which would have been the obvious
alternative resolution and which returns in a new form under the current design. A
broker that let the guest request a credential would have restored account-wide
push reach through the back door; a broker that lets the guest name its own
publication target gives away the destination decision the whole split of authority
depends on. Both are rejected for the same reason: the guest must not be able to
widen its own reach by asking.

**The GitLab content API can replace the git protocol entirely (from GitLab's API
documentation).** A single `POST /projects/:id/repository/commits` accepts a target
`branch`, a `start_branch` or `start_sha` to create it from, an `actions` array of
create/update/delete/move/chmod operations carrying file contents as text or
base64, and explicit `author_name`/`author_email`. So one call lands one complete,
correctly-attributed commit. Requests above 20 MB are rate limited and above 300 MB
rejected — irrelevant for module patches. This is what allows the host to publish
without a git repository, a checkout, or any git objects.

**The shape of a real drupal.org publication was verified rather than assumed
(probed anonymously, 2026-08-28).** Three findings, each of which corrected
something the earlier revision had stated or implied.

*The issue branch usually exists already.* drupal.org creates it when it creates
the fork: `issue/drupal-3181657` already carries `3181657-fix-views-timezone` and
`3181657-test-only`. An earlier revision described publication as "one call creates
the branch and lands the change set", which is the uncommon case. `start_branch`
against a branch that already exists is an error, so the normal path omits it and
appends; `start_branch` is used only when the branch is genuinely absent, and the
branch it starts from is the fork's own `default_branch` (`9.2.x` on that fork —
GitLab records there the branch the fork was taken from).

*Merge requests are cross-project, and the earlier revision described them wrongly.*
It said the second call "opens the merge request against the issue branch". It does
not. On `project/dubbot` the three most recent merge requests all run fork ->
parent: `source_project_id` 241450 -> `target_project_id` 106348, `target_branch`
`2.x`, and the same for 241411 and 241406. Listing merge requests on an issue fork
itself returns `[]`, because an issue fork holds none — they live on the canonical
project. The call is therefore `POST /projects/<fork>/merge_requests` carrying
`target_project_id` for the parent and the parent's development branch as
`target_branch`.

*The parent is derivable anonymously, so no guest data reaches the merge-request
target.* `GET /projects/issue%2Fdrupal-3181657` returns
`forked_from_project: {path_with_namespace: "project/drupal", id: 59858}` with no
credential presented. The canonical project the merge request is directed at is
therefore host-derived like every other part of the destination — read from the fork
the host itself resolved, never named by the guest.

**A host-side credential pattern already exists (confirmed in the codebase).**
`internal/profiles/token.go` establishes it: `profiles.yaml` is secret-free and
records only where a credential lives, `LoadToken` is the single read site, and a
file readable by group or other is refused outright rather than warned about. The
drupal.org account PAT should follow this exact pattern rather than introducing a
new store. Note that `internal/secrets` is *not* the right home — it exists to
deliver secrets into guests, which is precisely what this credential must never
do.

**Directory-scoped credentials cannot work for worktrees (verified
empirically).** Git resolves `includeIf "gitdir:…"` against `$GIT_DIR`. For a
linked worktree, `$GIT_DIR` is `<main-clone>/.git/worktrees/<name>` — not the
worktree's own directory. A test confirmed that an `includeIf` on the worktree's
path never fires, while an `includeIf` on the *main clone's* path fires from
inside the worktree. This has an unflagged consequence for sandbar's existing
GitHub support: every worktree of a repo silently inherits the main clone's token
today, and no per-worktree token is expressible through that mechanism. That
behavior is out of scope to change here, but is recorded as a follow-up.

**URL-keyed credentials solve it (verified empirically).**
`git-credential-store` files support one entry per URL including a path component,
and with `credential.useHttpPath` enabled git resolves them per-repository. A test
confirmed that distinct tokens for `issue/foo-3123456`, `issue/bar-3999999`, and
`project/foo` each resolve correctly, that an unlisted fork yields no credential at
all (fail-closed), and that resolution is identical from inside a linked worktree.

**`useHttpPath` must be host-scoped or it breaks GitHub (verified empirically).**
Enabling `credential.useHttpPath` globally causes git to send a path on every
request, and sandbar's existing path-less `github.com` store entry then stops
matching — a real BC break. A test confirmed that scoping it to
`git.drupalcode.org` preserves the GitHub entry and gives per-fork resolution
simultaneously.

*Those last three findings are retained deliberately even though this plan builds
none of that machinery.* They are expensively established, and they become
load-bearing the instant anyone sets up the documented per-fork escape hatch, or
attempts per-worktree credentials on any forge. An earlier revision claimed the
machinery was unnecessary under **either** credential design, reasoning that a
token bounded to the `issue` group would be one credential for the whole host. That
group boundary does not exist. Had the token design been chosen, it would have
needed **exactly** this machinery. It is unnecessary here because host-side
publication was chosen, not because no design needs it.

**What the VM already provides.** Every sand VM ships ddev, Docker, `mkcert`,
`glab`, and the `drupalorg` CLI — which offers `issue:get-fork`,
`issue:setup-remote`, `issue:checkout`, and the `mr:*` family. All of the read
operations were verified to work unauthenticated against a public fork, so the
guest keeps that CLI for merge-request status, pipeline logs, and diffs while
holding no credential. `internal/checkouts` already sweeps guests for git checkouts
and worktrees.

## Architectural Approach

Publication is a host-side adapter and a small guest-side collection step. It is a
sibling of `internal/landgh` and should be built as one. The whole design turns on
a single asymmetry: **content flows guest to host; destination is decided only on
the host.**

```mermaid
graph TD
    DEV["Developer: publish, when satisfied"]

    subgraph GUEST["Guest VM (agent-controlled) — HOLDS NO CREDENTIAL"]
        CLONE["Anonymous clone + anonymous reads<br/>MRs, pipelines, CI, files"]
        WORK["Agent commits to LOCAL git only"]
        COLL["Collect the commits not yet on the fork<br/>message, author, file actions per commit<br/>no destination, ever"]
        CLONE --> WORK --> COLL
    end

    subgraph HOST["Host (trusted) — holds the credential"]
        SURF["sand publish (CLI)<br/>or Landing pane row (TUI)"]
        RESOLVE["Resolve fork by convention<br/>module + issue number<br/>then read forked_from_project<br/>anonymous, no credential"]
        SHOW["Show every commit, its files,<br/>and WHERE. Human confirms."]
        PAT["Account PAT<br/>~/.config/sandbar/drupalorg.token<br/>mode 0600 or refused<br/>never enters a VM"]
        API["net/http, per commit:<br/>POST /repository/commits<br/>then POST /merge_requests<br/>if none exists yet"]
        SURF --> RESOLVE --> SHOW --> API
        PAT --> API
    end

    DEV --> SURF
    COLL -->|"content only, inert data"| SHOW
    API -->|"HTTPS"| GL["git.drupalcode.org"]

    DEST["Destination is a HOST-side argument.<br/>The agent never names it."]
    DEST -.-> RESOLVE

    style GUEST fill:#2d2d2d,color:#fff
    style PAT fill:#4a2020,color:#fff
    style DEST fill:#1f4a2f,color:#fff
    style SHOW fill:#1f3a5f,color:#fff
    style SURF fill:#1f3a5f,color:#fff
    style WORK fill:#1f3a5f,color:#fff
```

### The host-side account PAT

**Objective**: Hold one powerful credential in the one place it is safe, in a way
that cannot leak into a guest by accident.

The credential is an account-level drupal.org personal access token with API
scope, held on the workstation at a **fixed conventional path**:
`${XDG_CONFIG_HOME:-~/.config}/sandbar/drupalorg.token`. One loader is the single
read site, and a file readable by group or other is refused outright rather than
warned about — `internal/profiles/token.go`'s refusal, reused verbatim in intent.

The path is a convention rather than a configured value, and that is a deliberate
narrowing of what the plan first said. `token.go`'s `TokenFile` is a field on a
**Proxmox connection profile**, and `profiles.yaml` is a per-location store; a
workstation-global credential that has nothing to do with where VMs run has no
coherent home there. The alternative — a new global config file — would introduce a
persisted schema, its versioning, and a TUI editing surface for a single string,
which is the scope creep PRE_PLAN exists to stop. A convention has nothing to
mis-record and nothing to migrate. An absent file is not an error condition to
diagnose at publish time: it means publication is unavailable, and both surfaces say
so up front.

Placement in `internal/secrets` must be refused explicitly, and the reason
documented at the point of the decision rather than only in this plan: that package
exists to deliver secrets *into* guests. Putting this credential there would make
the one thing the entire design forbids into a one-line change that looks correct.

The credential is only ever read at the moment of an authenticated call, and the
only authenticated calls are the commit replay — one per local commit — and the
merge request, when one is needed. Fork resolution, branch-existence checks, branch
listing, and reading the fork's `forked_from_project` all use no credential at all.

### Fork resolution by convention

**Objective**: Let the host name a destination without Drupal tooling, a PHP
runtime, git, or a credential — and let plan 20's issue entry point reuse the same
resolution.

The module name is read from **the checkout being targeted** — its origin remote,
or the path the developer names — and explicitly *not* from the VM's create-time
`CloneURL`. That distinction is load-bearing and was wrong in an earlier revision:
`CloneURL` is a single value recorded once per VM, so in a VM holding several
drupal.org modules it identifies only the first, and deriving from it would publish
to the wrong fork.

Combined with the issue number, the module name yields the fork path by
drupal.org's documented `PROJECT-ISSUE_NUMBER` convention: `issue/<module>-<nid>`.
Existence and the branch list are confirmed by an anonymous fetch. GitLab's project
endpoints accept a URL-encoded project path, so no project-ID lookup is needed.

Resolution is a pure function of module name and issue number plus one anonymous
network check, which is what lets it be shared: plan 20's issue-number entry point
needs exactly the same derivation to pick a branch to check out. Because this plan
is implemented first, resolution is built here and plan 20 consumes it.

### The publication payload

**Objective**: Carry the change set as inert data, structurally incapable of
naming where it goes.

The payload is an **ordered list of commits**. Each carries its message, its
author name and email, and an ordered list of file actions; each action carries an
explicit **kind** — create, update, delete, move — a repository-relative path, and,
for the kinds that have one, the resulting content with its encoding (text, or
base64 for anything that is not valid UTF-8).

The action kind is not decoration. An earlier revision specified the payload as
"paths and their resulting contents", which cannot express a deletion — a deleted
file has no content — and cannot express a rename, though the content API offers
both. A change set that removes a file would have been silently unpublishable.

The payload carries **no destination field of any kind** — no project, no branch, no
remote, no URL. This is the central structural claim of the design and must be
enforced by the type, not by convention: if the payload cannot express a
destination, no amount of prompt injection or agent compromise can supply one.

Authorship is the one guest-supplied field that reaches drupal.org verbatim, and it
is stated here rather than left implicit. Replaying a commit without its author
would rewrite it, so the author travels with the commit. This does not weaken the
split of authority — an author name is not a destination, and it cannot redirect a
write — and the committer of record is the PAT owner in every case, so a forged
author line misattributes text without concealing who published it.

Paths must be validated on the host before use: repository-relative only, no
absolute paths, no `..` traversal, no path that normalises outside the tree.
Traversal-style paths are **refused**, not normalised into something plausible —
silently correcting a hostile path is how a payload ends up writing somewhere
unintended.

The payload is also the review surface, which is a genuine improvement over a git
push. A human can read the paths and the resulting contents before anything is
sent, whereas a push publishes first and shows a diff afterwards.

### Destination selection and confirmation

**Objective**: Keep the one decision the design depends on — where this goes — in
human hands, and make the dangerous case require a conscious act.

The destination is assembled entirely from host-side arguments: the target
checkout the developer names, the issue number they give, and the branch derived
from them. Nothing the guest produced participates.

Two controls sit on top of that. The publisher **refuses any commit destination
outside the `issue/` namespace unless the developer overrides deliberately**, so
writing to a canonical `project/<module>` — the case that motivated the entire
design — cannot happen as a default or a bug, only as an explicit act. And
publication requires explicit human confirmation showing what will change and where,
in a readable form rather than a summary count. Declining publishes nothing. No code
path may publish as a side effect of any other command.

The guard governs **where commits land**, and that scoping is deliberate rather than
incidental. A merge request necessarily names the canonical project as its
`target_project_id`, because on drupal.org every merge request runs fork -> parent;
a guard that refused to name a canonical project at all would forbid the one thing
publication exists to do. The distinction that matters is that a merge request is a
*proposal* directed at a project, not a write to its code: it lands no commit, moves
no branch, and is reviewable and closable by that project's maintainers. And its
target is not a wider grant to be abused, because it is derived — read from the
fork's own `forked_from_project` by the host, anonymously — rather than supplied. A
payload cannot influence it any more than it can influence the fork.

Publication being a **human-initiated act** is also what keeps the workflow clear
of drupal.org's automation-and-bots provision: it is "an individual action a user
could already perform with a regular authenticated session", not a bot.

### The publish call

**Objective**: Land a complete, correctly-attributed change set and open a merge
request, using no git.

**One call is one commit, and each local commit is replayed as its own call.** A
publish preserves the guest's history and messages on the fork rather than squashing
them, which is the decision refinement 7 took after the plan had carried it as an
open gap. So a publish is *N* sequential `POST /projects/<fork>/repository/commits`
calls, each carrying that commit's message, its `author_name` and `author_email`,
and its file actions.

**The branch usually exists, so `start_branch` is usually omitted.** drupal.org
creates the issue branch alongside the fork, and sending `start_branch` against an
existing branch is an error. The first call therefore appends when the branch is
present and creates only when it is genuinely absent, starting in that case from the
fork's own `default_branch`, which GitLab sets to the branch the fork was taken
from. Which of the two happened is resolved anonymously before any authenticated
call is made.

**The merge request is cross-project.** `POST /projects/<fork>/merge_requests`
carries `target_project_id` for the canonical parent and that parent's development
branch as `target_branch` — verified against three real merge requests on
`project/dubbot`. The parent's identity comes from the fork's `forked_from_project`,
read anonymously. The call is **skipped when a merge request already exists** for
that source branch, which is the common case on an issue that has been worked
before; publishing more commits to an open merge request must not attempt to open a
second one.

**Partial failure is a first-class outcome, not an error path.** *N* sequential
calls means call 2 of 3 can fail with call 1 already public and unrevocable. There
is no rollback and the design must not pretend otherwise. What it must do instead is
report precisely which commits landed and which did not, name the first failure, and
be **resumable**: re-running a publish re-derives what is already on the branch and
replays only the remainder. A publish that stops halfway leaves the branch in a
state the next publish can complete, rather than one a developer has to repair by
hand.

Three further behaviors of the content API differ observably from a git push:

- **Commits created this way are not GPG-signed** by the developer.
- **`last_commit_id` is the only concurrency guard** if the fork moved underneath.
  Send it on update actions, and treat a conflict as a normal outcome to re-derive
  from rather than an error. Under replay it also guards the chain: each call's
  expected parent is the commit the previous call created.
- **Oversized payloads must be detected before sending** and explained, since the API
  rate-limits above 20 MB and rejects above 300 MB. The check is per call, so a large
  change set may be publishable as a replay where it would not be as one commit.

The client is `net/http`, on the typed-client pattern `internal/pve` already
establishes — not a shell-out to `glab`. Success criterion 4 says the host needs no
Drupal tooling and no git, and a mandatory `glab` install would sit badly beside it;
it would also need an availability probe of the kind `landgh` carries only because
`gh` may be absent. `glab` remains a dependency of `probe-drupalorg-api.sh` alone,
which is a developer tool rather than part of the product.

Failure reporting is a named requirement rather than an afterthought. Because
drupal.org's allowlist can change, a blocked endpoint returns an HTML body where
JSON was expected, and that must be recognised and reported as *drupal.org refused
this request* — not as a generic API error or a misleading 404.

### The two surfaces

**Objective**: Offer publication both headlessly and where the developer already
looks, without building the confirmation twice in two idioms.

Publication is exposed the way `land` already is: a CLI entry point at
`cmd/sand/publish.go` and a TUI action, both over one shared `internal/drupalorg`
package that owns fork resolution, the payload type, the client, and the guard. The
surfaces differ only in how they render the confirmation and stream progress;
neither holds publication logic of its own.

The TUI half is smaller than it sounds, because the Landing pane already reserves a
place for it. `classifyLandRow` in `internal/ui/landing.go` carries an arm for *"a
non-GitHub forge: state shown, no one-key action (deferred: glab)"*, and
`checkouts.Checkout` already records the remote's `Forge`. A `git.drupalcode.org`
checkout flows into that dead arm today. Filling it reuses the pane's row model, its
one-action-per-row idiom, and the job registry every other sand action streams
through — so the work is an action and a confirmation view, not a new pane.

The CLI half carries the same guard and the same confirmation, printed rather than
rendered, and is what makes the publication path testable end to end without driving
a TUI.

### The fine-grained token option, and why this plan does not build it

**Objective**: Record precisely what is given up, and state the condition under
which the decision should be reopened.

The fine-grained token is real, and this plan should not pretend otherwise. It
exists on git.drupalcode.org, it carries a `Code: Push` permission, it can be
bounded to a single issue-fork project, and a token so bounded was verified pushing
to its fork. What is not available is the **group** boundary: the `issue` namespace
cannot be selected, so there is no one-token-forever. What is on offer is one
hand-built token per issue fork.

That reduces the comparison to a single honest trade:

| | Host publishes (**chosen**) | Fine-grained per-fork token |
| --- | --- | --- |
| Credential in the guest | none | one, bounded to a single issue fork |
| Worst case if the guest is hostile | nothing | garbage pushed to that one fork |
| Canonical project reachable | no | no |
| Guest can iterate against CI directly | no — each publish is a host step | yes, ordinary `git push` |
| Setup cost | none | a manual web-UI token, **per issue** |
| Can sand automate that setup | n/a | **no** — GitLab exposes no creation API |

The token's blast radius is genuinely excellent — one fork, tighter than the group
boundary would have given. It loses on cost, and it loses on the work order's own
terms. The work order opens by naming this exact friction — *"every new issue needs
a new gitlab fine-grained token, and the UI for creating those is pretty rough"* —
and asks for it to go away. A design whose first step is a trip through that UI,
once per issue, does not answer the question that was asked; and sand cannot soften
it, because there is no endpoint to call.

Building both was considered and rejected under the PRE_PLAN scope-control hook.
The second path is not free: it would require the URL-keyed credential store,
host-scoped `credential.useHttpPath`, and per-fork placement and lifecycle — a
substantial mechanism, bought to serve a workflow the developer must hand-configure
anyway, for a pain no one has yet reported feeling.

**What is given up, stated plainly.** A contributor iterating against a failing
pipeline pushes repeatedly, and under host-side publication every one of those
iterations is a host-side confirmed step rather than a `git push`. That is real
friction on a real workflow, and it is the price of holding no credential in the
guest. This plan pays it deliberately rather than by oversight.

**The escape hatch is documentation, not code.** A contributor who wants the
in-guest loop can create a fine-grained token bounded to their issue fork and place
it in the guest themselves. The documentation should say how, and should state what
it costs: a credential then exists inside an agent-controlled VM, and while its
boundary confines it to one issue fork, drupal.org's edge will not contain it if it
leaks. Before this is written up as safe, the negative control must pass: a token
bounded to one fork must be shown to **fail** when pushing to a canonical
`project/<module>`. That test has not been run, and the documentation must not ship
until it does.

**When to reopen this.** The decision rests on one fact: token creation cannot be
automated. If the Drupal Association ever allowlists fine-grained token creation, or
if GitLab makes a group-level boundary selectable by non-members, the in-guest loop
becomes cheap and this section should be revisited.

### What this plan deliberately does not build

**Objective**: Record the omissions explicitly, so later work does not rebuild them
by reflex.

The guest never authenticates to drupal.org, so no recognized-forge entry for
`git.drupalcode.org` is added, no per-fork entries are written to the guest's
credential store, and no `useHttpPath` configuration is set.
`internal/provision/gitcred.go` is **untouched** by this plan; its
`recognizedForgeTokens` table keeps its single GitHub entry, and the existing
`GH_TOKEN` wiring is unchanged in every respect.

No guest-to-host credential broker is built, and none should be: it would restore
account-wide reach through the back door. No mechanism lets the guest name its own
publication target.

## Risk Considerations and Mitigation Strategies

<details>
<summary>Security and Trust Risks</summary>

- **The account PAT on the host is powerful, and the host publishes on its
  behalf**: Blast radius is removed from the *guest*, not from the system. The
  host still holds an account-wide credential and uses it to publish
  agent-authored content. A host-side bug that got the destination wrong would
  push agent output somewhere it should not go, and the credential's reach is
  exactly what makes that consequential.
    - **Mitigation**: The destination must be an explicit host-side argument that
      no guest-produced data can influence, and it must be shown to the human and
      confirmed before any write. Refuse to publish to anything outside the
      `issue/` namespace unless the developer overrides deliberately, so the
      dangerous case — writing to a canonical project — requires an extra,
      conscious act. Cover destination selection with tests that feed hostile
      guest payloads and assert the target is unchanged.
- **The payload is agent-authored by construction**: `internal/landgh`'s
  injection-safety invariant applies here with more force, because that adapter
  calls `gh` for metadata about a branch the guest already pushed, whereas this one
  carries the content itself.
    - **Mitigation**: Shell out or call out with an argument vector rather than a
      shell string, never interpolate guest data into a command or a URL, and make
      the payload type structurally incapable of carrying a destination. Validate
      paths as repository-relative and refuse traversal rather than normalising it.
- **Someone reintroduces a guest credential later**: The design's central property
  is an absence, and absences are easy to delete without noticing. A contributor
  who discovers that a fine-grained per-fork token works will assume it was
  overlooked.
    - **Mitigation**: Document the reasoning where the contributor meets the
      friction, not only in this plan: the token works, it is bounded to one fork,
      and it costs a manual web-UI trip per issue because no API can create one.
      Record it in `AGENTS.md` so future agent-driven work does not reintroduce it.
      Do not ship sand machinery for it until someone reports the iteration pain.
- **A leaked guest credential would not be contained by drupal.org**: If the escape
  hatch is used, the edge allowlist offers no protection — `POST /api/graphql` is
  unblocked and git push over HTTPS bypasses REST entirely.
    - **Mitigation**: State this in the escape-hatch documentation, and gate that
      documentation on the negative control passing. A boundary observed only
      permitting things has not been shown to forbid anything.

</details>

<details>
<summary>External Dependency and Policy Risks</summary>

- **drupal.org's policy bars automation without approval**: PATs may perform any
  individual action a user could perform in a session, but may not build automation
  or bots without prior Drupal Association approval. A tool that publishes
  agent-written commits sits close to that line, and a separate DA policy governs
  AI-assisted contribution specifically.
    - **Mitigation**: Keep publication a human-initiated, human-confirmed action.
      Document the policy in the user-facing guide, follow whatever disclosure the
      DA's AI policy requires for AI-assisted merge requests, and raise the design
      with the DA before promoting it widely.
- **The content API is not the git protocol, and differs in observable ways**: One
  call produces one commit, so replaying a multi-commit change set means several
  sequential calls; commits created this way are not GPG-signed by the developer;
  and `last_commit_id` is the only concurrency guard if the fork moved underneath.
  Requests above 20 MB are rate limited. The branch usually already exists, so
  `start_branch` is usually wrong to send.
    - **Mitigation**: Replay is the decided shape and belongs in the documentation
      rather than surprising contributors. Resolve anonymously whether the branch
      exists before any authenticated call, and send `start_branch` only when it does
      not. Send `last_commit_id` on update actions, chain it across a replay, and
      treat a conflict as a normal outcome to re-derive from rather than an error.
      Detect an oversized payload per call before sending and explain it.
- **The endpoint allowlist is drupal.org's to change**: The content endpoints this
  design depends on are permitted today because drupal.org allows them, not because
  they are guaranteed. The same edge that blocks the token endpoints could block
  others.
    - **Mitigation**: Treat a blocked-endpoint response — an HTML body where JSON
      was expected — as a distinct, recognizable failure that names what happened,
      rather than a generic API error. Re-run the endpoint map as a validation step
      so a change is caught deliberately.

</details>

<details>
<summary>Implementation Risks</summary>

- **A published change is public and permanent**: Unlike a token, which can be
  revoked, a commit pushed to a shared issue fork is immediately visible to the
  Drupal community and cannot be quietly withdrawn. The failure mode of this design
  is therefore reputational and social rather than credential-theft.
    - **Mitigation**: The pre-publication confirmation is the control, so it must
      show what will change and where in a form a human can actually read, not a
      summary count. Publishing must never be the default or automatic outcome of
      any command.
- **A replay can fail partway, leaving a branch half-published**: Replaying each
  local commit as its own call means commit 2 of 3 can fail with commit 1 already
  public. Nothing can withdraw it, and the state is one a developer did not ask for
  and may not notice. This is a cost the replay decision accepted knowingly rather
  than a defect to design away.
    - **Mitigation**: Treat partial success as a reportable outcome rather than an
      error: name exactly which commits landed, which did not, and why the first
      failure happened. Make publication **resumable** — a re-run re-derives what is
      already on the branch and replays only the remainder — so the recovery path is
      running the same command again rather than repairing the fork by hand. Cover
      it with a test that fails an interior call and asserts both the report and the
      resumption.
- **End-to-end validation writes to real drupal.org**: Unlike the environment work
  in plan 20, this plan cannot be validated entirely inside a local VM. Its tests
  create real merge requests on a public forge.
    - **Mitigation**: Validate against an issue fork of a module the developer
      maintains, on an issue created for the purpose, so the artefacts are
      recognisable and expected rather than noise in someone else's queue. Keep the
      unit-level destination and payload tests hermetic so that only the final
      end-to-end steps need the real forge.
- **Scope creep toward a general Drupal feature set**: The adjacent temptations —
  site-install workflows, patch management, a general MR client — are numerous.
    - **Mitigation**: The deliverable is publication and the credential handling it
      needs. The `drupalorg` CLI already covers issue and MR read operations and
      should be used rather than reimplemented.

</details>

## Success Criteria

### Primary Success Criteria

1. No drupal.org credential is present anywhere in a guest VM, in any form — not
   in the environment, the credential store, a `.env`, a git config, or a shell
   history.
2. The account-level drupal.org PAT exists only on the workstation, is read from
   the conventional path `${XDG_CONFIG_HOME:-~/.config}/sandbar/drupalorg.token`
   rather than from any configured value, and is refused outright if its file is
   readable by group or other. Its absence disables publication with a clear message
   rather than failing mid-publish.
3. A developer publishes from a guest checkout on either surface — the `sand publish`
   CLI entry point or the Landing pane row — and each local commit not yet on the
   fork appears on the correct issue fork's branch, in order, with its own message
   and its original authorship, carrying exactly the intended file changes. An
   existing branch is appended to rather than recreated, and a merge request is
   opened against the canonical parent project only if one does not already exist.
4. The host requires no PHP runtime, no Drupal tooling, and no git — no repository,
   no checkout, no working tree, no git objects. Fork resolution and verification
   use no credential; the only authenticated calls are the per-commit replay calls
   and the merge request, when one is needed.
5. Nothing the guest produces — file paths, contents, branch names, commit message
   — can alter which project or branch the host writes to. The payload type cannot
   express a destination at all.
6. Publication cannot complete without explicit human confirmation that shows what
   will change and where; declining publishes nothing, and no code path publishes
   as a side effect of another command.
7. A *commit* destination outside the `issue/` namespace is refused without a
   deliberate override. The merge request's `target_project_id` names the canonical
   parent, as drupal.org requires, and is derived from the fork's own
   `forked_from_project` rather than from anything the guest produced.
8. Publication behaves identically whether the change set originates in a main
   clone or in a worktree at any path, including a harness-created nested one — the
   worktree's location never affects where the work lands.
9. In a VM holding several drupal.org modules, publication targets the fork of the
   module actually being worked on, never the VM's create-time clone URL.
10. A blocked or non-routed drupal.org endpoint is reported as a refusal by
    drupal.org, naming what happened, rather than as a generic API error or a
    misleading 404.
11. Existing GitHub behavior is unchanged: `internal/provision/gitcred.go` is
    untouched, a VM created with a `GH_TOKEN` authenticates exactly as it does
    today, and no stored secret changes.
12. A payload can express every change a real commit contains — creates, updates,
    deletes, and renames, with binary content carried as base64 — and a change set
    that removes a file publishes correctly rather than silently omitting it.
13. A replay that fails partway reports exactly which commits landed and which did
    not, and re-running the publish completes the remainder rather than duplicating
    what is already on the branch.
14. Publication works identically from both surfaces, with all resolution, payload,
    guard, and client logic living once in `internal/drupalorg` rather than being
    implemented twice.
15. The workstation needs no `glab` and no `gh` for publication: the client is
    `net/http`, and `glab` is required only by `probe-drupalorg-api.sh`.
16. Every task in the blueprint passes the per-task completion gate —
    `/code-review --fix`, then `/simplify`, then tests re-run — before being marked
    complete.

## Self Validation

### Per-Task Completion Gate

This gate applies to **every task** generated from this plan, and is a
precondition for marking any task complete — not a final sweep at the end.

Before a task may be considered done, and after its own tests pass:

1. Run `/code-review --fix` against the task's changes, and apply or consciously
   reject every finding. This pass hunts correctness defects.
2. Then run `/simplify`, and apply or consciously reject its findings. This pass is
   quality only — reuse, simplification, efficiency — and deliberately does not look
   for bugs, which is why it runs second rather than instead.
3. Re-run the task's tests afterwards. Both commands modify the working tree, so a
   green result from before they ran is not evidence the task is still correct.

A task whose gate has not been run is not complete, regardless of whether its
functional work appears finished.

One point of possible confusion is worth pre-empting: these are Claude Code
commands, and this plan requires strict harness neutrality in what sandbar *ships*.
There is no contradiction. Harness neutrality constrains the workflow delivered to
Drupal developers, not the tooling used to build sandbar. An implementer working in
a different harness should run that harness's closest equivalent review and
simplification passes; the requirement is the two passes and their order, not these
two command names.

### Post-Implementation Validation

These steps inspect the real system and must be executed after implementation.
Several require a real drupal.org account, a real issue fork, and a host PAT.
Because plan 20 has not been implemented, the guest-side checkout used by these
steps is created by hand — an ordinary `git clone` of a drupal.org module and a
`git worktree add` — rather than by any sand bootstrap.

1. **Re-probe the drupal.org access ceiling.** Using a real drupal.org account,
   query the GitLab API for the caller's access level on an issue fork **of a module
   that account maintains**, and attempt a token-creation call against it. Both came
   back negative and are not expected to change: the access level is Developer (30)
   even for the parent project's own maintainer on a fork they created themselves,
   and `POST .../access_tokens` is blocked at the edge for everyone. Use the
   maintained-module variant specifically — the weaker probe, against a module the
   account merely contributes to, cannot distinguish "this account is not privileged
   enough" from "no account can be".
2. **Re-run the endpoint map.** Run `probe-drupalorg-api.sh` and confirm the content
   endpoints this design depends on still route to GitLab and that the credential
   endpoints are still blocked. A change here is a platform change and must be
   noticed deliberately rather than discovered as a bug.
3. **Verify fork resolution needs no credential.** With no credential available,
   resolve a real issue number plus module name to a fork path by convention and
   list its branches. Confirm success and confirm the issue branch appears. This is
   the check that keeps the host free of Drupal tooling.
4. **Verify the PAT loader's file-mode refusal.** Point configuration at a PAT file
   readable by group, then by other, and confirm the loader refuses outright rather
   than warning. Confirm the token value never appears in configuration, in logs, or
   in any error message.
5. **Verify the PAT never reaches a guest.** After a publish, search a provisioned
   VM exhaustively — environment, credential store, per-directory `.env` files, git
   config, shell history — and confirm the account PAT is absent in every form.
6. **Verify publication end to end, on both surfaces.** From a real issue worktree
   holding **several** local commits, publish — once through `sand publish`, and once
   through the Landing pane row. Confirm each commit appears on the correct issue
   fork's branch in order, with its own message and original authorship, carrying
   exactly the intended file changes; that the pre-existing branch was appended to
   rather than recreated; and that a merge request exists against the canonical
   parent project with the right target branch, created only if one was absent.
   Verify from a clean anonymous clone, not from the machine that published.
7. **Verify publication requires human confirmation.** Confirm the publish path
   cannot complete without an explicit confirmation, that declining publishes
   nothing at all, and that no code path publishes as a side effect of any other
   command.
8. **Verify no git artefacts reach the host.** During and after a publish, confirm
   the workstation created no git repository, no checkout, and no working tree for
   the guest's code, and that nothing from the guest was executed on the host.
9. **Verify the split of authority under adversarial input.** Confirm that nothing
   the guest produces — file paths, contents, branch names, commit message — can
   alter which project or branch the host writes to, and that the host refuses a
   destination outside the `issue/` namespace without a deliberate override.
10. **Verify the destination cannot be influenced by a hostile payload.** Plant a
    hostile payload in the guest — file paths and contents containing another
    project's path, absolute paths, `..` traversal, and shell metacharacters — then
    publish. Confirm the change lands on the intended fork and branch only, that no
    request is made against any other project, and that traversal-style paths are
    refused rather than normalised into somewhere unintended.
11. **Verify publication targets the right module.** With two drupal.org modules
    cloned in one VM, work an issue on the second and publish. Confirm the change
    lands on that module's fork — not the first module's, and not the VM's
    create-time clone URL.
12. **Verify publication is location-independent.** Publish the same change set
    from a main clone and from a worktree at a nested path, and confirm both land
    identically. The worktree's location must not affect the destination.
13. **Verify the guest cannot publish, by trying.** From inside a VM, attempt a
    direct `git push` to the issue fork, to a different issue fork, and to the
    canonical project. All three must fail for want of any credential. Then attempt
    the same through the `drupalorg` CLI and `glab`. This is the check that the
    blast-radius claim is structural rather than merely intended.
14. **Verify the guest's read loop still works with no credential.** In the same VM,
    confirm an anonymous clone, a merge-request read, a pipeline status read, and a
    raw file fetch all succeed. The loop working *and* step 13 failing are both
    required; either alone proves nothing.
15. **Verify a blocked endpoint is reported honestly.** Point the publisher at an
    endpoint known to be blocked and confirm it reports that drupal.org refused the
    request — recognising an HTML body where JSON was expected — rather than
    surfacing a generic API error or a misleading 404.
16. **Verify content-API edge behaviors.** Confirm an oversized payload is detected
    and explained before sending, and that a `last_commit_id` conflict — produced by
    moving the fork underneath a prepared publish — is reported as a re-derivable
    conflict rather than a failure.
17. **Verify the replay's failure and resumption behavior.** Cause an interior call
    of a multi-commit replay to fail — a revoked token mid-run, or a `last_commit_id`
    conflict induced by moving the branch underneath. Confirm the report names
    exactly which commits landed and which did not, and that it does not present a
    partial success as a clean failure. Then re-run the same publish and confirm it
    replays only the remainder, leaving no duplicated commits on the branch.
18. **Verify the payload carries every kind of change.** Publish a change set that
    creates a file, modifies a file, **deletes** a file, renames a file, and adds a
    binary file. Confirm all five land correctly on the fork — the deletion in
    particular, which the prior payload design could not express at all.
19. **Verify the merge-request target is derived, not supplied.** Confirm the
    canonical parent is read from the fork's `forked_from_project` anonymously, that
    a hostile payload cannot alter `target_project_id` or `target_branch`, and that
    publishing again onto a branch whose merge request is already open does not
    attempt to create a second one.
20. **Run the fine-grained token's negative control, before documenting the escape
    hatch.** Most of this check is already answered and must not be re-litigated:
    the UI does expose fine-grained tokens, `Code: Push` exists, a single issue-fork
    project is a selectable boundary, the `issue` group is **not**, and a
    fork-bounded token pushes to its fork successfully. What has **not** been run is
    the negative control that carries the whole blast-radius claim: take a token
    bounded to one issue fork and attempt a push to a canonical `project/<module>`
    the developer maintains. It **must** fail. Until it does, the escape hatch stays
    undocumented.
21. **Verify the host needs no forge CLI.** With neither `glab` nor `gh` on `PATH`,
    confirm a publish completes on both surfaces. The client is `net/http`; only
    `probe-drupalorg-api.sh` may require `glab`.
22. **Run the existing suite.** `go test ./... -race` must pass, and coverage must
    not fall below the committed floor enforced in CI.

## Documentation

Yes — this plan requires documentation updates, both human- and agent-facing.

- **`docs/reference/security-model.md`** — document the agent-decides-content /
  host-decides-destination split, why no drupal.org credential enters a VM, and the
  blast-radius reasoning behind it: a contributor may hold push access to modules on
  tens of thousands of sites, so the only safe credential in an agent-controlled VM
  is none. Also state the limit honestly — the host still holds a powerful
  credential and publishes agent-authored content with it, and drupal.org's edge
  allowlist is not a containment boundary.
- **A user-facing guide to publishing** under `docs/using-sand/`, covering how a
  change set gets from a guest checkout to a merge request on both surfaces, what the
  confirmation shows, and three things a contributor will otherwise discover the hard
  way: that a publish **replays each local commit** rather than squashing, so local
  history is what appears on the fork; that a replay which fails partway leaves the
  earlier commits public and is recovered by re-running the publish, not by repairing
  the fork; and that commits published this way are not GPG-signed. Written for
  someone who knows Drupal and not sandbar. When plan 20 lands, this becomes part of
  the end-to-end drupal.org guide rather than a separate page.
- **An honest answer to "why can't I just push?"**, placed where the contributor
  meets the friction rather than buried in a security page. A fine-grained token
  bounded to one issue fork demonstrably works, so a contributor who finds that out
  will assume it was overlooked. Say that it works, that it is bounded to a single
  fork, and that it costs a manual web-UI token per issue because GitLab exposes no
  API to create one. Describe the manual escape hatch and what accepting it gives up
  — a credential inside an agent-controlled VM, which drupal.org's edge will not
  contain if it leaks. **Do not write this up until validation step 17 has passed.**
- **`docs/using-sand/secrets.md`** — state plainly that drupal.org work needs no
  guest secret at all, and why: the credential lives on the workstation and the VM
  never authenticates to drupal.org. This is a notable exception to the page's whole
  premise and should not be left implicit.
- **`docs/reference/files-and-state.md`** — document the host-side account PAT file
  at `${XDG_CONFIG_HOME:-~/.config}/sandbar/drupalorg.token`, its required mode, that
  its path is a convention rather than a configured value and why, and that it is
  deliberately not part of the guest secrets store.
- **`AGENTS.md`** — describe the publication mechanism and the reason no drupal.org
  credential belongs in a guest, so future agent-driven work does not reintroduce
  one.
- **drupal.org policy** — record, in the user-facing guide, that publication is a
  human-initiated action under drupal.org's PAT policy, and note that the DA has not
  been formally consulted.
- **Follow-up record** — the pre-existing behavior that GitHub worktrees inherit the
  main clone's token, uncovered during this investigation and deliberately left
  unchanged for compatibility, should be filed rather than lost.

## Resource Requirements

### Development Skills

Go, for the credential handling and the publication client. Working knowledge of
GitLab's REST API, specifically the repository-commits and merge-requests
endpoints. Familiarity with `internal/landgh`'s injection-safety invariant and with
the `internal/profiles/token.go` credential pattern. Enough Drupal contribution
experience to judge whether the resulting publish step is what a contributor
actually needs. Working knowledge of git's credential subsystem is needed only for
the documented escape hatch, not for the build.

### Technical Infrastructure

A workstation with a host PAT file at the correct mode. A provisioned sand VM with
a hand-made drupal.org checkout for the guest-side steps. Network access to
`git.drupalcode.org`. `glab` on the host for the endpoint probe.

### External Dependencies

A real drupal.org account with push access to at least one real issue fork —
preferably a fork of a module the account maintains, so the access-ceiling probe
and the token negative control are conclusive — and an account-level PAT with API
scope held on the workstation. End-to-end validation cannot be done without both,
and it creates public, permanent artefacts on drupal.org. The Drupal Association
should be consulted before this is promoted widely; endpoint availability is
established by probe and needs re-checking rather than discovering.

## Integration Strategy

Publication is a sibling of `internal/landgh` and is shaped like it, down to
having the same two surfaces: `internal/drupalorg` holds fork resolution, the
payload type, the guard, and the client; `cmd/sand/publish.go` is the headless
entry point, as `cmd/sand/land.go` is; and the TUI action fills the arm
`internal/ui/landing.go`'s `classifyLandRow` already reserves for a non-GitHub
forge. No publication logic lives in either surface.

The one place it departs from `landgh` is transport. `landgh` shells out to the
user's own `gh`; this adapter speaks `net/http` directly, on the typed-client
pattern `internal/pve` already establishes, so the workstation needs no `glab` and
no forge CLI at all. `landgh`'s injection-safety invariant still applies with more
force here, since the payload is agent-authored by construction: guest data is never
interpolated into a URL or a command, and the payload type cannot express a
destination. The deeper difference is that `landgh` calls `gh` for metadata about a
branch the guest already pushed, whereas this adapter carries the content itself —
so the destination must come from host-side arguments and host-side anonymous
lookups alone, and never from anything the guest produced.

The host-side account PAT follows the *spirit* of `internal/profiles/token.go` — one
loader, and a hard refusal of over-permissive file modes — but not its configuration
shape. `token.go`'s `TokenFile` is a field on a Proxmox connection profile and
`profiles.yaml` is a per-location store, so a workstation-global credential is
recorded there nowhere sensible. It lives at a fixed conventional path instead:
`${XDG_CONFIG_HOME:-~/.config}/sandbar/drupalorg.token`. `profiles.yaml` is
unchanged, no new config schema is introduced, and the guest-facing secrets store is
not extended.

`internal/provision/gitcred.go` is **untouched**. Its `recognizedForgeTokens` table
keeps its single GitHub entry, and no `useHttpPath` configuration is written into
any guest.

Collecting the change set from the guest uses sandbar's existing guest-execution
and transfer paths rather than introducing new plumbing. `internal/checkouts`
already sweeps guests for git checkouts and worktrees and may later surface
per-checkout publication state, but that is not required here and is deliberately
left out.

Nothing in this plan changes the Lima, Proxmox, or remote-SSH provider layers, the
TUI's board model, or the create form's existing fields.

**Relationship to plan 20.** This plan is implemented first and depends on nothing
in plan 20. The dependency runs the other way: plan 20's issue-number entry point
reuses the fork resolution built here rather than reimplementing the
`PROJECT-ISSUE_NUMBER` convention a second time. Plan 20 also inherits the property
this plan establishes — that the guest holds no drupal.org credential — and must
not weaken it.

## Notes

The work order's framing — "gitlab api tokens are issue, and not project scoped" —
turned out to be right about the shape and wrong about the mechanism, and the
mechanism is what decided the design. Each issue fork is a separate *project*, so
per-issue scoping is natural in principle. Two of the three ways to exploit that are
closed: a *classic* personal access token cannot be restricted to a project at all,
and a *project access token* requires Maintainer, which no one holds on an issue
fork. The third — a **fine-grained** access token bounded to one fork — genuinely
works, and was verified pushing. It fails on economics rather than capability:
GitLab exposes no API that can create one, and the boundary cannot be widened to the
`issue` group, so every issue costs a manual trip through the web UI. That is why
this plan publishes from the host instead of narrowing a token, and it is worth
carrying into the documentation, because "the secure option exists but cannot be
automated" is a far less obvious conclusion than "the secure option does not exist."

The corollary is worth stating for anyone who revisits this: the deciding constraint
is drupal.org's *token-creation* surface, not GitLab's token model. If the Drupal
Association ever exposed fine-grained token creation by API, or if a group-level
boundary became selectable by non-members, the in-guest push loop would become cheap
and this decision should be reopened.

The finding that actually decided the plan is blunt: **on drupal.org, every
credential that is safe enough to hand an agent is one a human must create by hand,
one issue at a time.** Once that is true, the cheapest secure design is not a better
token but no token — which is why publication moved to the host, and why the two
halves of the original work order turned out to have separate answers rather than
the single shared one the earlier revisions expected. That separation is also what
made this extraction from plan 20 clean.

### Known unresolved gaps

Carried deliberately rather than papered over. Downstream task generation should
treat these as known, not as oversights.

- **The fine-grained token's negative control has not been run.** A token bounded to
  a single issue fork has been verified *permitting* a push to that fork; it has not
  been verified *refusing* a push to a canonical `project/<module>`. This blocks
  nothing in the build, which places no token in a guest, but the manual escape hatch
  must not be documented as safe until it passes.
- **The in-guest iteration loop is a known, accepted cost.** Responding to a failing
  pipeline means a host-side confirmed publish per attempt rather than a `git push`.
  This is a deliberate trade and the most likely source of contributor complaint. If
  it bites in practice, the reopening condition is recorded above.
- **The Drupal Association has not been consulted, on three separate questions.**
  Whether host-side publishing via the commits API is acceptable under the automation
  policy given a human confirms each publish — the reading here is that it is "an
  individual action a user could already perform", but that is an inference and not a
  ruling. Whether they would allowlist `POST /projects/:id/access_tokens` — worth
  asking only if the escape hatch ever becomes a supported path. And whether
  AI-assisted merge requests must be disclosed — governance issue 3565917 proposes it
  but it is not ratified. None block building; all should be settled before this is
  promoted widely.
- **A replay can leave a branch half-published, and nothing can undo it.** Decided
  in refinement 7: a publish replays each local commit as its own call, so an
  interior failure leaves the earlier commits public and unrevocable. The mitigation
  is precise reporting plus resumability, not rollback, and the accepted residue is
  that a contributor can end up with a fork in a state they did not ask for. This is
  a consequence of a decision taken knowingly, not an oversight.
- **The guest-to-host transfer of the change set is named but not specified.** The
  plan says publication uses sandbar's existing guest-execution and transfer paths.
  That is directionally right — but the sweep those paths were built for reads short,
  delimiter-framed text, whereas a payload carries whole file contents, possibly
  binary and possibly large. Framing, encoding, and the size check are a task-level
  design problem that should be treated as its own unit of work rather than assumed
  to fall out of the existing plumbing.

### Change Log

- **2026-08-27 (created by extraction from plan 20)**: The publication and credential
  half of plan 20 was extracted into this plan, at the user's direction, to be
  implemented first. The grounds were that plan 20 itself certified the two mechanisms
  share no code and can be built and tested independently; that every external blocker
  — three unanswered Drupal Association questions, the host PAT, a real account for
  end-to-end tests — sits entirely on this side; that the test regimes differ in kind,
  since validation here creates public, permanent merge requests while plan 20's runs
  wholly inside a local VM; and that a combined blueprint would have run to roughly
  twenty tasks under a heavy per-task gate. Because this plan ships first, two items
  moved here that an earlier analysis had assigned to plan 20: the "why can't I just
  push?" documentation with its escape hatch, and the fine-grained token negative
  control that gates it. Fork resolution by convention is also built here, with plan
  20 consuming it. All findings are carried unchanged from plan 20's five refinement
  sessions; nothing was re-decided during the extraction.
- **2026-08-28 (refinement 7 — surfaces, config home, commit shape, and three
  corrections)**: Resolved the three things the plan left a downstream reader unable
  to act on. Publication gets **both** surfaces — `cmd/sand/publish.go` and a TUI
  action — over a shared `internal/drupalorg`; the TUI half turned out to be filling
  an arm `classifyLandRow` already reserves for a non-GitHub forge rather than
  building a new pane. The host PAT moves to a **fixed conventional path**
  (`~/.config/sandbar/drupalorg.token`), because `token.go`'s `TokenFile` is a
  Proxmox-*profile* field and `profiles.yaml` had no coherent home for a
  workstation-global credential; a new global config file was rejected as scope
  creep. A publish **replays each local commit** rather than squashing, closing the
  gap the plan had carried as undecided — and the accepted cost, partial failure with
  no rollback, is now a named risk, a success criterion, and a validation step
  requiring precise reporting and resumability. Three defects were corrected against
  live probes: the merge-request call is **cross-project** (verified on
  `project/dubbot`: 241450 -> 106348, target `2.x`), not "against the issue branch",
  so the `issue/` guard is now scoped to commit destinations while the merge-request
  target is derived from `forked_from_project` anonymously; the issue branch **almost
  always already exists** (`issue/drupal-3181657` carries `3181657-*` branches), so
  `start_branch` is the exception rather than the rule; and the payload could not
  express a **deletion or a rename**, and is now an ordered list of commits whose
  file actions carry an explicit kind and encoding. Transport was settled as
  `net/http` on the `internal/pve` pattern, leaving `glab` a dependency of the probe
  script alone. Commit authorship was made explicit as per-commit and guest-supplied
  by necessity under replay. The guest-to-host transfer of the payload was added to
  the unresolved gaps, since the existing sweep plumbing was built for short framed
  text rather than whole file contents.

## Execution Blueprint

**Validation Gates:**
- Reference: `/config/hooks/POST_PHASE.md`
- Plus this plan's own **per-task completion gate** (Self Validation): every
  task runs `/code-review --fix`, then `/simplify`, then re-runs its tests
  before it may be marked complete.

### Dependency Diagram

```mermaid
graph TD
    T1["001: Payload type + path validation"]
    T2["002: Host PAT loader"]
    T3["003: Anonymous client + fork resolution"]
    T4["004: Destination guard + confirmation"]
    T5["005: Commit replay, MR, resumption"]
    T6["006: Guest change-set collection"]
    T7["007: sand publish CLI"]
    T8["008: TUI Landing pane action"]
    T9["009: Documentation"]

    T1 --> T4
    T3 --> T4
    T1 --> T6
    T1 --> T5
    T2 --> T5
    T3 --> T5
    T4 --> T5
    T4 --> T7
    T5 --> T7
    T6 --> T7
    T4 --> T8
    T5 --> T8
    T6 --> T8
    T5 --> T9
    T7 --> T9
    T8 --> T9
```

No cycles: every edge runs from a lower task ID to a higher one.

### ✅ Phase 1: Foundations — types, credential, and anonymous reads
**Parallel Tasks:**
- ✔️ Task 001: Publication payload type and repository-path validation
- ✔️ Task 002: Host-side drupal.org PAT loader at a conventional path
- ✔️ Task 003: Anonymous GitLab client and fork resolution by convention

### ✅ Phase 2: The split of authority, and getting the work out of the guest
**Parallel Tasks:**
- ✔️ Task 004: Destination guard and the shared confirmation summary (depends on: 001, 003)
- ✔️ Task 006: Collect the change set from the guest checkout (depends on: 001)

### Phase 3: Publication
**Parallel Tasks:**
- Task 005: Authenticated publish — commit replay, merge request, partial failure and resumption (depends on: 001, 002, 003, 004)

### Phase 4: Surfaces
**Parallel Tasks:**
- Task 007: `sand publish` CLI surface (depends on: 004, 005, 006)
- Task 008: TUI Landing pane publish action (depends on: 004, 005, 006)

### Phase 5: Documentation
**Parallel Tasks:**
- Task 009: Security model, publishing guide, and the agent-facing record (depends on: 005, 007, 008)

### Post-phase Actions

Each phase ends with `POST_PHASE.md`: lint/format checks, a conventional
commit for the phase, and the blueprint's task/phase status updated in place.

### Deliberately not tasked

Two items from the plan are **not** decomposed into tasks, and their absence is
a decision rather than an oversight:

- **The fine-grained token escape-hatch documentation.** The plan gates it on a
  negative control that has not been run — a token bounded to one issue fork
  must be shown to *fail* pushing to a canonical `project/<module>` — and says
  the documentation "must not ship until it does". That control needs a real
  drupal.org account and a hand-built token. Task 009 records the withheld
  section and its precondition instead of writing it.
- **The plan's Post-Implementation Validation steps 1–21.** These inspect the
  real system: they need a real drupal.org account with push access to an issue
  fork, an account-level PAT on the workstation, and a provisioned VM, and they
  create **public, permanent** merge requests on drupal.org. They are executed
  by a human against real infrastructure under `POST_EXECUTION.md`, not by a
  task subagent. Step 22 — `go test ./... -race` and the coverage floor — is
  runnable and is part of the post-execution gate.

### Execution Summary
- Total Phases: 5
- Total Tasks: 9

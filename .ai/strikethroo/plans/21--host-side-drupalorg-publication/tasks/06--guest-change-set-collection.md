---
id: 6
group: "drupalorg-publish"
dependencies: [1]
status: "completed"
created: 2026-08-28
model: "sonnet"
effort: "high"
skills:
  - go
  - shell-scripting
complexity_score: 8
complexity_notes: "The plan names this as its own unresolved design problem: the existing sweep plumbing carries short delimiter-framed text, while a payload carries whole file contents that may be binary and large."
---
# Collect the change set from the guest checkout

## Objective

Read, from a targeted guest checkout, the local commits not yet on the issue
fork's branch — each commit's message, author name and email, and its file
actions with contents — and parse them host-side into task 1's payload type.
The guest side is read-only, holds no credential, and never names a
destination.

## Skills Required

`go` for the framing/parsing and the payload construction; `shell-scripting`
for the deliberately dumb guest-side command.

## Acceptance Criteria

- [ ] A command builder produces the guest-side script for a given checkout
      path and a base ref, following `internal/checkouts/sweep.go`'s
      "deliberately dumb guest side" philosophy: plain `git` reads plus
      `base64`, with every interesting decision made in Go.
- [ ] The guest command emits, for each commit not in the base ref, in
      **oldest-first order**: its message, author name, author email, and one
      record per changed file carrying the change kind (from
      `git diff-tree --name-status -M`), the path, the previous path for a
      rename, and — for the kinds that have one — the file's resulting content
      at that commit, base64-encoded on the wire.
- [ ] Framing is explicit and distinct from the sweep's and the heartbeat's
      delimiters, so a host-side bug that read the wrong stream fails loudly
      rather than interleaving silently. Content is length-prefixed or
      delimiter-framed in a way that cannot be confused by file content,
      because unlike the sweep this stream carries arbitrary bytes.
- [ ] The host-side parser converts the stream into a `ChangeSet`, choosing
      text or base64 encoding per task 1's helper (base64 only when the
      content is not valid UTF-8, so ordinary patches stay readable in the
      confirmation), validating every path with `ValidateRepoPath`, and
      refusing the whole change set if any path is refused.
- [ ] A total-size cap is enforced host-side with a clear message, and the
      guest command refuses to emit a single file above a stated cap rather
      than streaming an unbounded blob.
- [ ] The parser is unit-tested against **synthetic captured text**, with no
      VM: a multi-commit stream including a create, an update, a delete, a
      rename, a binary file, a UTF-8 filename, and a commit message containing
      the framing delimiter.
- [ ] Nothing in the collected data names a project, branch, remote, or URL —
      the parser constructs only task 1's types, which cannot express one.
- [ ] The guest command is executed through the existing provider guest-exec
      path (`provider.Provider.RunArgv`) rather than new plumbing, and guest
      data is passed as argv elements, never interpolated into a shell string.
- [ ] `go test ./internal/drupalorg/... -race` passes.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- The base ref for "not yet on the fork" is the fork's issue branch as fetched
  by the guest's own remote-tracking ref where one exists; where it does not,
  the caller supplies the base. Do not make a network call from the guest for
  this — `internal/checkouts/sweep.go`'s "No network, ever" rule applies, and
  remote truth is confirmed host-side.
- `git` reads only: `rev-list`, `log`, `diff-tree`, `show`/`cat-file`. No
  writes, no fetch, no push.
- Binary detection is the host's job, from the bytes: valid UTF-8 becomes text
  encoding, anything else base64. Do not rely on `.gitattributes`.

## Input Dependencies

- Task 1: `ChangeSet`, `Commit`, `FileAction`, `ValidateRepoPath`, and the
  encoding chooser.

## Output Artifacts

- `internal/drupalorg/collect.go` — the guest command builder and the
  host-side parser.
- `internal/drupalorg/collect_test.go` — parser tests against synthetic
  streams, including the adversarial framing and binary cases.

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

The plan lists this under **Known unresolved gaps**, and names precisely why
it is its own task:

> The plan says publication uses sandbar's existing guest-execution and
> transfer paths. That is directionally right — but the sweep those paths were
> built for reads short, delimiter-framed text, whereas a payload carries whole
> file contents, possibly binary and possibly large. Framing, encoding, and the
> size check are a task-level design problem that should be treated as its own
> unit of work rather than assumed to fall out of the existing plumbing.

So treat framing as the actual design decision. Base64-encoding every file's
content on the wire is the simplest thing that cannot collide with a
delimiter — it costs 33% on the wire and removes an entire class of framing
bug. Decode host-side, then choose the *payload's* encoding from the decoded
bytes (valid UTF-8 -> text, so the confirmation stays readable; otherwise
base64). Those are two different encodings for two different purposes and the
comment should say so, because conflating them is the obvious mistake.

A workable guest shape, per commit `c`:

```sh
git -C "$dir" log -1 --format='%H%n%an%n%ae%n%B' "$c"   # then a delimiter
git -C "$dir" diff-tree --no-commit-id --name-status -M -r "$c"
# per non-delete entry:
git -C "$dir" show "$c:$path" | base64 -w0
```

`git rev-list --reverse <base>..HEAD` gives oldest-first order, which is the
order a replay must send.

`--name-status -M` reports `A`, `M`, `D`, and `R100`-style rename entries with
two paths — that is where the payload's `move` kind comes from, and it is the
reason the plan's earlier "paths and their resulting contents" payload was
insufficient.

Mirror `internal/checkouts/sweep.go` for the token-substitution style
(`strings.Replacer`, not `fmt.Sprintf`, so the shell's own `%s` needs no
escaping) and for the doc-comment habit of explaining why the guest side is
dumb.

For the "message containing the framing delimiter" test: this is the case that
proves the framing works. If the design cannot survive a commit message
containing the delimiter, change the framing (length prefixes) rather than
sanitising the message — the message is content and must reach drupal.org
verbatim.

**Test philosophy — write a few tests, mostly integration.** Meaningful tests
verify custom business logic, critical paths, and edge cases specific to this
application. Test *your* code, not the framework or library. Write tests for:
custom business logic and algorithms; critical user workflows and data
transformations; edge cases and error conditions for core functionality;
integration points between components; complex validation logic. Do **not**
write tests for: third-party library functionality; framework features; simple
CRUD without custom logic; trivial getters/setters; obvious functionality that
would break immediately if incorrect. Combine related scenarios into one test
rather than one per method.

The parser against synthetic text is the whole test surface here — the sweep
package establishes exactly this pattern, and it is what makes the logic
testable without a VM.

</details>

### Per-task completion gate (required by the plan)

This task is not complete until, after its own tests pass:

1. `/code-review --fix` has run against this task's changes and every finding
   was applied or consciously rejected.
2. `/simplify` has then run, and its findings applied or consciously rejected.
3. The task's tests were **re-run** afterwards and pass.

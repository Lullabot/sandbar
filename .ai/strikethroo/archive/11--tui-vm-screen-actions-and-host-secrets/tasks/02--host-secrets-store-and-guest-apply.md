---
id: 2
group: "host-state"
dependencies: []
status: "completed"
created: 2026-07-09
model: "opus"
effort: "xhigh"
complexity_score: 9
complexity_notes: "Introduces host-side plaintext secret storage and renders values into a file the guest shell sources. An escaping bug is a shell injection into the guest; a plumbing bug puts a token on argv where every host user can read it via ps. Highest-risk task in the plan."
skills:
  - go
  - security
---
# Host secrets store and guest apply

## Objective

Create `internal/secrets`, a per-VM host store of arbitrary `KEY=VALUE` pairs
persisted at `${XDG_DATA_HOME:-~/.local/share}/sandbar/secrets.json` with mode
`0600` inside a `0700` directory; and `provision.ApplySecrets`, which streams the
rendered pairs into a guest's `~/.config/sandbar/secrets.env` (mode `0600`) over
**stdin, never argv**.

The rendering is the security-critical surface: the guest `source`s the file, so
a value containing `'`, `$(…)`, or a backtick must reach the shell as literal
text and never execute.

## Skills Required

- **go** — package design, `encoding/json`, atomic writes, file modes.
- **security** — POSIX single-quote escaping, avoiding argv leakage of secrets.

## Acceptance Criteria

- [ ] New package `internal/secrets` exposes at minimum: a `Store` type, `Load()`
      (tolerant of a missing file; a corrupt file yields a warning error **and** a
      usable non-nil empty store, mirroring `registry.Load`), `Get(vm string)
      map[string]string`, `Set(vm string, pairs map[string]string) error`,
      `Remove(vm string) error`, and `ValidKey(k string) bool`.
- [ ] On-disk schema is `{"version":1,"vms":{"<name>":{"KEY":"VALUE"}}}`. A file
      with a `version` greater than the binary understands is refused with an
      "upgrade sand" error and an empty store, mirroring task 1's registry.
- [ ] `secrets.json` is written via temp-file-then-`rename` with the temp file
      created at `0600` **before** any secret bytes are written to it, and its
      parent directory created at `0700`. There must be no instant at which a
      world-readable file contains a secret.
- [ ] `ValidKey` accepts exactly `[A-Za-z_][A-Za-z0-9_]*` and rejects everything
      else (`""`, `2FOO`, `A-B`, `A B`, `A=B`, `A$B`).
- [ ] A `Render(pairs map[string]string) string` function emits one
      `export KEY='VALUE'` line per pair, **keys sorted ascending**, with every
      `'` in a value replaced by `'\''`, and a trailing newline. It must be
      byte-stable across calls for equal input.
- [ ] `provision.ApplySecrets(ctx, lima *lima.Client, name, user string, pairs
      map[string]string, out io.Writer) error` streams `Render(pairs)` into the
      guest over stdin. When `pairs` is empty it **removes** the guest file
      instead of writing an empty one.
- [ ] The rendered secret text appears **only** in the command's stdin. It must
      not appear in any element of the command's argv.
- [ ] The guest file lands at `~/.config/sandbar/secrets.env`, owned by `user`,
      mode `0600`, with its parent directory `~/.config/sandbar` at `0700`.
- [ ] `managed-vms.json` is untouched by this task; `secrets.json` is a distinct
      file.
- [ ] Verification: `go test ./internal/secrets/... ./internal/provision/... -v`
      passes. Specifically:
      ```
      go test ./internal/secrets/... -run 'Render|ValidKey|Perm' -v
      go test ./internal/provision/... -run ApplySecrets -v
      ```
      Expected `PASS`, including a `Render` case asserting that the input
      `{"Q": "it's $(id) `whoami`"}` renders **exactly**
      `export Q='it'\''s $(id) ` + "`whoami`" + `'` followed by a newline, and an
      `ApplySecrets` case asserting the fake runner recorded the secret in stdin
      and that `strings.Contains(strings.Join(args, " "), secretValue)` is false.
- [ ] Verification: `go build ./... && go vet ./...` succeed.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- New package `internal/secrets`. `ApplySecrets` goes in a **new file**,
  `internal/provision/secrets.go` — not in `provision.go`. Task 4 edits
  `provision.go` and `staging.go` concurrently, and agents share one working tree.
- Mirror `internal/registry`'s XDG path derivation exactly: `$XDG_DATA_HOME`,
  else `$HOME/.local/share`, else `.` — then `sandbar/secrets.json`.
- `ApplySecrets` must use the same in-guest hygiene pattern as
  `provision.runProvision`'s `inGuestScript` (`internal/provision/provision.go:27`):
  `install -m 600 /dev/null "$f"` before `cat > "$f"`.
- `lima.Client.Shell(ctx, name, stdin io.Reader, out io.Writer, args ...string)`
  is the transport. Look at how `runProvision` calls it with
  `bytes.NewReader(vars)` as stdin.
- The model in `internal/ui` is passed **by value** through `Update`, so whatever
  handle the TUI eventually holds must be copy-safe: no `sync.Mutex`, no
  `sync.Map` embedded by value. A `*Store` pointer is fine.

## Input Dependencies

None. Task 1 runs in parallel; mirror its versioning approach but do not depend
on its code.

## Output Artifacts

- `internal/secrets` package: `Store`, `Load`, `Get`, `Set`, `Remove`,
  `ValidKey`, `Render`.
- `provision.ApplySecrets` — consumed by task 9 (apply on start/restart) and
  task 8 (the editor persists through `Store.Set`).

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Read first:** `internal/registry/registry.go` (for the store shape, XDG path,
and tolerant-load posture) and `internal/provision/provision.go` lines 15–64 (for
`inGuestScript` and how `runProvision` streams vars over stdin).

**Rendering — this is the part to get exactly right.**

Inside POSIX single quotes, *nothing* is expanded: no `$`, no backtick, no
backslash escape. The only character that cannot appear is `'` itself. The
standard escape is to close the quote, emit an escaped quote, and reopen:
`'` → `'\''`.

```go
// shellSingleQuote wraps s in single quotes for safe inclusion in a POSIX shell
// file. Inside single quotes no expansion occurs, so the only character needing
// special handling is the quote itself: close, emit an escaped quote, reopen.
func shellSingleQuote(s string) string {
    return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Render emits the guest env file: one `export KEY='VALUE'` per pair, keys
// sorted so the output is byte-stable for equal input.
func Render(pairs map[string]string) string {
    keys := make([]string, 0, len(pairs))
    for k := range pairs {
        keys = append(keys, k)
    }
    sort.Strings(keys)
    var b strings.Builder
    for _, k := range keys {
        b.WriteString("export " + k + "=" + shellSingleQuote(pairs[k]) + "\n")
    }
    return b.String()
}
```

Do **not** use `%q`, `strconv.Quote`, or double quotes — those permit `$` and
backtick expansion and would be a live injection.

**Atomic 0600 write.** Create the temp file in the *same directory* as the target
(so `rename` stays on one filesystem) and set the mode at creation, not after:

```go
if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil { ... }
f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
// write, Sync, Close, then:
os.Rename(tmp, path)
```

`os.OpenFile` with an explicit mode is subject to umask, which can only *remove*
bits — it can never add them — so `0600` is a safe ceiling. Remove the temp file
on any error path.

**ApplySecrets.** Model it on `runProvision`:

```go
const applySecretsScript = `set -eu -o pipefail
d="$HOME/.config/sandbar"
f="$d/secrets.env"
install -d -m 700 "$d"
install -m 600 /dev/null "$f"
cat > "$f"
`

const clearSecretsScript = `set -eu -o pipefail
rm -f "$HOME/.config/sandbar/secrets.env"
`

func ApplySecrets(ctx context.Context, cli *lima.Client, name, user string, pairs map[string]string, out io.Writer) error {
    if len(pairs) == 0 {
        return cli.Shell(ctx, name, nil, out, "sudo", "-u", user, "bash", "-c", clearSecretsScript)
    }
    body := secrets.Render(pairs)
    return cli.Shell(ctx, name, strings.NewReader(body), out, "sudo", "-u", user, "bash", "-c", applySecretsScript)
}
```

Note `sudo -u "$user"` (not `-iu`): we need `$HOME` to be the target user's home.
Verify which form the existing code uses — `Reset` step 6 uses `sudo -iu` for
`direnv allow`. Use whichever reliably yields the target user's `$HOME`; if
`-u` does not, use `sudo -iu <user> bash -c` and confirm `$HOME` is the guest
user's home, not root's. **This is worth checking against a real VM**, because
writing the file into `/root/.config` would silently do nothing useful.

If `internal/provision` importing `internal/secrets` creates an import cycle,
it will not — `secrets` must not import `provision`. Keep `Render` in `secrets`.

**Tests.**

`internal/secrets`:
- `TestRender_EscapesAdversarialValues` — table-driven. Cases must include a bare
  `'`, `$(id)`, a backtick expression, an embedded newline, a `\`, and a space.
  Assert exact expected output strings.
- `TestRender_StableOrder` — same map, rendered twice, byte-identical; and a map
  with keys inserted out of order renders sorted.
- `TestValidKey` — the accept/reject table from the acceptance criteria.
- `TestSet_FilePermissions` — after `Set`, `os.Stat(path).Mode().Perm()` is
  `0600` and the parent dir is `0700`.
- `TestLoad_FutureVersionRefused` — mirrors task 1.
- `TestLoad_CorruptFileWarnsButReturnsUsableStore` — non-nil store, non-nil error.

`internal/provision` — reuse the existing fake `lima` runner that
`provision_test.go` already uses to record calls (read it; it records `f.calls`
and `f.streams`). Add:
- `TestApplySecrets_SecretNeverOnArgv` — call with
  `{"TOK": "SENTINEL_SECRET_VALUE"}`, assert the recorded stdin stream contains
  `SENTINEL_SECRET_VALUE`, and assert **no** recorded argv element contains it.
- `TestApplySecrets_EmptyRemovesGuestFile` — call with an empty map, assert the
  recorded command is the `rm -f` script and stdin is nil/empty.

**Testing philosophy.** Write a few tests, mostly integration. Meaningful tests
verify custom business logic, critical paths, and edge cases specific to this
application — test *your* code, not the framework or library.

Write tests for: custom business logic and algorithms; critical user workflows
and data transformations; edge cases and error conditions for core functionality;
integration points between components; complex validation logic or calculations.

Do NOT write tests for: third-party library functionality; framework features;
simple CRUD operations without custom logic; trivial getters/setters or static
configuration; obvious functionality that would break immediately if incorrect.

Here that means: the escaping table and the argv-hygiene assertion are the
critical paths and must exist. Do not test `os.Rename` or `encoding/json`.

</details>

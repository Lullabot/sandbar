---
id: 16
group: "image-build"
dependencies: [3]
status: "pending"
created: 2026-09-13
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Changes what the image ships for two user-facing tools and introduces a first-run network dependency; the shim must be robust because a broken one makes the tool unavailable rather than merely stale."
skills:
  - ansible
  - shell
---
# Install Claude Code and Codex on first use instead of baking them in

## Objective

Stop baking the Claude Code and Codex binaries into the image. Ship a small shim for each that installs the real tool on first invocation, so users always get a current version instead of whatever was frozen into a month-old image — and the image sheds ~532 MB of content that would have been stale anyway.

## Skills Required

`ansible` for the role changes; `shell` for the shim scripts.

## Acceptance Criteria

- [ ] `roles/claude-code` no longer runs the vendor installer during the base phase; it installs a shim at the same path instead.
- [ ] `roles/codex` likewise.
- [ ] **Everything else these roles do is preserved**: `~/.claude` and its templated `settings.json`, the `~/.claude.json` onboarding seed, `~/.codex/config.toml`, the PATH line in `.bashrc`, and the `sand-xclip` / `sand-wl-paste` clipboard shims. Only the binary install becomes lazy.
- [ ] On first invocation each shim installs the real tool, then execs it transparently, passing through all arguments, stdin/stdout/stderr, and the exit code.
- [ ] On subsequent invocations there is **no** network call and no measurable overhead — the real binary has replaced or superseded the shim.
- [ ] Failure is clear and non-destructive: if the install fails (no network, vendor endpoint down), the user gets an actionable error naming the tool and the cause, and the shim remains in place so a later attempt still works. A failed install must never leave a broken or empty binary on PATH.
- [ ] Concurrent invocations do not corrupt the install (two shells running `claude` at once must not race into a half-written binary).
- [ ] Verification: build an image and confirm neither binary is present — `~/.local/share/claude/versions/` and `~/.codex/packages/` are absent or empty, while the shims and all config files ARE present. Paste the check.
- [ ] Verification: report the new compressed image size against the pre-change measurement, in MiB and percent.
- [ ] Verification: boot or chroot into the image with network and run `claude --version` and `codex --version`; confirm each installs on first run and reports a version. Paste the output.
- [ ] Verification: run each a second time and confirm it is fast and makes no install attempt. Paste the output.
- [ ] Verification: simulate a failed install (e.g. break DNS resolution) and confirm the error is clear and the shim survives. Paste the output.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- The current installs are: `roles/claude-code/tasks/main.yml` (`curl | bash` the official installer to `~/.local/bin/claude`) and `roles/codex/tasks/main.yml` (`curl | sh` the official installer).
- Measured footprints being removed: Claude 214 MB (`~/.local/share/claude/versions/2.1.270`, a single binary), Codex 318 MB (`~/.codex/packages/standalone/releases/<ver>/bin/`: a 251 MB `codex` plus a 67 MB `codex-code-mode-host`).
- The shims must work for a **non-root user** and install into that user's home, matching where the vendor installers put things today.
- The vendor installer typically writes to `~/.local/bin/<tool>` — i.e. it will overwrite the shim itself. That is acceptable and even desirable (the shim is self-eliminating), but the shim must handle it correctly: after installing, exec the path that now holds the real binary.
- Do not attempt to pre-seed a version or pin one. The entire point is to get whatever is current at first run.
- These roles are gated in `site.yml` on `toolset_claude` / `toolset_codex` today. Task 11 removes those gates; this task must work either way — do not depend on task 11 having landed.

## Input Dependencies

- Task 03's build script and roles, to rebuild and measure.

## Output Artifacts

- Updated `roles/claude-code` and `roles/codex` plus the two shim scripts — consumed by task 04 (hygiene gate, which must still pass) and task 14 (documentation must describe the first-run behaviour).

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

**Why this is being done.** Two reasons, and the second matters more than the first. The size saving is real (~532 MB uncompressed, ~200 MB compressed, the largest single reduction available). But the correctness argument is stronger: Claude Code and Codex release very frequently, while images are rebuilt monthly. Anything baked in is stale on arrival, so users would pay ~532 MB of download for binaries that immediately update themselves anyway. Lazy install trades a one-time first-run delay for always-current tooling and a much smaller image.

**Shim shape.** Keep it small and boring — a POSIX `sh` script, not bash-specific:

```sh
#!/bin/sh
# Lazy installer for <tool>. Replaced by the real binary on first successful run.
set -eu
target="$HOME/.local/bin/<tool>"
marker="$HOME/.local/state/sand/<tool>.installing"

# If the real binary is already here (not this shim), just run it.
...
mkdir -p "$(dirname "$marker")"
# Serialize concurrent first-runs.
if ! mkdir "$marker.lock" 2>/dev/null; then
  echo "<tool>: another shell is installing it; waiting..." >&2
  # wait for the lock to clear, with a timeout
fi
echo "<tool>: installing on first use (this happens once)..." >&2
if ! <vendor install command>; then
  rmdir "$marker.lock" 2>/dev/null || true
  echo "<tool>: install failed. Check network access and try again." >&2
  exit 1
fi
rmdir "$marker.lock" 2>/dev/null || true
exec "$target" "$@"
```

**The self-overwrite subtlety.** The vendor installer writes to the very path the shim occupies. A running shell script keeps its file handle, so being overwritten mid-execution is survivable, but the safest structure is: install, then `exec "$target" "$@"` so the newly written binary takes over the process. Verify this works in practice rather than reasoning about it — run the shim, let it install, and confirm the arguments reached the real tool.

**Detecting "am I the shim or the real thing".** The simplest reliable check is a marker inside the script that the real binary will not contain, or checking whether the installed-version directory exists (`~/.local/share/claude/versions/` for Claude, `~/.codex/packages/standalone/releases/` for Codex). Avoid recursing into yourself — a shim that execs itself is an infinite loop, and it is an easy mistake here.

**Concurrency.** Sandbar's tmux-backed multi-shell means a user can plausibly have two shells open and type `claude` in both. Use an `mkdir`-based lock (atomic on POSIX filesystems) rather than a file-existence check, with a bounded wait and a clear message.

**Preserve the configuration.** The point is to lazy-load *binaries*, not configuration. `settings.json`, the `~/.claude.json` onboarding seed and `~/.codex/config.toml` are tiny, stable, and part of what makes the sandbox pleasant on first use — they stay baked in. So do the `sand-xclip` and `sand-wl-paste` clipboard shims, which are sandbar's own code, not vendor binaries.

**Failure must be recoverable.** The worst outcome is a user whose `claude` is a broken stub. If the install fails, leave the shim intact and exit non-zero with a message naming the tool and suggesting a retry. Test this with DNS broken, not just by reasoning about it.

**Measure.** Report the compressed image size before and after this change. The pre-change reference from the zstd build is 1,151,401,984 bytes (1098.1 MiB); if task 15's trims have also landed by the time you build, note that and compare against whatever the current committed build produces instead.

</details>

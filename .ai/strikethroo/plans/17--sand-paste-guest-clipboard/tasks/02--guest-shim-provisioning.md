---
id: 2
group: "sand-paste-image"
dependencies: []
status: "completed"
created: 2026-07-17
model: "sonnet"
effort: "high"
skills:
  - bash
  - ansible
complexity_score: 5
complexity_notes: "Small shell scripts plus a provisioning task, but it sits on the image-only security surface (it is what serves content to the agent), so it takes the risk-floor effort tier despite modest size."
---
# Guest Clipboard Shim: scripts + provisioning

## Objective
Ship two guest shims named `xclip` and `wl-paste` that serve a single sand-managed
PNG file to Claude Code's native paste probe (so Ctrl-V works with no display
server), and provision them onto the guest `PATH` via the `roles/claude-code`
Ansible role. The shims have NO text-serving path — image-only by construction.

## Skills Required
- `bash` — the shim scripts (arg parsing for the exact probes Claude Code issues).
- `ansible` — a task in `roles/claude-code` to install them to `/usr/local/bin`.

## Acceptance Criteria
- [ ] `xclip` shim: `-t TARGETS -o` prints `image/png` **iff** the single-slot
      file exists (else prints nothing, exit 0); `-t image/png -o` (or any
      `image/*` target) streams the file's bytes; any non-image target prints
      nothing, exit 0.
- [ ] `wl-paste` shim: `-l`/`--list-types` prints `image/png` iff the file exists;
      `--type image/png` streams the bytes; otherwise empty.
- [ ] Both resolve the slot as `${HOME}/.sand/clip/latest.png`.
- [ ] `roles/claude-code` installs both to `/usr/local/bin` mode `0755`
      (matching how `roles/dev-tools` installs `/usr/local/bin/drupalorg`).
- [ ] Manual check documented and demonstrated:
      `printf '' > /tmp/x && HOME=/tmp mkdir -p /tmp/.sand/clip && cp <a.png> /tmp/.sand/clip/latest.png && HOME=/tmp xclip -selection clipboard -t TARGETS -o` prints `image/png`; with the file removed it prints nothing.
- [ ] `ansible-lint roles/claude-code` (if available) reports no new errors;
      otherwise `ansible-playbook --syntax-check` on the role's play passes.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- The shims must tolerate the exact invocations Claude Code emits (verified from
  the installed build): detection
  `xclip -selection clipboard -t TARGETS -o` / `wl-paste -l`, fetch
  `xclip -selection clipboard -t image/png -o` / `wl-paste --type image/png`.
- Persist-until-replaced lifecycle: the shim serves the same file on every read;
  it never deletes it (a consume-on-read shim would empty the slot between
  Claude's TARGETS call and its fetch call).
- Be permissive: serve the PNG for ANY `image/*` target requested.

## Input Dependencies
None. Phase-1 leaf. (The single-slot file is written at runtime by task 3; the
shim only needs to read whatever is there.)

## Output Artifacts
- `roles/claude-code/files/sand-xclip` and `sand-wl-paste` (or templates), plus
  the install task. Installed as `/usr/local/bin/xclip` and `/usr/local/bin/wl-paste`.
- The single-slot path contract (`~/.sand/clip/latest.png`) shared with task 3.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Shim `xclip` (POSIX sh):**
```sh
#!/bin/sh
# sand image-only clipboard shim. Serves ~/.sand/clip/latest.png to image
# probes; behaves as an empty clipboard otherwise. No text path.
slot="${HOME}/.sand/clip/latest.png"
want=""
while [ $# -gt 0 ]; do case "$1" in -t) want="$2"; shift 2;; *) shift;; esac; done
[ -f "$slot" ] || exit 0                 # empty clipboard
case "$want" in
  TARGETS) printf 'image/png\n' ;;       # advertise only png
  image/*) cat "$slot" ;;                # serve bytes for any image target
  *) : ;;                                # non-image -> empty
esac
exit 0
```

**Shim `wl-paste` (POSIX sh):** same slot; `-l`/`--list-types` → print
`image/png` iff file exists; `--type image/*` → `cat "$slot"`; else empty.
Parse `--type <mime>` and the bare `-l`.

**Ansible** (`roles/claude-code/tasks/main.yml`): add a `copy` task installing
both scripts to `/usr/local/bin` (`mode: "0755"`). Mirror the existing
`roles/dev-tools` `drupalorg` install (dest `/usr/local/bin/...`, mode 0755).
Put the script bodies under `roles/claude-code/files/`.

**Why /usr/local/bin:** it is on the default guest PATH and empty of a real
`xclip`/`wl-paste` (headless guest), so the shim is found first with no shadowing
hazard. Claude Code tries `xclip` before `wl-paste`, so `xclip` carries the
common case; ship both.

Do not add a `-i`/write path or a `text/plain` branch — the shim is read-only and
image-only by design.
</details>

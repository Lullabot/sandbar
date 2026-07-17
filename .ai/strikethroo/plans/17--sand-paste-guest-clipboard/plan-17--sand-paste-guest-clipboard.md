---
id: 17
summary: "Add `sand paste-image` — a host-initiated, image-only command that loads a host-clipboard image onto a shimmed guest clipboard so Ctrl-V works natively in Claude Code inside a sand VM"
created: 2026-07-17
---

# Plan: `sand paste-image` — Native Ctrl-V Image Paste Into Guest Agent Sessions

## Original Work Order

> claude code has support for pressing ctrl-v to paste an image, even in the
> terminal. It doesn't detect an image from my host. Is this an ssh problem, a
> terminal / tmux problem, a sand problem? Can we fix and support this? I don't
> even know how it's possible ctrl-v can work that way.
>
> Reading passwords is a real issue I want to avoid. Let's add a "sand paste"
> option - note it also needs to work with the TUI. But, I think this needs more
> refinement. When you paste, where does the image go? How do you easily get it
> into your re-attached agent sessions?
>
> Is there any way we could enable a clipboard inside of the VM? What would be a
> good workflow would be you select "paste" in sand, paste the content, and then
> you can press ctrl-v in claude.

## Plan Clarifications

| # | Question | Resolution |
|---|----------|------------|
| 1 | Is a live host↔guest clipboard bridge acceptable? | **No.** A standing bridge lets any guest process read the host clipboard, which routinely holds passwords. Rejected in favor of a host-initiated, one-shot, image-only command. |
| 2 | Is the design *truly* image-only on every platform? | Only if made explicit. macOS (`«class PNGf»` coercion) and Windows (`GetImage()` returns null) are image-or-nothing by construction; **Linux is not** — a bare typed `xclip`/`wl-paste` fetch is not a hard refusal on a text-only clipboard. Resolution: the host read MUST gate on an advertised `image/*` type first and fetch bytes only then; no image type ⇒ hard error, zero fetch. |
| 3 | How does the pasted image reach the agent — inject a path into tmux, or a real guest clipboard? | Enable a **clipboard inside the guest** so Ctrl-V works natively. Chosen over tmux path-injection; this also removes any need to touch tmux. |
| 4 | Real headless clipboard (Xvfb + xclip) or a shim? | **Shim (Option B).** Ship guest `xclip`/`wl-paste` shims that serve the sand-managed image file. No daemon, negligible image cost, and image-only by construction. Xvfb was rejected as heavier (standing X server per VM, larger base image). |
| 5 | Retention policy for stored pastes? | **Moot.** A clipboard holds one item; `sand paste-image` overwrites a single-slot file each time, so there is nothing to accumulate or prune. |
| 6 | Backwards compatibility? | Not applicable — purely additive (new command, new TUI verb, new guest files). No BC break. |
| 7 | Does `sand paste-image` auto-target the single running VM? | **No** — it mirrors `sand shell`: exactly one explicit VM name, honoring `--profile`. `cmd/sand/resolve.go` resolves the *connection profile*, not the VM; sand has no single-running-VM shortcut, so inventing one here would be inconsistent (corrected from the initial draft). |
| 8 | Which host platforms read the clipboard? | **macOS and Linux only** — the platforms sand/Lima run on — mirroring the `hostres_*.go` seam where `_other` is a stub. Windows/WSL is out of scope; the `other` build returns an "unsupported" sentinel (corrected from the initial draft, which overreached to PowerShell). |
| 9 | How are the image bytes written into the guest? | One guest round trip that creates the dir and writes the file — `Client.Shell` with the image on stdin (`mkdir -p <dir> && cat > <file>`) — against the **absolute** guest home resolved via `GuestHome`, not `~` expansion and not `Copy` (which creates no parent dirs). The clipboard is always read on the machine running sand; only the written bytes cross the remote hop. |
| 10 | Command/verb wording: "paste" or "paste image"? | **"paste image"** — the CLI command is `sand paste-image <vm>` and the TUI verb's help label is "paste image". The feature is image-only by design (text can never transit), so the name states the contract and forecloses the wrong expectation of a generic paste. |

Resolved design decisions (confirmed in refinement 2026-07-17):

| # | Decision | Resolution | Alternative considered |
|---|----------|------------|------------------------|
| A | TUI keybinding for the paste-image verb | **`v`** (confirmed free of collisions) | `i` (image) |
| B | Board behavior after a TUI paste | **Stay on board**, show a status-line result | Chain straight into attach |
| C | Staged image lifecycle | **Persist until replaced** — real-clipboard parity, simplest, and robust to Claude Code's two-call probe (TARGETS then fetch) | One-shot: consume/clear the slot after the fetch |

## Executive Summary

Claude Code's Ctrl-V image paste works by shelling out to a **local** clipboard
reader (on the machine Claude Code runs on): `xclip`/`wl-paste` on Linux,
`osascript «class PNGf»` on macOS, PowerShell `GetImage()` on Windows/WSL.
Inside a sand guest there is no display server and no clipboard tool, so Ctrl-V
finds nothing. This plan adds `sand paste-image` (a CLI command and a TUI tile verb)
that makes native Ctrl-V work while categorically refusing to expose host
clipboard **text** — the passwords a naive clipboard bridge would leak.

The flow: the user copies an image on the host, triggers `sand paste-image <vm>` (CLI,
mirroring `sand shell`'s argument contract, or the TUI verb), and presses Ctrl-V
inside Claude Code in the guest — where it renders as a normal `[Image #N]`
attachment. Under the hood, `sand paste-image` reads the clipboard **image-only** on
the machine sand itself runs on (gating on an advertised `image/*` type before
ever fetching bytes; macOS and Linux hosts only), writes the image into the guest
at a single-slot path in one guest round trip (`Client.Shell` with the bytes on
stdin, creating the directory and file together), and relies on a pair of
lightweight guest **shims** named `xclip` and `wl-paste` to serve that image to
Claude Code's own paste probe. There is no live bridge, no display server, and
no daemon.

This approach was chosen because it delivers the native Ctrl-V gesture the user
asked for with the strongest security posture available: nothing auto-syncs from
the host, the host read is one-shot and structurally image-only, and the guest
"clipboard" can only ever contain an image the user explicitly pasted. It reuses
sand's per-platform seam pattern (clipboard read) and its Runner plumbing (guest
delivery over the same transport `sand shell`/transfers use), so it works
identically for local and remote-host VMs, and it touches neither tmux nor the
attach path.

## Context

### Current State vs Target State

| Current State | Target State | Why? |
|---------------|--------------|------|
| Ctrl-V in Claude Code inside a guest finds no clipboard tool and no display server; image paste silently does nothing. | `sand paste-image` loads a host-clipboard image onto a shimmed guest clipboard; Ctrl-V in Claude renders `[Image #N]`. | Restore the paste gesture users expect, inside the sandbox. |
| The only way to get an image to a guest agent is a manual file copy plus typing the path. | One action (`v` in the TUI or `sand paste-image`) then a native Ctrl-V. | Remove friction; match muscle memory. |
| A host↔guest clipboard bridge would expose all host clipboard content (including passwords) to every guest process. | Host clipboard is read one-shot, image-only, on explicit user action; text can never transit. | Eliminate the credential-leak surface that made a bridge unacceptable. |
| No guest clipboard exists (headless: no `DISPLAY`, no `xclip`/`wl-paste`). | Guest ships `xclip`/`wl-paste` shims that serve a sand-managed image file. | Give Claude Code's native paste probe something to read without a display server. |
| sand knows about tmux in exactly one place (`internal/lima/attach.go`). | Unchanged — paste delivery uses a guest clipboard, not tmux injection. | Keep tmux knowledge centralized; avoid a second tmux touchpoint. |

### Background

**How Ctrl-V can work in a terminal at all.** The keypress sends one byte down
the TTY; Claude Code intercepts it and spawns a clipboard-reader subprocess on
its own host. The image never traverses the terminal. Verified against the
installed Claude Code build: the Linux probe is
`xclip -selection clipboard -t TARGETS -o | grep -E "image/(png|jpeg|jpg|gif|webp|bmp)" || wl-paste -l | grep -E …`
to detect, then `xclip -selection clipboard -t image/png -o` (falling back to
`wl-paste --type image/png`) to fetch; macOS uses `osascript … «class PNGf»`;
Windows/WSL uses PowerShell `[Clipboard]::GetImage()`.

**Why not SSH X11 forwarding or a socket bridge.** Both would forward the whole
host clipboard, text included. The threat that killed the bridge is concrete:
password managers put secrets on the clipboard, and a bridged guest process
could read them at will. The design must therefore be host-initiated and
image-only.

**Why the guest is headless.** The guest has no `DISPLAY`/`WAYLAND_DISPLAY` and
no clipboard binaries, so Claude Code's probe finds nothing today. The shim
supplies real binaries on `PATH` that Claude Code invokes unmodified — it does
not patch or depend on Claude Code's internals beyond the standard `xclip` CLI
contract.

**Relevant repo facts that constrain the implementation.**
- The image must be read off the clipboard of *the machine sand runs on* (the
  user's workstation, even when driving a remote Lima host); a terminal never
  delivers image bytes when pasting into it, so sand cannot receive an image
  "through" its TUI, and it never touches the remote host's clipboard.
- Clipboard reading is inherently per-platform and belongs behind a small
  build-tagged seam, mirroring the existing `internal/ui/hostres_*.go` pattern:
  `darwin` and `linux` implement it; `hostres_other.go` (`//go:build !linux &&
  !darwin`) is a stub, so non-unix hosts return an "unsupported" sentinel rather
  than a broken probe.
- Guest file delivery goes through sand's Runner plumbing. `Client.Shell(ctx,
  name, stdin, out, argv…)` runs a guest command with stdin piped, so
  `mkdir -p <dir> && cat > <file>` writes the image and creates its directory in
  one round trip; the remote-host two-stage hop is handled by the same transport
  as `sand shell`, and no test requires a real `limactl` (per `AGENTS.md`).
  `Client.Copy`/`copyAcrossHop` is the alternative but creates no parent dirs and
  does not expand `~`, so it would need a separate `mkdir` and an absolute path.
- The guest home is **not** reconstructable from a username (Lima puts it at
  `/home/<user>.guest`); it is resolved via `lima.GuestHome(instanceDir)`, the
  same source `AttachArgv`/`internal/ui.guestHome` use.
- TUI verbs live in `internal/ui/commandreg.go` as key bindings with
  `enabledFor` guards (the same shape as `S` shell, `u` upload, `g` download);
  `v` and `i` are both currently unbound. The transfer verbs open a file-browser
  wizard (`startTransfer` → `launchCopy`); paste has no file to pick, so it is a
  *direct* async action reporting a status-line result, not a wizard.
- CLI commands live under `cmd/sand/`; `shell.go` is the closest sibling and the
  contract to mirror — `runShell` requires exactly one VM name and a `--profile`
  flag. `cmd/sand/resolve.go` resolves the connection *profile*, not the VM.
- The guest shim scripts install to `/usr/local/bin` (mode 0755, on `PATH`; the
  same place `roles/dev-tools` drops the `drupalorg` binary) via the
  `roles/claude-code` provisioning role, since they exist to serve Claude Code.

## Architectural Approach

The feature is four cooperating pieces: a **host clipboard-read seam**
(image-only), a **guest delivery** step (single-slot copy through existing
plumbing), a **guest shim pair** (provisioned `xclip`/`wl-paste`), and the two
**entrypoints** (CLI command + TUI verb) that orchestrate the first two. Only the
host-read seam and the shim are genuinely new surface; delivery and orchestration
reuse established sand patterns.

```mermaid
sequenceDiagram
    participant U as User
    participant H as sand process (workstation)
    participant HC as Local clipboard
    participant G as Guest clip dir
    participant CC as Claude Code (guest)
    U->>H: sand paste-image VM  /  TUI verb
    H->>HC: gate - advertised image type? (macOS/Linux)
    alt no image type
        HC-->>H: none
        H-->>U: error (nothing read, nothing sent)
    else image present
        HC-->>H: PNG bytes (image-only fetch)
        H->>G: Client.Shell stdin - mkdir then write latest.png
        H-->>U: status - image staged on VM clipboard
        U->>CC: Ctrl-V
        CC->>G: xclip -t TARGETS -o then -t image/png -o (shim)
        G-->>CC: image/png bytes
        CC-->>U: Image attached
    end
```

### Clipboard-Read Seam (image-only, local to the sand process)
**Objective**: Read an image off the clipboard of the machine running sand, and
make it structurally impossible to read text.

A build-tagged seam mirroring `hostres_*.go` exposes one operation: "return the
current clipboard image bytes, or a sentinel meaning *no image*." `darwin` and
`linux` implement it; `_other` (non-unix) returns an "unsupported" sentinel, so
the feature degrades cleanly rather than mis-probing. Each platform uses the same
probes Claude Code uses, but the contract is **gate-then-fetch**: first confirm an
`image/*` representation is advertised (`osascript «class PNGf»` coercion on macOS
is itself the gate; `xclip -t TARGETS -o` / `wl-paste -l` on Linux), and only then
fetch the bytes. A clipboard with no image type yields the *no image* sentinel and
**zero bytes are fetched** — there is no code path that returns non-image content.
This is the load-bearing security property and must be covered by tests that
assert a text-only clipboard produces the sentinel, never bytes.

The read always runs on the machine sand runs on — the workstation — regardless
of whether the target VM is local or on a remote Lima host. sand never reads the
remote host's clipboard; only the resulting bytes are sent onward.

**Format:** the seam normalizes to PNG (macOS `«class PNGf»` coerces to PNG;
Linux fetches `image/png`), because the guest shim always presents a single PNG.
A Linux *host* clipboard that advertises only a non-PNG image (e.g. JPEG with no
PNG flavor) is treated as *no image* in v1 rather than pulling in an image
converter — see Risks. macOS, the primary host platform, always coerces, so this
edge is Linux-host-only.

### Guest Delivery (single-slot write, one round trip)
**Objective**: Place the image where the guest shim can serve it, working
identically for local and remote-host VMs.

The image bytes are written to a single-slot guest path
(`<guest-home>/.sand/clip/latest.png`) in one guest round trip:
`Client.Shell(ctx, name, <image-on-stdin>, out, "sh", "-c", "mkdir -p <dir> &&
cat > <file>")`. This runs over the same transport `sand shell` uses, so the
remote-host two-stage hop is inherited, the directory is created together with the
file, and no test needs a real `limactl`. The guest home is the absolute path from
`lima.GuestHome(instanceDir)` — never a literal `~`, which the copy/shell
destination would not expand. The directory is created `0700` and the file `0600`
(defense in depth on a multi-user guest). Single-slot overwrite mirrors real
clipboard semantics (one item), which is why lifecycle decision C is
persist-until-replaced and there is nothing to prune. No tmux and no attach
interaction occur here.

`Client.Copy`/`copyAcrossHop` is the alternative delivery API but would require a
separate `mkdir` step and pre-resolved absolute path; the stdin-shell write is
preferred as one atomic round trip. Final API choice is left to task generation,
but either implementation MUST create the directory and use the absolute guest
home.

### Guest Shim Pair (`xclip` / `wl-paste`)
**Objective**: Give Claude Code's native paste probe a clipboard to read, with no
display server or daemon.

Two small scripts named `xclip` and `wl-paste` install to `/usr/local/bin`
(mode 0755, on `PATH`) via the `roles/claude-code` role, and answer exactly the
probes Claude Code issues:
- Target/type listing (`-t TARGETS -o`, `wl-paste -l`): advertise `image/png`
  **iff** the single-slot file exists; otherwise behave like an empty clipboard
  (clean exit, no output), so Ctrl-V with nothing staged correctly reports "no
  image."
- Image fetch (`-t image/png -o`, `--type image/png`): stream the file's bytes.
- Any non-image target (e.g. `text/plain`): behave as empty. The shim has **no**
  text-serving path — image-only by construction, independent of the read seam.

Lifecycle is persist-until-replaced (decision C): the shim serves the same
single-slot file on every read until the next `sand paste-image` overwrites it, matching
how a real clipboard retains its contents and staying robust against Claude Code's
two-call probe (it lists TARGETS, then fetches — a consume-on-first-read shim
would empty the slot before the fetch).

The shims are permissive about *which* `image/*` target is requested (serve the
staged PNG for any image target) so a future Claude Code tweak to its target list
does not break paste. Claude Code tries `xclip` first and only falls back to
`wl-paste`, so the `xclip` shim carries the common case; the `wl-paste` shim is
belt-and-suspenders. This is the only maintenance coupling in the design, and it
is to the decades-stable `xclip` CLI contract, not to Claude Code's private
behavior.

### Entrypoints (CLI command + TUI verb)
**Objective**: One host action that runs read → deliver, for both headless and
TUI users.

- **CLI `sand paste-image <vm>`**: mirrors `sand shell`'s contract exactly — exactly one
  explicit VM name plus a `--profile` flag (there is no single-running-VM
  shortcut; `resolve.go` resolves the profile, not the VM). It runs the clipboard
  read and guest delivery and reports a concise result (staged, or a specific
  reason nothing was — no image on clipboard, VM not running/unknown). Requires a
  running VM, the same guard as `sand shell`.
- **TUI verb** (key `v`, help label **"paste image"**, decision A): registered in
  `internal/ui/commandreg.go` beside `u`/`g`/`S` with an `enabledFor` guard
  restricting it to running VMs.
  Unlike the transfer verbs it opens **no** file-browser wizard (there is no file
  to pick); it is a direct async action that surfaces its outcome on the status
  line (e.g. `staged image on <vm> — press S then Ctrl-V`) and stays on the board
  (decision B). The single small write does not need the job registry's
  progress/cancel machinery — a plain async `tea.Cmd` returning a result message
  is sufficient. It acts on the VM the command registry hands it, never on an
  implicit `m.detail`, consistent with the transfer verbs.

## Risk Considerations and Mitigation Strategies

<details>
<summary>Security Risks</summary>
- **Host clipboard text (passwords) leaking to the guest**: the entire motivation.
    - **Mitigation**: gate-then-fetch host read that returns a *no image* sentinel
      and fetches zero bytes when no `image/*` type is advertised; a guest shim
      with no text-serving path at all. Two independent image-only layers. Tests
      assert a text-only clipboard yields no bytes on every platform.
- **A guest process reading the staged image**: any guest process could read the
  single-slot file.
    - **Mitigation**: accepted and bounded — the file only ever contains an image
      the user explicitly pasted, never host clipboard history and never host
      text; it is no more exposed than any other file the user copies in. Nothing
      auto-syncs from the host. The file is written `0600` in a `0700` directory
      as defense in depth on a multi-user guest.
</details>

<details>
<summary>Technical Risks</summary>
- **Linux clipboard read is not image-only by default**: a bare typed
  `xclip`/`wl-paste` fetch is not a hard refusal on a text-only clipboard.
    - **Mitigation**: make the advertised-`image/*`-type gate an explicit,
      tested precondition; never issue a typed fetch on an un-gated clipboard.
- **Per-platform clipboard probes are brittle across OS/tool versions**.
    - **Mitigation**: isolate behind the build-tagged seam (`darwin`/`linux` real,
      `_other` stub); degrade to a clear "no image found" / "unsupported" result
      rather than a crash or a wrong fetch; document the probe per platform.
- **Non-PNG image on a Linux host clipboard**: the guest shim always presents PNG,
  so the read normalizes to PNG. macOS coerces any image to PNG, but a Linux host
  clipboard advertising only a non-PNG flavor (e.g. JPEG, no PNG) cannot be
  fetched as `image/png`.
    - **Mitigation**: v1 treats that case as *no image* (a clear message), rather
      than adding an image-converter dependency. macOS — Lima's primary host
      platform — is unaffected. Revisit only if it proves to bite real users
      (YAGNI).
- **Shim coupling to Claude Code's paste command shape**: a future Claude Code
  change to its target list could bypass the shim.
    - **Mitigation**: shims are permissive (serve the staged image for any
      `image/*` target) and cover both `xclip` and `wl-paste`; the coupling is to
      the standard `xclip` CLI, not Claude Code internals. Documented as the known
      maintenance point.
</details>

<details>
<summary>Integration Risks</summary>
- **Remote-host VMs**: guest delivery must survive the two-stage hop.
    - **Mitigation**: reuse `Client.Copy`/`copyAcrossHop`; add no new transport.
- **Wrong-VM action from the TUI**: a verb firing on the focused tile must target
  that tile's VM.
    - **Mitigation**: take the VM from the command registry argument, mirroring the
      transfer verbs; never read `m.detail`.
- **Shim shadowing a real tool**: placing `xclip`/`wl-paste` on `PATH` could mask
  a genuinely-installed clipboard tool.
    - **Mitigation**: the guest is headless with no such tools today; the shim is
      the intended provider. Provision deterministically via the role and document
      it.
</details>

## Success Criteria

### Primary Success Criteria
1. On a running local VM, copying an image on the host, invoking `sand paste-image <vm>`,
   and pressing Ctrl-V inside Claude Code in the guest yields an `[Image #N]`
   attachment matching the copied image.
2. The TUI paste-image verb performs the same staging and reports its outcome on the
   status line, enabled only for running VMs.
3. With **text** (not an image) on the host clipboard, both entrypoints report
   "no image found" and provably fetch/transfer zero clipboard bytes — verified
   on macOS and Linux.
4. The same paste flow succeeds against a **remote-host** VM (delivery over the
   two-stage hop).
5. Ctrl-V in the guest with nothing staged is a correct no-op ("no image"),
   because the shim advertises no image type when the single-slot file is absent.
6. No new code references tmux; `internal/lima/attach.go` remains the sole tmux
   touchpoint. Tests do not require a real `limactl`.

## Self Validation

After all tasks are complete, an implementer should verify by:

1. Start a local VM, attach, and launch Claude Code. From a second host terminal,
   copy a known PNG to the host clipboard, run `sand paste-image <vm>`, then in the
   attached Claude Code press Ctrl-V; confirm the `[Image #N]` chip appears and
   the image is the one copied. Capture the terminal output as evidence.
2. Put plain text (a fake password string) on the host clipboard, run
   `sand paste-image <vm>`, and confirm it reports "no image found" and stages nothing
   (`ls ~/.sand/clip/` in the guest shows no new/updated file, or the prior slot
   is untouched). Repeat on macOS and Linux hosts.
3. In the TUI, focus a running VM's tile, press the paste-image verb, and confirm the
   status line reports the staged result; press it on a stopped VM and confirm the
   verb is disabled/absent.
4. With the single-slot file absent, press Ctrl-V in guest Claude Code and confirm
   it reports no image (shim advertises nothing).
5. Repeat step 1 against a remote-host VM and confirm the image arrives over the
   hop.
6. `grep -rn tmux internal/` shows no new matches outside `attach.go`; run the
   package tests and confirm none spins up a real `limactl`.

## Documentation

- Update user-facing docs (README / `docs/`) with the `sand paste-image` command, the
  TUI verb and its key, and the copy-image → `sand paste-image` → Ctrl-V workflow,
  explicitly stating it is image-only and never transmits clipboard text.
- Note the guest shim and single-slot path in the provisioning role's
  documentation so the headless-clipboard mechanism is discoverable.
- Update `AGENTS.md` / relevant AI-facing notes if the new host-read seam or shim
  introduces conventions future changes must respect (e.g. "the paste path is
  image-only by contract; do not add a text fallback").

## Resource Requirements

### Development Skills
- Go (CLI command + Bubble Tea TUI verb wiring).
- Clipboard internals on sand's host platforms (macOS `osascript`/pasteboard,
  Linux X11/Wayland selections); non-unix hosts are an "unsupported" stub.
- Ansible role authoring for guest provisioning.
- Shell scripting for the guest shims.

### Technical Infrastructure
- Existing `internal/lima` Runner/copy plumbing and `internal/ui` command
  registry + jobs subsystem.
- The build-tagged platform-seam pattern (`hostres_*.go`).
- A running local Lima VM and, for one validation, a remote-host target.

## Integration Strategy

The feature slots into existing seams without new transports: clipboard reads sit
in a new build-tagged seam mirroring `hostres_*.go` (`darwin`/`linux` real,
`_other` stub); guest delivery reuses the `Client.Shell` stdin path over the same
transport as `sand shell`; the CLI command joins `cmd/sand/`, mirroring
`shell.go`'s named-VM + `--profile` contract; the TUI verb joins `commandreg.go`
as a direct async action (no file-browser wizard, no job-registry progress bar).
The guest shim ships via the `roles/claude-code` provisioning role to
`/usr/local/bin`. tmux/attach are untouched.

## Notes

- Purely additive; no backwards-compatibility surface.
- Decisions A (keybinding `v`), B (stay-on-board), and C (persist-until-replaced
  lifecycle) are now **confirmed** (see the Resolved decisions table), not
  defaulted.
- The single-slot design intentionally omits paste history and retention; if a
  multi-item guest clipboard is ever wanted, it is a separate future work order
  (YAGNI).
- **Known limitation:** a Linux *host* clipboard holding only a non-PNG image is
  treated as "no image" in v1 (macOS always coerces to PNG). Documented as a
  Technical Risk.

### Change Log

- **2026-07-17 (creation):** initial plan — `sand paste-image`, Option B shim clipboard.
- **2026-07-17 (refinement):**
    - Corrected VM resolution: `sand paste-image` requires an explicit VM name +
      `--profile`, mirroring `sand shell`; removed the invented "single running
      VM" auto-resolution (`resolve.go` resolves the profile, not the VM).
    - Replaced the `Client.Copy`/`copyAcrossHop` delivery assumption with the
      `Client.Shell` stdin write (`mkdir -p && cat >`) — one round trip that
      creates the directory; noted the absolute-guest-home (`GuestHome`)
      requirement and that `~` does not expand.
    - Scoped the clipboard read to macOS/Linux hosts (`_other` = unsupported
      stub), matching `hostres_*.go`; removed the Windows/WSL host-read overreach.
    - Clarified the read is always local to the sand process, never the remote
      host; only bytes cross the hop.
    - Added the non-PNG-on-Linux-host limitation and `0600`/`0700` file perms.
    - Specified the shim install path (`/usr/local/bin` via `roles/claude-code`)
      and that the TUI verb is a direct action, not the transfer file-browser
      wizard.
    - Confirmed open decisions A/B/C with the user.
    - Renamed the command/verb to state the image-only contract: CLI
      `sand paste-image <vm>`, TUI verb label "paste image" (clarification 10).

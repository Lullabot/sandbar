---
id: 12
group: "documentation"
dependencies: [6, 9, 10]
status: "pending"
created: 2026-07-13
model: "sonnet"
effort: "medium"
skills:
  - technical-writing
---
# Document the tool-set, the base lifecycle, and the invariants a future agent could break

## Objective

Record the facts a future reader (human or agent) would otherwise get wrong — above all that clones inherit the base's mounts, so the apt-cache strip is a security control and not a tidy-up.

## Skills Required

- **technical-writing** — README.md, README-sand.md, AGENTS.md, and load-bearing code comments.

## Acceptance Criteria

- [ ] **README.md** documents: the base-image tool-set (how to select DDEV / Go / Java, and that the default is all three); the `--rebuild` flag and the form's "Rebuild base image" toggle, and when they are needed (notably after de-selecting a tool, because Ansible cannot uninstall); and the base self-refresh, so users understand why an occasional create is slower.
- [ ] **README-sand.md** updates the base-image lifecycle: staleness now triggers an **in-place re-apply** rather than a rebuild; staleness is keyed on playbook **content** plus the tool-set (not the git commit); and the base carries a build timestamp used for the 30-day refresh.
- [ ] **AGENTS.md** records four things:
  - (a) **Clones inherit the base's `lima.yaml`, mounts included.** This is the most surprising fact in the provisioning code — it is why the read-only playbook mount works inside the clone, and why the apt-cache mount is dangerous. Any writable mount added to the base **must** be stripped from the clone, and the strip is guarded by a test. Future agents must neither "fix" the base's cache mount by deleting it as an apparent invariant violation, nor add a writable mount to the clone path believing the base's precedent generalizes.
  - (b) `playbook_embed.go`'s `go:embed` set and the rsync filter in `internal/provision/provision.go` must stay in step — and the base version stamp now hashes exactly that fileset, so the drift test guards the stamp too.
  - (c) Every base mutation — build, rebuild, re-apply, refresh, **and `--rebuild`'s destroy** — belongs inside the base lock held by `prepareBaseAndClone`, and staleness/age decisions must be read **after** the lock is acquired. This is the property that makes concurrent creates safe and the easiest one to regress.
  - (d) The `docker` group is granted in the **base** phase, so finalize needs no bounce to make it effective — a correction to the folklore that motivated the unconditional bounce.
- [ ] **Code comment in `internal/ui/ansible.go`** (or at the timing code that feeds it): note that `==> ` lines **reset tile progress**, which is why per-phase timings are plain writes rather than `step()` banners. Without this, a later change "tidying" the timings into `step()` would silently break the tile's progress bar.
- [ ] **Code comment in `internal/provision/overlay.go`**: the `overlayHeader` comment currently states there is no writable host mount. It is amended at the mount site to state the exception, its justification, and the clone-side strip. (Task 10 writes this; this task verifies it landed and reads correctly.)
- [ ] **Profiling is documented**: how to turn on the profiling mode, and that it provisions `ansible.posix` on demand — so the measurement instrument stays usable after the `ansible-core` swap.
- [ ] Every documented flag/toggle/behavior actually exists in the code as described. Verify each claim against the implementation rather than against this task file.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Do not document behavior that was planned but not implemented. If a task landed differently from its plan (e.g. task 10 fell back to the `limactl copy` tarball instead of a mount, or task 9 kept the bounce for a hostname reason), **document what shipped**, not what was intended.

## Input Dependencies

- Tasks 6, 9, 10 — the user-facing surfaces (form toggles, bounce behavior, cache mount + strip) must be settled before they can be described accurately.

## Output Artifacts

- Updated README.md, README-sand.md, AGENTS.md.
- Verified load-bearing code comments in `overlay.go` and `ansible.go`.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Read the shipped code first.** This task runs last for a reason: several tasks have empirical decision points whose outcome is not knowable in advance (does the macOS mount work, or did we fall back to the tarball? does the hostname actually need a bounce?). Check what was implemented, then write that.

**AGENTS.md item (a) is the most important sentence in this task.** Write it so someone skimming cannot miss it. Something like:

> **Clones inherit the base image's `lima.yaml` — including its mounts.**
> `limactl clone` copies the base's entire instance directory. The only
> post-clone config write is `Configure`, which sets cpus/memory/disk **and
> strips writable mounts**. This is why the read-only playbook mount works
> inside a clone (finalize rsyncs from `/mnt/playbook`), and it is why the
> base builder's writable apt-cache mount MUST be stripped: work VMs run
> Claude unsupervised, and "delete the VM and everything it produced is gone"
> depends on there being no writable host mount. The strip is a security
> control, not a tidy-up. A test enforces it. Do not remove it, and do not
> add a writable mount to the clone path.

**Keep the READMEs user-facing.** README.md is for people using `sand`; it should not explain flocks or content hashes. README-sand.md carries the lifecycle detail. AGENTS.md carries the invariants.

</details>

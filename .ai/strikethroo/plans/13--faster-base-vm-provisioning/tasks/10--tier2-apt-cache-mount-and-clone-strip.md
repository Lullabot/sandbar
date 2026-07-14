---
id: 10
group: "tier-2-cache"
dependencies: [7]
status: "pending"
created: 2026-07-13
model: "sonnet"
effort: "high"
skills:
  - go
  - lima
complexity_score: 8
complexity_notes: "Security-critical. Clones inherit the base's lima.yaml wholesale, so a writable mount added to the base lands on every work VM unless it is explicitly stripped. The strip IS the security control."
---
# Tier 2: host apt archive cache on the base builder, stripped from every clone

## Objective

Make a base rebuild CPU-bound rather than network-bound by caching apt archives on the host — while **actively enforcing** the invariant that work VMs carry no writable host mount.

## Skills Required

- **go** — `internal/provision/overlay.go`, `internal/lima/client.go`.
- **lima** — overlay/template YAML, instance cloning, mount types and their ownership semantics.

## Acceptance Criteria

- [ ] The base overlay (`RenderBaseOverlay`) mounts a host cache directory **writable** (under the user's cache dir).
- [ ] During the base phase, an `/etc/apt/apt.conf.d/` fragment points `Dir::Cache::archives` at the mount and sets `Binary::apt::APT::Keep-Downloaded-Packages "true"` so apt retains rather than deletes fetched `.deb` files.
- [ ] **THE CRITICAL ONE — the mount is stripped from every clone.** Clones inherit the base's `lima.yaml` wholesale (`limactl clone` copies the whole instance dir; the only post-clone write is cpus/memory/disk). The post-clone `limactl edit --set` is extended to **remove the cache mount** from the clone's config, while **preserving the read-only playbook mount** (the clone still needs it — finalize rsyncs from `/mnt/playbook`).
- [ ] **A test asserts no clone carries a writable mount**, and it fails if the strip is removed. Verify this by deleting the strip locally, watching the test go red, and restoring it. A test that cannot fail is not a control.
- [ ] In a cloned VM: `limactl shell <vm> -- mount | grep -Ei 'virtiofs|9p|sshfs'` shows no *writable* host mount; the clone's `lima.yaml` has no cache-mount entry while the base's does.
- [ ] The `overlayHeader` comment in `overlay.go` — which today states flatly that there is **no writable host mount** — is amended **at the site where the mount is added** to state the exception, why it is sound for the base builder, and that the clone path strips it.
- [ ] With a warm cache, a `--rebuild` base rebuild re-downloads no (or negligibly few) `.deb` files — verify from apt's output (no `Get:` lines for previously-fetched packages, or a near-zero "Need to get" figure) and confirm the host cache directory is populated.
- [ ] The measured delta against the Tier 1 baseline is **recorded — whatever its size, including "negligible"**. The plan's own diagnosis is that bandwidth is not the bottleneck on a fast link, so a small win is a legitimate finding and must be reported, not papered over.
- [ ] `go vet ./...` and `go test ./...` are green.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- **The inheritance fact (this is the whole task).** There is exactly one overlay renderer: `RenderBaseOverlay` (`internal/provision/overlay.go` ~:48), whose sole caller is `BuildBase`. `Client.Clone` (`internal/lima/client.go` ~:139) runs `limactl clone base name`, which copies the base's entire instance directory **including its `lima.yaml`**. The only post-clone configuration write is `Configure` (~:146-149): `limactl edit --set '.cpus=… | .memory=… | .disk=…'`. Mounts are therefore inherited by every clone — and this is load-bearing today: finalize's rsync from `/mnt/playbook` **inside the clone** works only because the clone inherited the base's read-only playbook mount.
- **The security rationale.** The no-writable-mount invariant protects **work VMs**, where Claude runs unsupervised and where deleting the VM must provably remove everything it produced. The base builder is a different machine: identity-free, runs only our own playbook, no user code and no agent ever executes on it, disposable. Public `.deb` files are not an exfiltration channel. So a writable mount on the *base* is sound — but it must not reach the clones, and it does not stay off them by itself.
- **Known risk — host mount semantics.** Lima's default on macOS is reverse-sshfs; apt needs a `partial/` subdirectory with `_apt`-compatible ownership plus working rename/lock behavior. If that proves painful, the **pre-approved fallback** is to avoid mount semantics entirely: after a successful base build, `limactl copy` an archives tarball out to the host cache and push it back in before the next build. Same win, no permission surface, nothing to strip. Decide empirically on both macOS and Linux — do not decide by assumption. Note the fallback's copy cost is not free on a fast link, which is exactly why task 1's measurement settles it.
- Do **not** use apt-cacher-ng: five of the repos are HTTPS, needing remap rules plus a host daemon.

## Input Dependencies

- Task 7: the base lifecycle (build / re-apply) is settled, so the cache is exercised by both paths.

## Output Artifacts

- A host apt archive cache on the base builder.
- The clone-side mount strip and the test that enforces it.
- A recorded Tier 2 marginal-win measurement.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**1. Base overlay — add the writable mount.** In `RenderBaseOverlay`:

```yaml
mounts:
  - location: "{{ .PlaybookDir }}"
    mountPoint: /mnt/playbook
    writable: false
  - location: "{{ .AptCacheDir }}"     # e.g. ~/.cache/sand/apt-archives
    mountPoint: /mnt/apt-cache
    writable: true
```

**Amend `overlayHeader` at this site.** The header currently says there is NO writable host mount. It must now say:

```go
// NOTE — the ONE exception to the no-writable-mount rule, and why it is sound.
//
// The base builder gets a writable apt-archive cache mount. That is safe here
// and ONLY here: the base is identity-free, runs only our own playbook, no user
// code and no agent ever executes on it, and it carries nothing but public .deb
// files. Work VMs are the machines the invariant protects, and they must never
// have this mount.
//
// Clones DO inherit this file — `limactl clone` copies the base's whole instance
// dir including lima.yaml. So the cache mount is STRIPPED from every clone in
// Client.Configure. That strip is the security control, not a tidy-up. It is
// covered by a test; do not remove it.
```

Without this comment a future reader hits a writable mount that contradicts the file's own header and "fixes" it in one of two wrong directions.

**2. apt config in the guest** (base phase only):

```yaml
- name: Point apt's archive cache at the host mount
  ansible.builtin.copy:
    dest: /etc/apt/apt.conf.d/99-sand-archive-cache
    mode: "0644"
    content: |
      Dir::Cache::archives "/mnt/apt-cache";
      Binary::apt::APT::Keep-Downloaded-Packages "true";
  when: provision_phase | default('full') != 'finalize'
```

apt needs `/mnt/apt-cache/partial/` to exist and be writable by `_apt`. Create it and set ownership before the install pass:

```yaml
- name: Ensure the apt archive partial dir exists
  ansible.builtin.file:
    path: /mnt/apt-cache/partial
    state: directory
    owner: _apt
    mode: "0700"
```

If ownership cannot be set (reverse-sshfs on macOS), that is the signal to switch to the `limactl copy` tarball fallback. Try the mount first; measure; fall back if it fights you.

**3. THE STRIP — extend `Configure`.** This is the security control:

```go
// Configure sets a STOPPED clone's cpus/memory/disk — and strips the base
// builder's writable apt-cache mount.
//
// Clones inherit the base's lima.yaml wholesale (limactl clone copies the
// instance dir), so the cache mount arrives here whether we want it or not.
// Work VMs must carry NO writable host mount: that is the invariant that makes
// "delete the VM and everything it produced is gone" true. The read-only
// playbook mount is kept — finalize rsyncs from /mnt/playbook inside the clone.
func (c *Client) Configure(name string, cpus int, memory, disk string) error {
    expr := fmt.Sprintf(
        `.cpus=%d | .memory=%q | .disk=%q | .mounts |= map(select(.writable != true))`,
        cpus, memory, disk)
    return c.run("edit", "--set", expr, name)
}
```

(Verify the yq expression `limactl edit --set` accepts — Lima uses yq syntax. Select-out by `writable != true` keeps the read-only playbook mount and drops any writable one, which is the property we actually want to guarantee: *no writable mount*, not merely "not this specific mount".)

**4. The test — make it able to fail.**

```go
func TestCloneHasNoWritableMount(t *testing.T) {
    // Assert the Configure expr passed to limactl removes writable mounts.
    // Then, as a real guard, render the base overlay, apply the Configure
    // transform, and assert the resulting mount list contains the read-only
    // playbook mount and NOTHING writable.
}
```

Then **actually verify the test can fail**: delete the `.mounts |= map(...)` clause, run the test, confirm red, restore. Report that you did this.

**5. Measure and report honestly.** Run a `--rebuild` with a cold cache, then with a warm cache. Record both against the Tier 1 baseline from task 1. If the win is negligible because Tier 1 already took the network off the critical path, **say so plainly** — that is a legitimate finding and the plan explicitly asks for it.

</details>

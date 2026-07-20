---
id: 6
group: "provider"
dependencies: [3, 5]
status: "completed"
created: 2026-07-20
model: "opus"
effort: "xhigh"
complexity_score: 9
complexity_notes: "Multi-stage orchestration (image download, cloud-init create, agent wait, Ansible run, template convert) over an async task API, with concurrency hazards (template flock serialization) and partial-failure cleanup."
skills:
  - go
  - cloud-init-provisioning
---
# provider: base template build and the Create/Recreate/Reset lifecycle

## Objective

Implement the Proxmox provisioning lifecycle: build the sandbar base **PVE
template** from a cloud image, clone VMs from it, and implement `Create`,
`Recreate`, and `Reset` with progress streamed to the caller's writer.

## Skills Required

`go` (orchestration, context cancellation, cleanup on failure) and
`cloud-init-provisioning` (cloud images, cloud-init parameters, Ansible over SSH).

## Acceptance Criteria

- [ ] `go test ./internal/provider/ -race` passes with mock-server tests
      asserting:
      - `Create` refuses an existing target VM (name already in the pool) before
        issuing any mutating call.
      - `Create` passes `pool` on **both** the base create and the clone.
      - After any cloud-init config write, a `PUT .../cloudinit` (or a stop/start)
        is issued — asserted by the mock recording the call sequence.
      - Two concurrent `Create` calls against the same base template do **not**
        issue overlapping clone requests (serialized client-side).
      - A clone that fails mid-way triggers cleanup of the partial VM.
      - A task returning `exitstatus: "WARNINGS: 1"` is treated as success and
        the VM is **not** recreated.
- [ ] `Create`, `Recreate`, and `Reset` no longer return the task-5 stub error.
- [ ] Progress written to `out` includes a line per stage so the TUI shows
      movement during the multi-minute base build.

## Technical Requirements

- Base template name mirrors the existing convention (`sandbar-base`); reuse the
  existing constant rather than introducing a second one.
- Build pipeline: `DownloadURL(content=import)` → `CreateVM` with
  `import-from` → wait for agent → run the embedded Ansible playbook over SSH →
  stop → `ConvertToTemplate`.
- Reuse the existing embedded playbook and `provision` package's toolset/config
  handling. Do **not** fork the playbook or re-implement toolset flags.
- Clones: `CloneVM` with `full=1`, target `pool` and `storage`, then
  `ResizeDisk` up to the requested size, then start and wait for the agent.
- Serialize clones from the same source template with a per-template mutex.
- Honour `provision.CreateOptions` (notably the rebuild-base intent) and
  `provision.ResetOptions`. Where an option has no Proxmox meaning, no-op it
  explicitly with a comment rather than silently ignoring it.

## Input Dependencies

Task 3 (`internal/pve` VM endpoints), task 5 (`proxmoxProvider` core, agent wait,
guest IP, SSH transport).

## Output Artifacts

`internal/provider/proxmoxprovision.go` and tests — consumed by tasks 10, 11, 12.

## Implementation Notes

<details>

**Base build stages**, each writing a progress line to `out`:

1. **Ensure the cloud image is present.** `StorageContent` on the configured
   storage; if the image volid is absent, `POST download-url` with
   `content=import` and wait on the UPID. Accepted extensions are
   `ova|ovf|qcow2|raw|vmdk` — **`.img` is rejected by PVE**, so if the
   configured image URL ends in `.img`, fail early with a clear message naming
   the accepted set.
2. **Create the base VM** with `import-from=<storage>:import/<file>` and
   `scsi0=<storage>:0,import-from=...`. Pass `pool`. Use absolute
   storage-backed volids — **absolute filesystem paths are hard-gated to
   `root@pam` and fail even for a `root@pam` API token**, so they are never an
   option here.
3. **Resize** the imported disk up to the base floor (reuse `vm.BaseDiskFloor` —
   clones grow from it because a qcow2 cannot shrink live).
4. **Start** and **wait for the guest agent**, then resolve the IP (task 5).
5. **Run the playbook over SSH** against that IP using the existing provisioning
   entry points.
6. **Stop** the VM, then `ConvertToTemplate`.
7. **Stamp the base version** through `HostFiles` using the existing
   `provision` base-version machinery, so staleness detection works identically
   to the other providers.

Wrap the whole build in the existing base **advisory lock**
(`provision.lockBase` via `HostFiles.OpenLock`) so two `sand` processes on the
same laptop cannot build the same template twice.

**Create:**

```go
func (p *proxmoxProvider) Create(ctx, cfg, opts, out) error {
    // Refuse an existing target FIRST — the interface contract, and it avoids
    // a partial clone that would need cleaning up.
    if _, err := p.Get(cfg.Name); err == nil {
        return fmt.Errorf("vm %q already exists", cfg.Name)
    } else if !errors.Is(err, lima.ErrNoSuchInstance) {
        return err
    }
    ...
}
```

Then: ensure base (respecting `opts` rebuild intent) → clone → resize → start →
wait for agent → finalize pass.

**Clone serialization:**

```go
// Parallel clones from one template serialize on the template's server-side
// flock, which has a 10s timeout and produces
// "can't lock file ... - got timeout". Raising client timeouts does not help
// because the timeout is server-side, and there is no unlock endpoint reachable
// from a token identity (the skiplock path is gated on the caller being exactly
// root@pam, which a token identity never is). So: never contend in the first
// place.
```

A `sync.Mutex` keyed by template name in a package-level map, or a single mutex
on the provider — a single mutex is simpler and sufficient, since there is one
base per provider.

**Cleanup on failure.** If the clone succeeds but a later stage fails, delete the
partial VM (`DeleteVM` with `purge=1`) before returning, and say so in the error.
Mirror the existing partial-instance cleanup behaviour in
`internal/provision/cleanup.go` so the user experience matches the other
providers.

**Recreate**: force-delete then clone, skipping the exists-guard.

**Reset**: reuse the existing `provision.ResetOptions` semantics (preserving the
Claude login and/or the per-org project tree). The preservation mechanism is
guest-side file copying over SSH, which works unchanged here — do not invent a
Proxmox-specific mechanism.

**Do not** call `WaitTask` and then separately poll the VM's status as a
belt-and-braces check; `WaitTask` already classifies correctly, and a second
"is it really running" poll is where the `WARNINGS: n` bug usually creeps back in
through the side door.

</details>

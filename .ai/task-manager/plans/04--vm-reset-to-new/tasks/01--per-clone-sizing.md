---
id: 1
group: "per-vm-sizing"
dependencies: []
status: "pending"
created: "2026-06-27"
skills:
  - go
complexity_score: 5
complexity_notes: "Touches three files (vm, provision/overlay, provision/provision) plus lima client and tests; the limactl resource-config mechanism is the main uncertainty and is pinned to `limactl edit --set`."
---
# Per-clone sizing: small base floor + grow/configure clone

## Objective
Make a VM's `cpus`, `memory`, and `disk` take effect on the *clone* (not just the shared base), so a reset/create can size a VM independently — including an effective "shrink" of disk relative to a previous VM. Achieve this by building the base image with a small virtual-disk **floor** and configuring each clone to the requested sizes (growing the disk) before its first start.

## Skills Required
- `go` (Lima client wrapper + provisioner sequence + table-driven tests)

## Acceptance Criteria
- [ ] A `BaseDiskFloor` constant (value `"20GiB"`) is defined in the `vm` package and documented.
- [ ] `RenderBaseOverlay` emits `disk: "20GiB"` (the floor) instead of `cfg.Disk`; `cpus`/`memory` continue to use `cfg`.
- [ ] `lima.Client` gains a `Configure(name string, cpus int, memory, disk string) error` method that sets the stopped instance's resources via `limactl edit --set`.
- [ ] `provision.createVM` calls `Configure` on the clone **after** `Clone` and **before** `Start`.
- [ ] `overlay_test.go`, `lima/client_test.go`, and `provision/provision_test.go` are updated to cover the new behavior and ordered calls; `cd tui && go test ./...` passes.

## Technical Requirements
- Lima `limactl edit --set '<yq-expr>' <name>` applies config to a stopped instance; the change takes effect on next start. Increasing `disk` grows the qcow2 (Lima resizes on start; the Debian image's growpart extends the root FS). Decreasing `disk` is unsupported — the floor guarantees clones only ever grow.

## Input Dependencies
None.

## Output Artifacts
- `vm.BaseDiskFloor` constant (consumed by the reset form in task 4).
- `lima.Client.Configure` (consumed by the reset orchestration in task 3).
- A base image that is small + per-clone sizing in the standard create path.

## Implementation Notes
<details>
<summary>Detailed implementation guidance</summary>

**1. Floor constant — `tui/internal/vm/vm.go`**
- Add an exported constant near the top of the package:
  ```go
  // BaseDiskFloor is the virtual disk size the base image is built at. Clones are
  // grown from this floor to their requested size; qcow2 can grow but not shrink
  // live, so a small floor is what lets each VM pick (and effectively "shrink" to)
  // any size >= the floor without rebuilding the base.
  const BaseDiskFloor = "20GiB"
  ```

**2. Base overlay — `tui/internal/provision/overlay.go`**
- In `RenderBaseOverlay`, change the disk line from `cfg.Disk` to the floor:
  ```go
  fmt.Fprintf(&b, "disk: %s\n", quoteYAML(vm.BaseDiskFloor))
  ```
  Leave the `cpus`/`memory` lines as-is (they affect base-build speed; clones override them anyway).
- Update `tui/internal/provision/overlay_test.go`: the rendered overlay must now contain `disk: "20GiB"` regardless of `cfg.Disk`. Add/adjust the assertion accordingly.

**3. Lima client — `tui/internal/lima/client.go`**
- Add the method (model it on the existing `Clone`/`Create` wrappers, using the private `run`):
  ```go
  // Configure sets a STOPPED instance's cpus/memory/disk via `limactl edit --set`.
  // Applied on next start; disk may only grow (qcow2 cannot shrink live). memory
  // and disk are Lima size strings (e.g. "8GiB", "100GiB").
  func (c *Client) Configure(name string, cpus int, memory, disk string) error {
      expr := fmt.Sprintf(`.cpus=%d | .memory=%q | .disk=%q`, cpus, memory, disk)
      return c.run("edit", "--set", expr, name)
  }
  ```
  Quoting `memory`/`disk` with `%q` yields `.memory="8GiB"` etc. in the yq expression, matching `quoteYAML` output and avoiding unit ambiguity.
- Add a case to `TestMethodArgv` in `tui/internal/lima/client_test.go`:
  ```go
  {"configure", func(c *Client) { _ = c.Configure("vm1", 4, "8GiB", "100GiB") },
      []string{"edit", "--set", `.cpus=4 | .memory="8GiB" | .disk="100GiB"`, "vm1"}},
  ```

**4. Provisioner — `tui/internal/provision/provision.go`**
- In `createVM`, between `p.Lima.Clone(...)` and `p.Lima.Start(...)`, add:
  ```go
  if err := p.Lima.Configure(cfg.Name, cfg.CPUs, cfg.Memory, cfg.Disk); err != nil {
      return fmt.Errorf("configure clone %q: %w", cfg.Name, err)
  }
  ```
- Update `tui/internal/provision/provision_test.go` (`TestCreateVM_StoppedBase` and any other create/recreate sequence tests): insert the expected call
  `{"edit", "--set", \`.cpus=4 | .memory="8GiB" | .disk="100GiB"\`, "claude"}` between the `clone` and `start` argv entries. Use the exact memory/disk from `testConfig()` (Memory `"8GiB"`, Disk `"100GiB"`, CPUs 4 → confirm against `DefaultCreateConfig`).

**Verification note (cannot be unit-tested here):** the actual disk grow + FS expansion requires a real Lima/macOS host. The unit tests verify the *command construction* and *ordering*; leave a brief comment that the grow-on-start behavior is validated manually on a Lima host. Do not attempt to run a real `limactl` in tests.

**Out of scope for this task:** disk-minimum validation surfacing (handled in the form, task 4); preserve/staging (task 3).
</details>

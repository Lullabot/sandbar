---
id: 3
group: "core"
dependencies: [1, 2]
status: "pending"
created: 2026-06-26
skills:
  - golang
  - ansible
---
# Port the base-image / clone / finalize orchestration into the `provision` package

## Objective
Reimplement `new-vm.sh`'s provisioning orchestration in Go: render the Lima base
overlay YAML, build the phased Ansible `extra-vars`, locate the playbook, and drive
the base-build → clone → finalize → bounce sequence through the `lima.Client`,
streaming output. This is the self-contained "reimplement fully in Go" core.

## Skills Required
- **golang**: orchestration, YAML marshaling, streaming I/O, integration tests with fakes.
- **ansible**: understanding the `provision_phase` (base/finalize) contract and the in-guest `ansible-playbook` invocation being reproduced.

## Acceptance Criteria
- [ ] `RenderBaseOverlay(cfg, playbookDir) ([]byte, error)` produces overlay YAML inheriting `template:_images/debian-13`, with cpus/memory/disk, a read-only playbook mount at `/mnt/playbook`, and the `dependency` provision script installing `ansible`+`rsync`.
- [ ] `BuildExtraVars(cfg, phase, hostname) ([]byte, error)` emits the `all.yml` vars; identity (`user_git_user_name/email`) and `project_clone_url/token` appear ONLY for non-`base` phases; always sets `samba_enabled: false`; emits docker-proxy vars only when `DockerProxyHost` is set. Uses `gopkg.in/yaml.v3` for scalar quoting.
- [ ] `LocatePlaybook() (string, error)` returns the git toplevel containing `site.yml`, else errors (cache-clone fallback may be a documented TODO, not required for tests).
- [ ] `BuildBase(ctx, cfg, out)` and `CreateVM(ctx, cfg, out)` drive the correct ordered `lima.Client` calls; `Recreate` = delete(force) + CreateVM.
- [ ] The in-guest provisioning command reproduces the script: vars streamed over stdin into `/dev/shm`, `install -m 600`, EXIT-trap removal, `rsync` the playbook, `ansible-playbook -i localhost, --connection=local site.yml --extra-vars @vars`.
- [ ] Unit tests assert the rendered overlay + extra-vars for base vs finalize (incl. a token-bearing value to prove quoting & phase-gating); an integration test asserts `CreateVM` issues the expected ordered calls against a fake `lima.Client`/`Runner`.
- [ ] `cd tui && go build ./... && go vet ./... && go test ./...` pass.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- `gopkg.in/yaml.v3` (`cd tui && go get gopkg.in/yaml.v3`).
- Consumes `lima.Client` (task 02) and `vm.CreateConfig` (task 01).
- Reference: `scripts/new-vm.sh` lines ~307–509 (`build_allyml`, `render_base_overlay`, `run_provision`, `build_base`, `finalize_clone`, launch sequence).

## Input Dependencies
- Task 01: `vm.CreateConfig`.
- Task 02: `lima.Client` (and the `Runner`/`fakeRunner` pattern for the integration test).

## Output Artifacts
- `tui/internal/provision/overlay.go` — `RenderBaseOverlay`
- `tui/internal/provision/vars.go` — `BuildExtraVars`
- `tui/internal/provision/playbook.go` — `LocatePlaybook`
- `tui/internal/provision/provision.go` — `Provisioner` with `BuildBase`, `CreateVM`, `Recreate`, and the in-guest command builder
- `tui/internal/provision/*_test.go` — render/vars unit tests + ordered-call integration test

## Implementation Notes

<details>
<summary>Detailed implementation steps</summary>

1. **`vars.go` — `BuildExtraVars(cfg, phase, hostname)`**. Mirror `build_allyml`
   (script lines ~314-335). Build a `map[string]any` (or an ordered set of
   `yaml.MapItem`s for stable output) and marshal with `yaml.v3`:
   - Always: `user_name`, `base_hostname: hostname`, `base_domain`, `base_locale`,
     `provision_phase: phase`, `samba_enabled: false`.
   - If `cfg.DockerProxyHost != ""`: `devtools_docker_registry_proxy_enabled: true`
     and `devtools_docker_registry_proxy_host`.
   - If `phase != "base"`: `user_git_user_name`, `user_git_user_email`, and when
     `cfg.CloneURL != ""` add `project_clone_url` (+ `project_clone_token` when set).
   Using `yaml.v3` marshaling replaces the script's hand-rolled `yaml_str` quoting.

2. **`overlay.go` — `RenderBaseOverlay(cfg, playbookDir)`**. Reproduce
   `render_base_overlay` (script lines ~347-383). The dependency script body and the
   `base:`/mount layout must match. Simplest faithful approach is a text template
   with the fixed YAML header/footer and interpolated cpus/memory/disk/playbookDir
   (quote the path). Structure:
   ```
   base:
   - template:_images/debian-13
   cpus: <n>
   memory: "<mem>"
   disk: "<disk>"
   mounts:
   - location: "<playbookDir>"
     mountPoint: /mnt/playbook
     writable: false
   provision:
   - mode: dependency
     script: |
       #!/bin/bash
       set -eux -o pipefail
       if command -v ansible >/dev/null 2>&1 && command -v rsync >/dev/null 2>&1; then
         exit 0
       fi
       export DEBIAN_FRONTEND=noninteractive
       apt-get update
       apt-get install -y ansible rsync
   ```

3. **`playbook.go` — `LocatePlaybook()`**. Run `git rev-parse --show-toplevel`
   (via `os/exec`) from the binary's directory / cwd; if the result contains
   `site.yml`, return it. Otherwise return an error directing the user to run from a
   checkout (the script's cache-clone standalone mode may be left as a `// TODO`
   comment — not required by the acceptance tests, keep scope tight).

4. **`provision.go` — the orchestrator**. Define:
   ```go
   type Provisioner struct {
       Lima        *lima.Client
       PlaybookDir string
   }
   ```
   - `inGuestScript` constant = the exact `bash -c` body from `run_provision`
     (script lines ~416-430): create `/dev/shm/claude-vm-vars.yml` with
     `install -m 600 /dev/null`, EXIT-trap remove, `cat > vars`, `rsync -a --delete
     /mnt/playbook/ /root/playbook/`, `cd /root/playbook`, `ansible-playbook -i
     localhost, --connection=local site.yml --extra-vars @"$vars"`. Run it via
     `Lima.Shell(ctx, name, varsReader, out, "sudo", "bash", "-c", inGuestScript)`,
     where `varsReader` is a `bytes.Reader` over `BuildExtraVars(...)`. The vars go
     over **stdin**, never argv (secret hygiene).
   - `BuildBase(ctx, cfg, out)`: render overlay to a temp file (0600) → `Create` →
     run base-phase guest script → `Stop`. (Mirror `build_base`.)
   - `CreateVM(ctx, cfg, out)`: ensure base exists (`Status`) — build it if absent;
     ensure base `Stopped` (Stop if not) → `Clone(base, name)` → `Start(name)` → run
     finalize-phase guest script with `hostname = cfg.EffectiveHostname()` →
     `Stop`/`Start` bounce. (Mirror the launch sequence, lines ~474-509.)
   - `Recreate(ctx, cfg, out)`: `Delete(name, true)` then `CreateVM`.

5. **Tests**:
   - `vars_test.go`: assert base-phase output has NO `user_git_*`/`project_*` keys
     but HAS `samba_enabled: false`; assert finalize-phase output DOES include git
     identity and, with a `CloneURL`+`CloneToken` containing a `"` character, that
     the token is correctly quoted and present. (Custom logic + security gating.)
   - `overlay_test.go`: assert the rendered overlay contains the debian-13 base line,
     the read-only mount with the playbook path, and the dependency script.
   - `provision_test.go` (integration): inject a fake `lima.Client` backed by a
     `fakeRunner` that records argv; call `CreateVM` with a pre-existing stopped base
     and assert the ordered sequence: `clone` → `start` → `shell ... finalize` →
     `stop` → `start`. This is the key "mostly integration" test.
</details>

### Meaningful Test Strategy Guidelines
Your critical mantra for test generation is: "write a few tests, mostly integration".
- **DO** test `BuildExtraVars` phase-gating + quoting, the overlay render, and the ordered orchestration calls (the ported business logic).
- **DON'T** test `yaml.v3`, `os/exec`, or attempt a real provision. Use fakes; never invoke real `limactl`/`ansible`.

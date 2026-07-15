---
id: 2
group: "base-image-clis"
dependencies: [1]
status: "completed"
created: 2026-07-15
model: "sonnet"
effort: "high"
complexity_score: 6
complexity_notes: "Verification/quality gate: must exercise real provisioning and inspect in-VM behavior, not just re-read the role. Risk-floor task (proves the base image every VM clones from is correct), so it runs at sonnet + high."
skills:
  - ansible
  - integration-testing
---
# Verify glab and drupalorg in a provisioned base image

## Objective
Prove, with real evidence, that a base image provisioned with the updated
`dev-tools` role ships working `glab` and `drupalorg` CLIs and has not regressed
the existing `gh` install. This is the plan's Self-Validation gate.

## Skills Required
- `ansible` — running the provisioning playbook / `dev-tools` role.
- `integration-testing` — driving the resulting VM and capturing command output.

## Acceptance Criteria
- [ ] `ansible-playbook --syntax-check site.yml` exits 0 (fast pre-check).
- [ ] A base image (or a VM provisioned through the `base`/`full` phase, which
      runs `dev-tools`) is built with the updated role without error.
- [ ] Inside the provisioned VM, `glab --version` exits 0 and reports the pinned
      version.
- [ ] Inside the provisioned VM, `drupalorg --version` (or `drupalorg list`)
      exits 0 and prints version/commands, proving the PHAR and its PHP runtime
      both work.
- [ ] Inside the provisioned VM, `php --version` reports PHP ≥ 8.1, and
      `gh --version` still exits 0 (regression check).
- [ ] The captured terminal output of the above commands is recorded as evidence
      in the execution record.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Use the project's own provisioning entry points to build/refresh a base image
  or provision a VM (see `internal/provision/` and the `sand` CLI; the base build
  runs the `base` phase which includes `dev-tools`). Prefer the real path over an
  ad-hoc `ansible-playbook` invocation when practical.
- Open a shell in the resulting VM (e.g. `sand shell` / `limactl shell`) to run
  the verification commands.

## Input Dependencies
- Task 1 output: the updated `roles/dev-tools/tasks/main.yml` and
  `roles/dev-tools/defaults/main.yml`.

## Output Artifacts
- A verification record (command transcript) demonstrating `glab`, `drupalorg`,
  `php`, and `gh` all work in the provisioned VM. Consumed by the plan's
  POST_EXECUTION gate and execution summary.

## Implementation Notes

<details>
<summary>How to verify</summary>

### Verification gate — evidence before claims
Do not rely on Task 1's report or on re-reading the role. Actually provision and
run the commands, read the exit codes and output, then state the result.

### 1. Fast static pre-check
```bash
ansible-playbook --syntax-check site.yml
```

### 2. Provision a base image / VM through the real path
Build or refresh the base image so the `base` phase (which includes the
`dev-tools` role) runs against a clean Debian trixie guest. Use the project's
`sand` orchestrator / provisioning code paths in `internal/provision/`. If a full
base rebuild is impractical in the execution environment, provision a single VM
in the `full` phase (which also runs `dev-tools`) as an equivalent exercise of
the same tasks.

### 3. Exercise the CLIs inside the VM
Open a shell in the provisioned VM and run:
```bash
glab --version        # expect the pinned glab version, exit 0
drupalorg --version   # or: drupalorg list — expect output, exit 0
php --version         # expect PHP >= 8.1 (trixie: 8.4), exit 0
gh --version          # regression check — expect exit 0
```
Capture the full output and exit codes.

### 4. Fallback if live provisioning is impossible here
If the environment genuinely cannot provision a VM, record that explicitly and
provide the strongest available evidence instead:
- `ansible-playbook --syntax-check site.yml` output.
- Reachability of the pinned download URLs (HTTP `HEAD`/200) for the `glab`
  `.deb` and the `drupalorg.phar`.
Clearly flag that live in-VM verification is pending and why. Do not claim
success on the in-VM criteria without having run them.

### What counts as pass
All four commands exit 0 with sensible version output, and no existing task in
the role broke. Anything less is a gate failure — report it, do not paper over
it.
</details>

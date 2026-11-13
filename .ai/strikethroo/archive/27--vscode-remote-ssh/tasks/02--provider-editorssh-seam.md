---
id: 2
group: "provider"
dependencies: [1]
status: "completed"
created: 2026-11-13
skills:
  - go
complexity_score: 4
---
# `Provider.EditorSSH` for local Lima and Proxmox

## Objective
Add `EditorSSH(ctx context.Context, v vm.VM) (sshconfig.Entry, error)` to `provider.Provider`, implement it for local Lima and Proxmox, add a remote-Lima stub (replaced by task 3), and add `EditorSSHFunc` to `providerfake.Provider`.

## Skills Required
Go (provider package).

## Acceptance Criteria
- [x] Interface method documented (may do I/O; callers run it off the TUI update goroutine).
- [x] Local Lima returns `Entry{Alias: sshconfig.LimaAlias(v.Name), Include: filepath.Join(v.Dir, "ssh.config")}` and errors clearly when that file does not exist.
- [x] Proxmox resolves the IP via the existing bounded `guestIP` and returns alias `sshconfig.ProfileAlias(<profile name>, v.Name)`, HostName ip, User ciUser, IdentityFile identityPath, IdentitiesOnly, EphemeralHostKeys.
- [x] Remote Lima overrides with a stub error so the embedded local implementation (which would point at a REMOTE path locally) is never used.
- [x] `providerfake` gains `EditorSSHFunc` following its zero-value defaulting contract.
- [x] `go build ./... && go test ./internal/provider ./internal/providerfake` pass, with tests asserting the local and Proxmox entries (Proxmox via the existing fake PVE harness in `proxmox_test.go`).

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
The non-local providers need their profile name for the alias. Check how `provider.BuildFleet` / `TargetConfigFor` carry profile identity. If the name is not available, add `ProfileName` to `TargetConfig`, set in `TargetConfigFor`/`BuildFleet`, and store it on the Proxmox and remote providers. Per AGENTS.md, `connectionFieldsEqual` (internal/ui) must compare every field `TargetConfigFor` reads; a rename must therefore rebuild the binding — update `connectionFieldsEqual` (or its callers) accordingly and keep its tests passing.

## Input Dependencies
Task 1 (`sshconfig.Entry`, alias helpers).

## Output Artifacts
`Provider.EditorSSH` for local Lima and Proxmox; remote stub; fake updated; possibly `TargetConfig.ProfileName`.

## Implementation Notes
<details>

- Local: `internal/provider/local.go` near `ForwardArgv`; `os.Stat` the ssh.config (local filesystem).
- Remote stub in `remote.go`: `return sshconfig.Entry{}, errors.New("opening VS Code is not available for remote Lima profiles yet")`.
- Proxmox: mirror `attachArgvMode`, deriving the timeout ctx from the passed ctx: `context.WithTimeout(ctx, attachResolveTimeout)`.
- Test philosophy: write a few tests, mostly integration; do not test trivial wiring.

</details>

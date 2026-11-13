---
id: 3
group: "provider"
dependencies: [2]
status: "pending"
created: 2026-11-13
skills:
  - go
  - ssh
complexity_score: 5
---
# Remote-Lima `EditorSSH`: authorize workstation key + ProxyCommand entry

## Objective
Replace the remote-Lima stub: choose the workstation key pair, append its public key idempotently to the guest's `~/.ssh/authorized_keys`, and return a Host entry that tunnels through the remote host.

## Skills Required
Go, OpenSSH (ProxyCommand, `-W`, authorized_keys).

## Acceptance Criteria
- [ ] Key selection: profile `identity_path` + `.pub` when set (error if the `.pub` is missing); else the first of `~/.ssh/id_ed25519`, `id_ecdsa`, `id_rsa` with both halves present; else an error telling the user to set the profile's identity path.
- [ ] Public key validated as one line with a recognised type prefix (`ssh-`, `ecdsa-`, `sk-`); it travels to the guest on STDIN only; the literal guest script creates `~/.ssh` 0700 and the file 0600 and appends only when `grep -qxF` finds no identical line. Running twice leaves exactly one copy.
- [ ] Entry: alias `sshconfig.ProfileAlias(profile, v.Name)`, HostName `127.0.0.1`, User `GuestUser(v)`, IdentityFile the private key, IdentitiesOnly, EphemeralHostKeys, AuthorizedKey the `.pub` path, ProxyCommand = ssh to the remote host (profile user/host/port, `-i` when an identity is set) running `ssh -F <v.Dir>/ssh.config -W 127.0.0.1:22 lima-<name>` there, each remote token quoted with `sshx.Quote`, literal `%` escaped as `%%`.
- [ ] Unit tests (fake runner/transport, temp `HOME`) assert: no key material in the guest argv, the key on stdin, the exact ProxyCommand, key-selection fallbacks and errors.
- [ ] `go test ./internal/provider` passes.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
Use the provider's existing guest transport (`Shell` with stdin) — no new hand-rolled ssh connection. Reuse `sshx.Quote`.

## Input Dependencies
Task 2 (interface, stub, profile name).

## Output Artifacts
`remoteLimaProvider.EditorSSH` in `internal/provider/remote.go` plus tests.

## Implementation Notes
<details>

- Guest script (literal, run as `bash -c <script>`):
  ```
  set -e; umask 077; mkdir -p ~/.ssh; key=$(cat); touch ~/.ssh/authorized_keys; grep -qxF -- "$key" ~/.ssh/authorized_keys || printf '%s\n' "$key" >> ~/.ssh/authorized_keys; chmod 600 ~/.ssh/authorized_keys
  ```
  Run via `p.Shell(ctx, v.Name, strings.NewReader(pubLine), &buf, "bash", "-c", script)`; include `buf` in any error.
- Remote hop: build from the provider's stored `lima.SSHConfig`/`TargetConfig`: `ssh [-p PORT] [-i ID] [user@]host -- ssh -F <dir>/ssh.config -W 127.0.0.1:22 lima-<name>`. Quote local tokens containing spaces. `v.Dir` may be relative to the remote login home (see `defaultRemoteLimaHome` in internal/lima/sshhost.go); keep it as-is since the remote command runs in the login home.
- Leave the outer hop's ssh_config resolution alone (sshx.Config documents why the remote-Lima hop does not force IdentitiesOnly).
- Test philosophy: write a few tests, mostly integration; cover the security-relevant behaviour and fallbacks.

</details>

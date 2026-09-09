---
id: 4
group: "provider-seam"
dependencies: []
status: "completed"
created: 2026-08-04
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Adds a method to the central Provider interface implemented by three backends plus fakes; each backend's forwarding semantics differ and must be exactly right."
skills:
  - go
  - networking
---
# Provider.ForwardArgv seam for all three backends

## Objective

Add a forwarding seam to the `Provider` interface that makes a guest loopback
port reachable on the workstation's loopback, implemented for local Lima
(nothing to do), remote Lima over SSH, and Proxmox.

## Skills Required

- **go** — extending a central interface and its three implementations plus
  test fakes.
- **networking** — SSH local port forwarding semantics and Lima's automatic
  localhost forwarding.

## Acceptance Criteria

- [ ] `Provider` gains `ForwardArgv(v vm.VM, hostPort, guestPort int) []string`
      with a doc comment stating the nil-means-already-reachable contract.
- [ ] `go build ./...` succeeds — every implementation and every test fake
      satisfies the extended interface.
- [ ] The **local Lima** provider returns `nil`; a test asserts this explicitly
      and its name/comment records *why* (Lima auto-forwards guest 127.0.0.1 to
      host 127.0.0.1 on the same port).
- [ ] The **remote Lima** provider returns an `ssh` argv containing `-N` and
      `-L <hostPort>:127.0.0.1:<guestPort>` targeting the configured SSH host; a
      table test asserts the exact argv.
- [ ] The **Proxmox** provider returns an `ssh` argv containing `-N` and
      `-L <hostPort>:127.0.0.1:<guestPort>` targeting the guest address, reusing
      that provider's existing ssh identity/options; a table test asserts the
      exact argv.
- [ ] `go test ./internal/provider/... -race -v -run Forward` passes and its
      output shows the asserted argv for each backend.
- [ ] `go test ./... -race` passes with no other package broken by the interface
      change.
- [ ] No test in this task spawns a real `ssh`, opens a socket, or needs a VM.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Follow the established argv-returning idiom of `AttachArgv` and `RunArgv`:
  the method is **pure** — it builds argv and performs no I/O — so behavior is
  assertable exactly, with no real ssh binary, network or VM.
- The caller execs the returned argv as a long-running child and kills it when
  done; `-N` (no remote command) is therefore required.
- Both ends use the same port number in practice (see task 5), but the method
  must still take `hostPort` and `guestPort` separately rather than assuming
  they match.

## Input Dependencies

None. This task is independent of the web app and can run in parallel with
task 1.

## Output Artifacts

- The `ForwardArgv` method on `provider.Provider` and its three implementations
- Updated test fakes across the repository
- Table tests asserting each backend's exact argv

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Why local Lima returns nil — state this in the code comment.** Lima
automatically forwards guest localhost ports to the host's localhost on the same
port number. This was verified against Lima's own source:
`FillPortForwardDefaults` in `pkg/limayaml/defaults.go` defaults a rule's
`GuestIP` to IPv4 loopback and its `HostIP` to IPv4 loopback, and Lima's
port-forwarding documentation states it "supports automatic port-forwarding of
localhost ports from guest to host". So for a local Lima instance there is
nothing to start — the port is already on the workstation's loopback. Returning
nil (rather than inventing a no-op process) keeps that fact visible at the seam.

**Remote Lima.** Lima on the *remote* host performs that same auto-forward, but
onto the **remote host's** loopback — not the workstation's. One hop is needed:

```
ssh <remote-host-args…> -N -L <hostPort>:127.0.0.1:<guestPort>
```

Build the host arguments the same way `remote.go` already builds them for
`AttachArgv` (user, host, port, identity, and the existing options) rather than
hand-rolling a second connection identity. Read `internal/lima/sshhost.go` and
`internal/provider/remote.go` first, and reuse whatever helper produces the base
ssh argv there.

**Proxmox.** No Lima is involved; the guest's own sshd is the endpoint, so
forward directly to the guest address that provider already resolves for
`AttachArgv`:

```
ssh <guest-ssh-args…> -N -L <hostPort>:127.0.0.1:<guestPort>
```

Read `internal/provider/proxmox.go`'s `sshHost` and `guestArgv` and reuse the
same identity, port and host-key posture. Note that provider resolves the guest
address lazily and bounds it with `attachResolveTimeout` — follow whatever
`AttachArgv` does there, including its timeout behavior.

**Interface change ripple.** `Provider` is implemented by the local, remote and
Proxmox providers and by fakes in tests (`internal/providerfake` and
package-local fakes). Add the method everywhere; `go build ./...` is the check
that nothing was missed. Where a fake needs to record the call, mirror how
existing fakes record `AttachArgv`/`RunArgv`.

**Bind the forward to loopback.** `ssh -L <port>:…` binds the local end to
localhost by default (unless `GatewayPorts`/`-g` is in play). Do not pass
anything that would bind it to all interfaces — the whole point is that the
reviewed code stays on the workstation's loopback.

**Testing.** Follow the repository's existing provider test style: table tests
asserting the exact argv slice. Do not test that ssh works — test that the argv
is right. One test per backend plus a shared table is sufficient; this is
exactly the "test your code, not the library" case.

</details>

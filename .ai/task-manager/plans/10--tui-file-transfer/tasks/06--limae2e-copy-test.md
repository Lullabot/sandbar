---
id: 6
group: "e2e-test"
dependencies: [1]
status: "pending"
created: 2026-07-03
skills:
  - go
---
# Gated real-VM round-trip copy test (`//go:build limae2e`)

## Objective
Add a `//go:build limae2e` test that boots a real Lima VM and exercises an actual
`limactl copy` round-trip through `lima.Client.Copy` — upload a host file into the
guest, download it back, and assert the content survives. It is auto-excluded from
`go test ./...` (build tag + `LIMA_E2E` env guard), mirroring the existing
`provision/lima_e2e_test.go`. Real Lima VMs do boot in this dev environment, so
this test is runnable here.

## Skills Required
- **go**: build-tagged tests, `lima.Client` against a real `execRunner`, filesystem assertions.

## Acceptance Criteria
- [ ] New file `tui/internal/lima/copy_e2e_test.go` begins with `//go:build limae2e` and skips unless `os.Getenv("LIMA_E2E") != ""`, matching `provision/lima_e2e_test.go`'s guard and header-comment style.
- [ ] The test builds a `lima.Client` from `lima.NewExecRunner()`, creates a minimal base VM (Debian 13 template at the disk floor, as `provision/lima_e2e_test.go` does), and always tears it down via `t.Cleanup`.
- [ ] It writes a host file with known contents, `Copy`s it up into a guest directory (`GuestPath(vm, <guest dir>)`, non-recursive), then `Copy`s it back down to a fresh host temp dir, and asserts the round-tripped file's bytes equal the original.
- [ ] It also exercises a **recursive** directory copy (a small host dir with one file) up into the guest, asserting the file lands at `<guest dir>/<srcdir>/<file>` (destination-is-a-directory placement).
- [ ] The test is excluded from the normal suite: `cd tui && go test ./...` does NOT build or run it; `cd tui && go build ./... && go vet ./...` still pass. (Running it is `go test -tags limae2e -timeout 30m -run TestE2E ./internal/lima/` with `LIMA_E2E=1`.)

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Reuse the patterns in `tui/internal/provision/lima_e2e_test.go`: the `e2eOverlay`
  (Debian 13 at `vm.BaseDiskFloor`), the `guestOut` helper for reading guest state
  over `lima.Client.Shell`, the `LIMA_E2E` skip, and unconditional `Delete`
  cleanup. Booting the Debian image downloads it once (slow first run) — hence the
  long timeout.
- Use `t.TempDir()` for host source/destination; make a guest scratch directory
  with `cli.Shell(ctx, vm, nil, buf, "mkdir", "-p", "<dir>")` before copying into it.

## Input Dependencies
- Task 1: `lima.Client.Copy` and `lima.GuestPath`.

## Output Artifacts
- `tui/internal/lima/copy_e2e_test.go` — the gated round-trip copy test.

## Implementation Notes

<details>
<summary>Detailed implementation steps</summary>

1. Header + guard (copy the shape from `provision/lima_e2e_test.go`):
   ```go
   //go:build limae2e

   package lima

   import (
       "bytes"
       "context"
       "os"
       "path/filepath"
       "testing"

       "github.com/deviantintegral/claude-code-ansible/tui/internal/vm"
   )
   ```
   Because the file is in `package lima`, it can use the unexported `execRunner`
   if helpful, but prefer the exported surface (`New`, `NewExecRunner`, `Copy`,
   `Shell`, `Create`, `Delete`, `GuestPath`).

2. Skeleton:
   ```go
   func TestE2ECopyRoundTrip(t *testing.T) {
       if os.Getenv("LIMA_E2E") == "" {
           t.Skip("set LIMA_E2E=1 (and -tags limae2e) to run the real-Lima copy e2e test")
       }
       cli := New(NewExecRunner())
       const name = "claude-copy-e2e"
       _ = cli.Delete(name, true)
       t.Cleanup(func() { _ = cli.Delete(name, true) })

       overlay := filepath.Join(t.TempDir(), "base.yaml")
       os.WriteFile(overlay, []byte(
           "base:\n- template:_images/debian-13\ncpus: 2\nmemory: \"2GiB\"\ndisk: \""+vm.BaseDiskFloor+"\"\n"), 0o600)
       if err := cli.Create(name, overlay); err != nil { t.Fatalf("create: %v", err) }

       ctx := context.Background()
       // guest scratch dir
       var b bytes.Buffer
       if err := cli.Shell(ctx, name, nil, &b, "mkdir", "-p", "/tmp/e2e-in"); err != nil {
           t.Fatalf("mkdir guest: %v\n%s", err, b.String())
       }

       // --- single file round-trip
       hostSrc := filepath.Join(t.TempDir(), "hello.txt")
       os.WriteFile(hostSrc, []byte("round-trip payload"), 0o644)
       if err := cli.Copy(ctx, io.Discard, false, hostSrc, GuestPath(name, "/tmp/e2e-in")); err != nil {
           t.Fatalf("upload: %v", err)
       }
       hostDstDir := t.TempDir()
       if err := cli.Copy(ctx, io.Discard, false, GuestPath(name, "/tmp/e2e-in/hello.txt"), hostDstDir); err != nil {
           t.Fatalf("download: %v", err)
       }
       got, _ := os.ReadFile(filepath.Join(hostDstDir, "hello.txt"))
       if string(got) != "round-trip payload" { t.Fatalf("round-trip content = %q", got) }
   }
   ```
   (Add `io` to the imports for `io.Discard`, or stream to a `bytes.Buffer`.)

3. Recursive case: make a host dir `srcdir/` with one file, `Copy(..., true, srcdir, GuestPath(name, "/tmp/e2e-in"))`, then assert via `guestOut`-style `cli.Shell(ctx, name, nil, buf, "cat", "/tmp/e2e-in/srcdir/<file>")` that the content is present — proving the destination-is-a-directory placement (`srcdir` nested under the dest).

4. Keep the test focused on the copy mechanics; it does not need the Ansible
   provision or the TUI. Mirror the existing e2e test's cleanup discipline so a
   failed run never leaks a VM.
</details>

### Meaningful Test Strategy Guidelines
Your critical mantra for test generation is: "write a few tests, mostly integration".
- **DO** exercise the real Lima transport end-to-end (upload → download content equality; recursive placement) — this is the one thing the fake `Runner` cannot prove.
- **DON'T** duplicate the argv unit tests here, and keep it behind the build tag + env guard so it never runs in the default suite.

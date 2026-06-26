---
id: 1
group: "foundation"
dependencies: []
status: "pending"
created: 2026-06-26
skills:
  - golang
---
# Scaffold the `tui/` Go module and VM domain model

## Objective
Create the `tui/` Go module (own `go.mod`), the package directory layout, and the
shared `vm` domain model (`VM`, `CreateConfig`, defaults, `Validate()`) that the
`lima`, `provision`, and `ui` packages will all consume.

## Skills Required
- **golang**: Go module setup, package layout, struct/validation design, table tests.

## Acceptance Criteria
- [ ] `tui/go.mod` exists with module path `github.com/deviantintegral/claude-code-ansible/tui` and `go 1.24`.
- [ ] Directory layout exists: `tui/cmd/claude-vm/`, `tui/internal/lima/`, `tui/internal/provision/`, `tui/internal/vm/`, `tui/internal/ui/`.
- [ ] `tui/internal/vm/vm.go` defines `VM` and `CreateConfig` structs and a `DefaultCreateConfig()` constructor matching the script's defaults.
- [ ] `CreateConfig.Validate()` enforces: git name & email required, `Name != BaseName`, CPUs a positive integer.
- [ ] `tui/internal/vm/vm_test.go` covers defaults and the three validation rules.
- [ ] `cd tui && go build ./... && go vet ./... && go test ./...` all pass.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Go 1.24.x (`go1.24.4` is installed).
- No third-party deps required for this task (model + tests are stdlib only). Bubble Tea/yaml deps are added in later tasks when first used.

## Input Dependencies
None. This is the foundation task.

## Output Artifacts
- `tui/go.mod`
- `tui/internal/vm/vm.go` — `VM`, `CreateConfig`, `DefaultCreateConfig`, `Validate`
- `tui/internal/vm/vm_test.go`
- Empty package directories (with a `doc.go` placeholder if needed so `go build ./...` sees them) for `lima`, `provision`, `ui`, and `cmd/claude-vm`.

## Implementation Notes

<details>
<summary>Detailed implementation steps</summary>

1. From the repo root create the module:
   ```bash
   mkdir -p tui/cmd/claude-vm tui/internal/lima tui/internal/provision tui/internal/vm tui/internal/ui
   cd tui && go mod init github.com/deviantintegral/claude-code-ansible/tui
   ```
   Ensure `go.mod` says `go 1.24`.

2. `tui/internal/vm/vm.go` — define the domain types. Reference `scripts/new-vm.sh`
   for defaults (lines ~119-127 for the flag vars, ~260-305 for defaults/validation):

   ```go
   package vm

   import (
       "fmt"
       "strconv"
   )

   // VM is one Lima instance as reported by `limactl list`.
   type VM struct {
       Name   string
       Status string // Running | Stopped | ...
       CPUs   int
       Memory string
       Disk   string
       Dir    string
       IP     string
       Arch   string
   }

   // CreateConfig mirrors the answers new-vm.sh gathers.
   type CreateConfig struct {
       Name            string
       BaseName        string
       Hostname        string
       User            string
       GitName         string
       GitEmail        string
       CPUs            int
       Memory          string
       Disk            string
       Locale          string
       Domain          string
       DockerProxyHost string
       CloneURL        string
       CloneToken      string
   }

   // DefaultCreateConfig returns the script's defaults (cpus left to caller/host).
   func DefaultCreateConfig() CreateConfig {
       return CreateConfig{
           Name:     "claude",
           BaseName: "claude-base",
           Memory:   "8GiB",
           Disk:     "100GiB",
           Domain:   "lan",
           Locale:   "en_US.UTF-8",
           CPUs:     2,
       }
   }

   func (c CreateConfig) Validate() error {
       if c.GitName == "" {
           return fmt.Errorf("git user.name is required")
       }
       if c.GitEmail == "" {
           return fmt.Errorf("git user.email is required")
       }
       if c.Name == c.BaseName {
           return fmt.Errorf("instance name %q must differ from base image name %q", c.Name, c.BaseName)
       }
       if c.CPUs < 1 {
           return fmt.Errorf("cpus must be a positive integer (got %d)", c.CPUs)
       }
       return nil
   }

   // Hostname defaults to Name when unset; helper used by the form/provisioner.
   func (c CreateConfig) EffectiveHostname() string {
       if c.Hostname != "" {
           return c.Hostname
       }
       return c.Name
   }

   // ParseCPUs validates the script's "cpus must be a positive integer" rule from a string field.
   func ParseCPUs(s string) (int, error) {
       n, err := strconv.Atoi(s)
       if err != nil || n < 1 {
           return 0, fmt.Errorf("cpus must be a positive integer (got %q)", s)
       }
       return n, nil
   }
   ```

3. `tui/internal/vm/vm_test.go` — table test the three `Validate()` failure cases,
   one success case, and `DefaultCreateConfig()` values. Keep it small (a few tests,
   integration-leaning) per the test strategy.

4. To keep `go build ./...` clean across the empty packages, add a minimal
   `doc.go` (`// Package lima ...` etc.) in `lima`, `provision`, `ui`, and a stub
   `main.go` in `cmd/claude-vm` that just prints a placeholder (it is fully
   implemented in task 04). Example stub:
   ```go
   package main

   func main() {}
   ```

5. Run `go build ./... && go vet ./... && go test ./...` from `tui/` and confirm green.
</details>

### Meaningful Test Strategy Guidelines
Your critical mantra for test generation is: "write a few tests, mostly integration".
- **DO** test `Validate()` (custom business rules) and `DefaultCreateConfig()`.
- **DON'T** test stdlib or trivial getters. Keep the suite minimal.

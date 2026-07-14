---
id: 5
group: "tier-1-toolset"
dependencies: [3, 4]
status: "pending"
created: 2026-07-13
model: "sonnet"
effort: "high"
skills:
  - go
  - ansible
complexity_score: 7
complexity_notes: "Spans the Go CLI config surface, the extra-vars bridge, the version stamp, and the Ansible package lists. Backwards compatibility is a hard requirement."
---
# Make DDEV, Go, and Java a configurable base-image tool-set

## Objective

Let a user who does not need DDEV, Go, or Java stop paying for them (~500–700 MB installed between Go and Java alone) — **without changing what an existing user's VM contains by default**. The tool-set configures the shared **base image**, not the individual clone.

## Skills Required

- **go** — `vm.CreateConfig`, `cmd/sand/create.go` flags, `provision.BuildExtraVars`, wiring the selection into the version stamp.
- **ansible** — making `golang`, `default-jdk-headless`, and ddev conditional on the selection.

## Acceptance Criteria

- [ ] `vm.CreateConfig` gains three booleans (e.g. `WithDDEV`, `WithGo`, `WithJava`), **all defaulting to `true`** in `vm.DefaultCreateConfig()`.
- [ ] `sand create` gains `--with-ddev`, `--with-go`, `--with-java` flags (defaulting true, so they are opt-**out**).
- [ ] **Backwards compatibility**: `sand create` with no tool flags produces a VM containing DDEV, Go, and Java exactly as today. Verify by diffing the resolved package list against the current `base_packages`.
- [ ] `provision.BuildExtraVars` emits the three booleans as real YAML bools (follow the existing `samba_enabled` / `devtools_docker_registry_proxy_enabled` precedent).
- [ ] `golang` and `default-jdk-headless` are removed from the unconditional `base_packages` in `roles/base/defaults/main.yml` and are added to the install list only when their var is true.
- [ ] ddev's repo registration and package install are gated on the DDEV var.
- [ ] **`tmux` and `ncurses-term` remain in `base_packages` under every selection** — main's persistent-shell feature (`sand shell`, the TUI `S` verb) hard-fails without a guest tmux. Same for the base-phase-only `~/.tmux.conf` template and the `loginctl enable-linger` task.
- [ ] The tool-set selection feeds the version stamp from task 4, so changing the selection marks the base stale. Verify with a test: two configs differing only in `WithJava` produce different stamps.
- [ ] `sand` detects a **shrinking** selection (the new selection is a strict subset of the stamped one) and prints a clear advisory that the de-selected tools remain installed until the base is rebuilt. It must NOT silently leave stale software installed.
- [ ] `go vet ./...`, `go test ./...`, and `ansible-playbook --syntax-check site.yml` are green.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- `internal/vm/vm.go` `CreateConfig` (~:44-59) and `DefaultCreateConfig()` (~:62-72). The registry (`internal/vm/registry.go`) snapshots `CreateConfig` and Reset replays it, so new booleans are persisted and replayed **for free**.
- `internal/provision/vars.go` `BuildExtraVars` builds an ordered `yaml.Node` mapping; booleans are encoded via `Node.Encode(bool)`.
- `roles/base/defaults/main.yml`: `base_packages` (36 entries) currently includes `default-jdk-headless` (~:12) and `golang` (~:15). `tmux` is at ~:30 and `ncurses-term` at ~:22 — **do not remove these**.
- ddev lives in `roles/dev-tools` (repo at ~:68-74, install in the consolidated list after task 3).
- Ansible **cannot converge a removal**: it will not uninstall a package whose task no longer applies. This is why the shrink case needs an advisory, not a silent no-op.

## Input Dependencies

- Task 3: the consolidated single install task builds its package list from variables — the tool-set adds/removes entries there.
- Task 4: the content-hash version function accepts a tool-set selection string; this task supplies the real value.

## Output Artifacts

- Tool-set booleans on `CreateConfig`, CLI flags, extra-vars, and role gating.
- A canonical tool-set string feeding the version stamp.
- The shrink-detection advisory (task 6 surfaces it in the TUI).

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**1. Config + flags.**

```go
// CreateConfig
WithDDEV bool
WithGo   bool
WithJava bool

// DefaultCreateConfig — all three default ON, so today's VM contents are unchanged.
WithDDEV: true, WithGo: true, WithJava: true,
```

In `cmd/sand/create.go`, register them as opt-out flags:

```go
fs.BoolVar(&cfg.WithDDEV, "with-ddev", true, "Install DDEV in the base image")
fs.BoolVar(&cfg.WithGo,   "with-go",   true, "Install the Go toolchain in the base image")
fs.BoolVar(&cfg.WithJava, "with-java", true, "Install a headless JDK in the base image")
```

**2. Canonical tool-set string** (feeds the stamp — must be stable and order-independent):

```go
// ToolsetKey renders the selection into a stable string for the base version
// stamp. Order is fixed, so the same selection always hashes the same way.
func (c CreateConfig) ToolsetKey() string {
    var on []string
    if c.WithDDEV { on = append(on, "ddev") }
    if c.WithGo   { on = append(on, "go") }
    if c.WithJava { on = append(on, "java") }
    if len(on) == 0 { return "none" }
    return strings.Join(on, "+")   // e.g. "ddev+go+java"
}
```

Pass this into task 4's `PlaybookVersion(fsys, toolset)`.

**3. Extra-vars** (`internal/provision/vars.go`) — emit as real bools, following the existing precedent:

```go
addBool(&m, "toolset_ddev", cfg.WithDDEV)
addBool(&m, "toolset_go",   cfg.WithGo)
addBool(&m, "toolset_java", cfg.WithJava)
```

Emit them for the **base** phase (that is where the tools are installed).

**4. Ansible.** Remove `golang` and `default-jdk-headless` from `base_packages`. Add a computed optional list in `roles/base/defaults/main.yml`:

```yaml
toolset_ddev: true
toolset_go: true
toolset_java: true

toolset_packages: >-
  {{ ([ 'golang' ] if toolset_go | bool else [])
   + ([ 'default-jdk-headless' ] if toolset_java | bool else []) }}
```

Feed `toolset_packages` into task 3's single install list. Gate ddev's repo registration and package on `when: toolset_ddev | bool`.

**5. Shrink detection.** Parse the stamped tool-set out of the existing stamp and compare to the requested one:

```go
// Ansible converges additions but cannot converge a removal: it will not
// uninstall a package whose task no longer applies. So a shrinking selection
// leaves residue on the base until it is rebuilt. Say so — never leave stale
// software installed silently.
func shrunk(stamped, want map[string]bool) []string {
    var lost []string
    for tool, on := range stamped {
        if on && !want[tool] {
            lost = append(lost, tool)
        }
    }
    return lost
}
```

When `lost` is non-empty, emit a `step()` advisory (this one IS a phase banner — it is user-facing news, not a timing line):

```
==> Note: <tools> were de-selected but remain installed on the base image.
    Ansible cannot uninstall them. Rebuild the base to remove them
    (sand create --rebuild, or the "Rebuild base image" toggle in the form).
```

**Tests.** Assert: default config → all three true and the package list matches today's; `ToolsetKey()` is stable and order-independent; two configs differing only in `WithJava` produce different stamps; `shrunk()` returns the de-selected tools and nothing else.

</details>

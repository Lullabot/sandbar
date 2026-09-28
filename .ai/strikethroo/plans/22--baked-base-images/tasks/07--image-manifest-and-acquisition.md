---
id: 7
group: "host-acquisition"
dependencies: [5]
status: "pending"
created: 2026-09-12
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "high"
complexity_score: 7
complexity_notes: "Supply-chain-sensitive: this code decides whether a downloaded disk image is trusted and booted. Digest verification and partial-download handling are security-relevant, so the rubric's risk floor applies."
skills:
  - go
  - supply-chain-verification
---
# Pin the image manifest in Go and add a verified, cached acquisition helper

## Objective

Replace the three hand-maintained image constants with a generated pinned manifest covering both architectures. Add verified cache acquisition for Lima hosts, while Proxmox continues passing the manifest URL and digest to PVE's existing server-side download.

## Skills Required

`go` for the manifest type, generator and acquisition helper; `supply-chain-verification` for getting digest checking and partial-download handling right.

## Acceptance Criteria

- [ ] A generated Go source file carries the pinned manifest: per architecture, the asset URL, filename and SHA-256, plus the image version string.
- [ ] A `go generate` (or `make`) target regenerates that file from a published release's `manifest.json`, so bumping the image is one command and one reviewable diff.
- [ ] The three existing constants `baseImageURL`, `baseImageFile` and `defaultBaseImageSHA256` (`internal/provider/proxmoxprovision.go:61-77`) are removed in favour of the manifest.
- [ ] A Lima-host acquisition helper resolves the target architecture and returns a verified path on the host where `limactl` runs (workstation for local Lima, remote host for remote Lima), downloading only on a cache miss or failed verification.
- [ ] Proxmox callers consume URL/filename/SHA directly from the same manifest and retain PVE's server-side download and verification; they do not download the image through the workstation cache.
- [ ] A **digest mismatch is a hard failure** — never a fallback to using the file anyway, and never a silent re-download loop.
- [ ] A partially downloaded file is never mistaken for a complete one: download to a temporary path and rename into the cache only after the digest verifies.
- [ ] Download progress is written to the `io.Writer` the caller supplies, so it flows into the existing TUI job stream.
- [ ] Verification: `go build ./...` and `go vet ./...` pass. Paste the output.
- [ ] Verification: `grep -rn "defaultBaseImageSHA256\|baseImageURL\|baseImageFile" internal/` returns no hits outside the generated manifest. Paste it.
- [ ] Verification: run the regeneration target against the real published release and confirm the generated file's digests match `gh release view <tag>` checksums. Paste both.
- [ ] Verification: a manual smoke test acquiring the image twice shows a download on the first call and a cache hit (no network) on the second. Paste the timings or log lines proving it.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Architecture resolution must map Go's `runtime.GOARCH` (`amd64`, `arm64`) to the manifest keys, and must return a clear error for an unsupported architecture rather than defaulting to one.
- The cache location belongs with sand's other state; document it, because task 14 must add it to `docs/reference/files-and-state.md`.
- Progress reporting should match the shape the TUI already consumes (`internal/ui/progress.go:39`, `internal/ui/jobstream.go`) — a periodic human-readable line is sufficient; do not invent a new protocol.
- Verification is SHA-256, matching what the workflow publishes and what PVE's server-side download already checks.
- Keep manifest lookup provider-neutral. Keep filesystem acquisition on the `lima.HostFiles`/`Host` seam so a remote-Lima cache path is remote, not a nonexistent workstation path.

## Input Dependencies

- Task 05's published `manifest.json` and its agreed field names.

## Output Artifacts

- The generated manifest source and its regeneration target.
- The Lima-host acquisition helper — consumed by task 08 and task 12.
- Provider-neutral manifest lookup — consumed by both tasks 08 and 09.

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

**Why this replaces constants.** Today `internal/provider/proxmoxprovision.go:61-77` holds a URL, a filename and a SHA-256 as three separate constants, with a comment warning they must be bumped together. With two architectures and two providers that becomes four-plus values with the same hazard. One generated file with one regeneration command removes the class of bug entirely.

**Suggested shape:**

```go
type ImageEntry struct {
    URL      string
    Filename string
    SHA256   string
    Size     int64
}

type Manifest struct {
    Version string
    Images  map[string]ImageEntry // keyed by GOARCH: "amd64", "arm64"
}
```

with `var PinnedManifest = Manifest{...}` in a generated file (header comment: `// Code generated by ...; DO NOT EDIT.`).

**The regeneration target** fetches a release's `manifest.json` (via `gh release download` or a plain HTTP GET of the asset URL) and renders the Go file. Keep it dumb and deterministic — sorted keys, stable formatting — so the diff when bumping an image is exactly the changed digests and URLs and nothing else.

**Acquisition helper contract.** Something like:

```go
func Acquire(ctx context.Context, host lima.Host, m Manifest, goarch string, out io.Writer) (string, error)
```

returning a path meaningful on that Lima host. The order of operations matters:

1. Resolve the entry for `goarch`; unknown arch → error naming the supported set.
2. Compute the cache path from the filename (and ideally the version, so two pinned versions can coexist).
3. If the file exists, **hash it and compare**. A cached file that fails verification is deleted and re-downloaded — do not trust the cache blindly, because a truncated earlier download or a corrupted disk would otherwise wedge the user permanently.
4. On miss, download to `<cachepath>.tmp-<random>`, hashing as you stream (`io.TeeReader` into a `sha256.New()`), reporting progress periodically.
5. Compare the computed digest to the expected one. Mismatch → delete the temp file and return an error that names both digests. **Never** use the file anyway.
6. `os.Rename` the temp file into place. Rename is atomic within a filesystem, which is what makes step 3's "if it exists it was complete" assumption sound.

**Progress reporting.** Write a line every few seconds or every N MiB — something like `==> downloading base image: 412 MiB / 1.5 GiB (27%)`. Note that sand's own phase banners already use the `==> ` prefix and the TUI's parser recognises it (`internal/ui/ansible.go:35-52`), so using that prefix makes the download appear naturally alongside the existing phases. Do not write a progress line per chunk; it will flood the ring buffer.

**Concurrency.** Two `sand` processes could acquire simultaneously. The temp-file-plus-rename pattern makes that safe (both download, one rename wins, both end up with a valid file). Do not add a lock unless a real problem appears — the existing base lock already serializes the surrounding operations.

**Testing hooks.** Task 12 will unit-test this. Keep the HTTP client injectable (or accept a base URL) so tests can serve a small fixture over `httptest` and cover: happy path, digest mismatch, truncated response, cache hit, and corrupt cache. Do not write tests here that merely re-test Go's HTTP client.

</details>

---
id: 2
group: "listing"
dependencies: []
status: "pending"
created: 2026-07-03
skills:
  - go
---
# Build the `DirLister` seam: interface + host and guest listers

## Objective
Create the data-source abstraction that lets one browser render either the host
or a guest without knowing which. Add a new `tui/internal/browse` package with a
`DirLister` interface returning `DirEntry` values (Name/IsDir/Size), a
`localLister` over `os.ReadDir` (host), and a `guestLister` that runs a single
`find … -printf` over `lima.Client.Shell` (guest) and parses its tab-separated
output. Unit-test the host lister against a temp directory and the guest lister
against a fake `Runner` returning canned `find` output.

## Skills Required
- **go**: interfaces, `os.ReadDir`, `bufio`/`strings` parsing, `context`, table tests.

## Acceptance Criteria
- [ ] New package `tui/internal/browse` defines `type DirEntry struct { Name string; IsDir bool; Size int64 }` and `type DirLister interface { List(ctx context.Context, path string) ([]DirEntry, error) }`.
- [ ] `localLister` (host) implements `List` via `os.ReadDir(path)`, mapping each entry to a `DirEntry` (`IsDir` from `entry.IsDir()`, `Size` from `entry.Info().Size()`, tolerating a per-entry `Info()` error by treating size as 0). Entries are returned directory-first is NOT required, but hidden files ARE included.
- [ ] `guestLister{cli *lima.Client, vm string}` implements `List` by running `cli.Shell(ctx, vm, nil, &buf, "find", path, "-mindepth", "1", "-maxdepth", "1", "-printf", "%y\t%s\t%f\n")` and parsing each line into a `DirEntry` (`%y`=='d' → `IsDir=true`; `%s` → `Size`; `%f` → `Name`). A non-zero exit / empty-but-error surfaces as a returned error (no hang).
- [ ] Malformed lines (wrong field count) are skipped, not fatal; a genuine shell error is returned wrapped with the path.
- [ ] Unit tests: (a) `localLister` against a `t.TempDir()` seeded with a file and a subdirectory asserts the returned entries' Name/IsDir/Size; (b) `guestLister` against a `fakeRunner` (define a small local fake, or reuse the lima package's pattern) whose `Stream` writes canned `find` output (`d\t4096\tsrc\nf\t12\tfile.txt\n`) asserts it parses two entries with correct IsDir/Size/Name.
- [ ] `cd tui && gofmt -l . && go build ./... && go vet ./... && go test ./...` all pass.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- The guest command is a single SSH round-trip; it MUST run as a `tea.Cmd`/off the render goroutine when used by the browser (that wiring is the browser task's concern — this task just provides the blocking `List`).
- `find -printf` is GNU findutils, present on the apt-based Debian/Ubuntu guests. Type letter `%y` is `d` (dir), `f` (file), or `l` (symlink); treat only `d` as a directory for v1.
- Parsing must be locale-proof: split each line on `\t` into exactly 3 fields (type, size, name); `strconv.ParseInt` the size (default 0 on parse failure). Names never contain `\t`/`\n` in the common case; exotic filenames with embedded tabs/newlines are an accepted, documented limitation (matches `limactl copy`'s own behaviour) — do not build a binary-safe protocol.

## Input Dependencies
None for the interface/local lister. The `guestLister` uses the **existing**
`lima.Client.Shell` (already present) — it does NOT depend on the new `Copy`
method from task 1.

## Output Artifacts
- `tui/internal/browse/lister.go` — `DirEntry`, `DirLister`, `localLister`, `guestLister`.
- `tui/internal/browse/lister_test.go` — temp-dir test for local, fake-Runner test for guest.

## Implementation Notes

<details>
<summary>Detailed implementation steps</summary>

1. `tui/internal/browse/lister.go`:
   ```go
   package browse

   import (
       "bufio"
       "bytes"
       "context"
       "fmt"
       "os"
       "strconv"
       "strings"

       "github.com/deviantintegral/claude-code-ansible/tui/internal/lima"
   )

   type DirEntry struct {
       Name  string
       IsDir bool
       Size  int64
   }

   type DirLister interface {
       List(ctx context.Context, path string) ([]DirEntry, error)
   }

   type localLister struct{}

   func NewLocalLister() DirLister { return localLister{} }

   func (localLister) List(_ context.Context, path string) ([]DirEntry, error) {
       des, err := os.ReadDir(path)
       if err != nil {
           return nil, fmt.Errorf("read %s: %w", path, err)
       }
       out := make([]DirEntry, 0, len(des))
       for _, de := range des {
           var size int64
           if fi, err := de.Info(); err == nil {
               size = fi.Size()
           }
           out = append(out, DirEntry{Name: de.Name(), IsDir: de.IsDir(), Size: size})
       }
       return out, nil
   }

   type guestLister struct {
       cli *lima.Client
       vm  string
   }

   func NewGuestLister(cli *lima.Client, vm string) DirLister { return guestLister{cli: cli, vm: vm} }

   func (g guestLister) List(ctx context.Context, path string) ([]DirEntry, error) {
       var buf bytes.Buffer
       // find prints one line per entry: "<type>\t<size>\t<name>".
       if err := g.cli.Shell(ctx, g.vm, nil, &buf,
           "find", path, "-mindepth", "1", "-maxdepth", "1",
           "-printf", "%y\t%s\t%f\n"); err != nil {
           return nil, fmt.Errorf("list guest %s:%s: %w (%s)", g.vm, path, err, strings.TrimSpace(buf.String()))
       }
       var out []DirEntry
       sc := bufio.NewScanner(&buf)
       sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
       for sc.Scan() {
           parts := strings.SplitN(sc.Text(), "\t", 3)
           if len(parts) != 3 {
               continue // skip malformed line rather than failing the listing
           }
           size, _ := strconv.ParseInt(parts[1], 10, 64)
           out = append(out, DirEntry{Name: parts[2], IsDir: parts[0] == "d", Size: size})
       }
       return out, sc.Err()
   }
   ```
   (Passing `"find"` as the first argv element is fine; `lima.Client.Shell`
   prepends `shell <vm>`, so this runs `limactl shell <vm> find …`.)

2. `tui/internal/browse/lister_test.go`:
   - Local: `dir := t.TempDir()`; `os.WriteFile(dir+"/file.txt", []byte("hello"), 0o644)`; `os.Mkdir(dir+"/sub", 0o755)`; call `NewLocalLister().List(ctx, dir)`; assert it returns 2 entries and that the one named `sub` has `IsDir` and `file.txt` has `Size==5`.
   - Guest: define a tiny fake `Runner` (copy the 6-line shape from `lima/client_test.go`: it must implement `Output` and a `Stream` that WRITES canned bytes to `out` then returns nil). Build `lima.New(fake)`, wrap in `NewGuestLister(cli, "vm1")`, and assert two parsed entries. Because `lima.Runner` is an exported interface, the fake can live in this test file.
     ```go
     type fakeRunner struct{ out []byte }
     func (fakeRunner) Output(context.Context, ...string) ([]byte, error) { return nil, nil }
     func (f fakeRunner) Stream(_ context.Context, _ io.Reader, w io.Writer, _ ...string) error {
         _, err := w.Write(f.out); return err
     }
     // ...
     cli := lima.New(fakeRunner{out: []byte("d\t4096\tsrc\nf\t12\tfile.txt\n")})
     ```

3. Module-path note: import lima via its current path
   `github.com/deviantintegral/claude-code-ansible/tui/internal/lima`; do not
   hard-code it elsewhere (plans 06/07 may rename the module).
</details>

### Meaningful Test Strategy Guidelines
Your critical mantra for test generation is: "write a few tests, mostly integration".
- **DO** test the custom `find -printf` parsing (type/size/name) and the `os.ReadDir` mapping — these are YOUR transformations.
- **DON'T** test `os.ReadDir`/`find` themselves or stand up a real VM; use a temp dir and a fake `Runner`.

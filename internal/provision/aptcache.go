package provision

// aptcache.go caches apt archives fetched during a base build on the HOST,
// across rebuilds, so a `--rebuild` is CPU-bound (unpacking/configuring
// already-fetched .deb files) rather than network-bound (re-downloading
// them). It exists because profiling the base build's phases showed that on a
// fast link, the network fetch was not dominant — but re-fetching public .deb
// files on every rebuild is still pure waste, and a `--rebuild` used for local
// development can happen often.
//
// THE ROAD NOT TAKEN — a writable host mount. The obvious design is a writable
// Lima mount on the base overlay pointing apt's Dir::Cache::archives at a host
// directory (RenderBaseOverlay, internal/provision/overlay.go, still documents
// this in its doc comment). It was built and tested against a real Lima
// instance and rejected: Lima's mount type falls back to reverse-sshfs
// whenever virtiofsd is not installed on the host (virtiofsd is a SEPARATE
// system package, not bundled with Lima, and is not a safe assumption on an
// arbitrary Linux host — and Lima's reverse-sshfs default on macOS carries
// exactly the same restriction). Reverse-sshfs does not honour a guest
// `chown` of the mounted directory: `chown _apt
// /mnt/apt-cache/partial` fails with EPERM, which apt needs to succeed to use
// that directory as its archive cache. That failure was reproduced against a
// real `limactl` instance, not assumed.
//
// So this file takes the fallback approach instead: `limactl copy` moves
// the guest's OWN default apt cache (/var/cache/apt/archives — Debian keeps
// fetched .deb files there until `apt-get clean` runs, no config needed) out
// to the host after a successful base build, and back in before the next one.
// No host mount, no permission surface, nothing for Client.Configure to strip
// for THIS feature (it still strips any writable mount on principle — see its
// doc comment).
//
// Both directions rely on Client.Copy's documented "destination is always a
// directory, source is placed INSIDE it" contract: pushing a host directory
// named "archives" into the guest's /var/cache/apt/ lands its contents at
// /var/cache/apt/archives (apt's real cache dir); pulling the guest's
// /var/cache/apt/archives into a host directory lands it at
// <host dir>/archives — the same leaf name on both sides, so a harvest's
// output is exactly the next build's seed input.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lullabot/sandbar/internal/lima"
)

// aptArchivesDirName is the leaf directory name both directions of the
// seed/harvest copy rely on — see the package doc above.
const aptArchivesDirName = "archives"

// aptCacheHostDirFn is indirected through a package var, like playbookVersionFn
// and writeBaseVersionFn elsewhere in this package, so a test can point it at a
// throwaway directory instead of the real user cache dir.
var aptCacheHostDirFn = defaultAptCacheHostDir

// defaultAptCacheHostDir is the host directory that HOLDS the apt-archives
// cache; its "archives" subdirectory (aptArchivesDirName) is what actually
// gets copied to/from the guest. It lives under the user's cache dir
// (os.UserCacheDir(), which honours XDG_CACHE_HOME on Linux) so it survives
// across `sand` invocations and base rebuilds — a temp dir would defeat the
// entire point, since it would never survive to the next rebuild.
func defaultAptCacheHostDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "sand", "apt-cache"), nil
}

// aptCacheStagingDir is where a seed push actually lands in the guest: Copy
// places its source directory INSIDE the destination it is given (see the
// package doc), so pushing archivesDir (whose basename is aptArchivesDirName)
// to the guest's "/tmp/" lands at "/tmp/" + aptArchivesDirName — this constant
// spells that out explicitly so the Copy call and the sudo move command below
// cannot drift apart.
//
// It has to be a WORLD-WRITABLE guest scratch location in the first place
// because `limactl copy` shells in as the unprivileged guest user, never
// root: /var/cache/apt/archives is root:root 0755, and pushing straight into
// it is a plain `dest open: Permission denied` for every file — reproduced
// against a real Lima instance, not assumed. Landing here first and moving
// into the real cache dir with a separate `sudo` shell step is what actually
// works.
const aptCacheStagingDir = "/tmp/" + aptArchivesDirName

// seedAptCache pushes a previously harvested apt-archives cache from the host
// into the RUNNING base instance's /var/cache/apt/archives, so the base
// playbook's apt installs reuse already-fetched .deb files instead of
// re-downloading them.
//
// This is a two-step push, not a direct Copy into the cache dir — see
// aptCacheStagingDir's doc comment for why the direct form does not work.
//
// It is deliberately best-effort and NEVER returns a non-nil error: caching is
// an optimisation, not a correctness requirement, so a failure here (no cache
// yet, a Copy error, a permissions problem) is reported to out and swallowed
// rather than failing the whole base build — the same posture buildBase
// already takes with the playbook-version stamp.
func (p *Provisioner) seedAptCache(ctx context.Context, name string, out io.Writer) error {
	hostDir, err := aptCacheHostDirFn()
	if err != nil {
		fmt.Fprintf(out, "Note: could not determine the apt cache dir (%v); this build will not reuse previously cached packages.\n", err)
		return nil
	}
	archivesDir := filepath.Join(hostDir, aptArchivesDirName)
	entries, err := os.ReadDir(archivesDir)
	if err != nil || len(entries) == 0 {
		return nil // no cache yet (first build, or a prior harvest never ran) — nothing to seed
	}
	if err := p.Lima.Copy(ctx, out, true, archivesDir, lima.GuestPath(name, "/tmp/")); err != nil {
		fmt.Fprintf(out, "Note: could not seed the apt archive cache (%v); this build will re-download previously cached packages.\n", err)
		return nil
	}
	// Move what was just staged into the real cache dir as root, then remove
	// the staging copy so it never lingers as a second, stale copy of every
	// .deb the base ever fetched. `cp` (not `mv`) tolerates the staged
	// directory being empty or partially transferred without erroring the
	// whole step; a glob that matches nothing is a no-op under nullglob-less
	// bash's default (an unmatched glob is passed through literally to cp,
	// which then fails on that one nonexistent name) — acceptable since this
	// whole step is best-effort already.
	moveCmd := fmt.Sprintf(
		"mkdir -p /var/cache/apt/archives && cp -f %s/*.deb /var/cache/apt/archives/ 2>/dev/null; rm -rf %s",
		aptCacheStagingDir, aptCacheStagingDir,
	)
	if err := p.Lima.Shell(ctx, name, nil, io.Discard, "sudo", "bash", "-c", moveCmd); err != nil {
		fmt.Fprintf(out, "Note: could not move the seeded apt archive cache into place (%v); this build will re-download previously cached packages.\n", err)
	}
	return nil
}

// harvestAptCache pulls the RUNNING base instance's /var/cache/apt/archives
// back to the host cache dir after a successful base playbook run, so the
// NEXT build's seedAptCache has something to push. Best-effort, for the same
// reason as seedAptCache, and for the same reason NEVER returns a non-nil
// error.
func (p *Provisioner) harvestAptCache(ctx context.Context, name string, out io.Writer) error {
	hostDir, err := aptCacheHostDirFn()
	if err != nil {
		fmt.Fprintf(out, "Note: could not determine the apt cache dir (%v); this build's downloads will not be cached for next time.\n", err)
		return nil
	}
	if err := os.MkdirAll(hostDir, 0o755); err != nil {
		fmt.Fprintf(out, "Note: could not create the apt cache dir %s (%v); this build's downloads will not be cached for next time.\n", hostDir, err)
		return nil
	}
	// apt's archives dir also holds `lock` (0640, root-owned) and `partial/`
	// (0700, owned by _apt) — both apt-internal bookkeeping, not cached .deb
	// files, and both unreadable by the unprivileged user `limactl copy` shells
	// in as. Left in place, scp fails to read them and Client.Copy reports the
	// WHOLE harvest as failed even though every .deb file transferred fine —
	// reproduced against a real Lima instance, not assumed. Clear them first so
	// the copy that follows only ever touches files it can actually read.
	if err := p.Lima.Shell(ctx, name, nil, io.Discard,
		"sudo", "rm", "-rf", "/var/cache/apt/archives/lock", "/var/cache/apt/archives/partial",
	); err != nil {
		fmt.Fprintf(out, "Note: could not clear apt's lock/partial before harvesting the cache (%v); the harvest may report a spurious failure below.\n", err)
	}
	if err := p.Lima.Copy(ctx, out, true, lima.GuestPath(name, "/var/cache/apt/archives"), hostDir); err != nil {
		fmt.Fprintf(out, "Note: could not harvest the apt archive cache (%v); this build's downloads will not be cached for next time.\n", err)
	}
	return nil
}

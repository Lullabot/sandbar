package provision

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/lullabot/sandbar/internal/lima"
)

// schemeRe matches a leading URL scheme (e.g. "https://"), mirroring the strip
// the project role applies via regex_replace with the pattern ^[a-zA-Z]+://.
var schemeRe = regexp.MustCompile(`^[a-zA-Z]+://`)

// OrgRelDir turns a clone URL into the per-org directory relative to the
// guest home, mirroring roles/project/tasks/main.yml: host = first path segment
// after the scheme, relpath = the rest minus any trailing slash(es) and a
// trailing ".git", and the result is host/dirname(relpath) (e.g.
// https://github.com/org/repo -> github.com/org). Returns ("", false) when the
// URL is empty or has no org component (a bare repo with no directory part, so
// dirname is ".").
func OrgRelDir(cloneURL string) (string, bool) {
	if cloneURL == "" {
		return "", false
	}
	// Strip the scheme to leave host/path, then split off the first segment as
	// the host (e.g. "github.com").
	rest := schemeRe.ReplaceAllString(cloneURL, "")
	host, relpath, ok := strings.Cut(rest, "/")
	if !ok {
		return "", false // host only, no path => no org
	}
	// Trim trailing slashes before ".git" so a URL like .../org/repo/ resolves to
	// org "org", not "org/repo" — matching the role's regex_replace('/+$', '').
	relpath = strings.TrimRight(relpath, "/")
	relpath = strings.TrimSuffix(relpath, ".git")
	org := path.Dir(relpath)
	if org == "." {
		return "", false // a bare "repo" with no org segment
	}
	return host + "/" + org, true
}

// CheckoutRelDir returns the guest-home-relative directory the project role
// clones a repo into (<host>/<org>/<repo>), or ("", false) when cloneURL is
// empty or has no org segment. It extends OrgRelDir (which yields the parent
// <host>/<org>) by appending the repo directory name, so the TUI can open the
// guest file browser at a VM's project checkout.
func CheckoutRelDir(cloneURL string) (string, bool) {
	orgRel, ok := OrgRelDir(cloneURL)
	if !ok {
		return "", false
	}
	rest := schemeRe.ReplaceAllString(cloneURL, "")
	rest = strings.TrimRight(rest, "/")
	rest = strings.TrimSuffix(rest, ".git")
	return orgRel + "/" + path.Base(rest), true
}

// guestHome resolves the guest user's home directory by reading the passwd entry
// over `limactl shell` (`getent passwd <user>` => user:x:uid:gid:gecos:home:shell).
// The home is field index 5; fewer than 7 fields means an unexpected line.
func guestHome(ctx context.Context, cli *lima.Client, name, user string) (string, error) {
	// ShellOut (stdout only), not Shell (merged stdout+stderr): getent output is
	// parsed by splitting on ':', and limactl's cd-to-host-cwd warning on stderr
	// is full of colons — merging it in would corrupt the parse and yield a
	// garbage home directory.
	out, err := cli.ShellOut(ctx, name, "getent", "passwd", user)
	if err != nil {
		return "", fmt.Errorf("getent passwd %s: %w", user, err)
	}
	fields := strings.Split(strings.TrimSpace(string(out)), ":")
	if len(fields) < 7 {
		return "", fmt.Errorf("unexpected getent passwd output for %s: %q", user, string(out))
	}
	return fields[5], nil
}

// newStageDir creates a private (0700) host staging directory for archives that
// cross a destroy/recreate. The temp name carries a recognisable prefix so a
// leaked dir is easy to spot.
func newStageDir() (string, error) {
	dir, err := os.MkdirTemp("", "sand-reset-*")
	if err != nil {
		return "", fmt.Errorf("create stage dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("lock down stage dir: %w", err)
	}
	return dir, nil
}

// removeStageDir best-effort deletes a staging directory; cleanup failures are
// non-fatal to the reset flow.
func removeStageDir(dir string) { _ = os.RemoveAll(dir) }

// StageOut streams guestPaths (relative to home) out of a running VM into the
// host archive file using `tar` over `limactl shell` as root. --ignore-failed-read
// keeps a missing optional path (e.g. ~/.claude.json) from aborting the archive;
// tar preserves the original modes/ownership inside the tarball.
func StageOut(ctx context.Context, cli *lima.Client, name, home string, guestPaths []string, hostArchive string) error {
	file, err := os.Create(hostArchive)
	if err != nil {
		return fmt.Errorf("create archive %s: %w", hostArchive, err)
	}
	defer file.Close()

	argv := append([]string{"sudo", "tar", "-C", home, "--ignore-failed-read", "-czf", "-"}, guestPaths...)
	if err := cli.Shell(ctx, name, nil, file, argv...); err != nil {
		return fmt.Errorf("stage out: %w", err)
	}
	return nil
}

// StageIn extracts the host archive back into the guest home and re-chowns the
// restored top-level paths to the user. Extraction runs as root (so the files
// land root-owned and must be chowned back); the extract MUST complete before
// the chown, since chown targets the just-written paths.
func StageIn(ctx context.Context, cli *lima.Client, name, home, user string, topPaths []string, hostArchive string) error {
	file, err := os.Open(hostArchive)
	if err != nil {
		return fmt.Errorf("open archive %s: %w", hostArchive, err)
	}
	defer file.Close()

	if err := cli.Shell(ctx, name, file, io.Discard, "sudo", "tar", "-C", home, "-xzf", "-"); err != nil {
		return fmt.Errorf("stage in extract: %w", err)
	}

	// chown needs concrete paths, so resolve each top-level path to an absolute
	// path under home.
	absPaths := make([]string, 0, len(topPaths))
	for _, p := range topPaths {
		absPaths = append(absPaths, home+"/"+p)
	}
	argv := append([]string{"sudo", "chown", "-R", user + ":" + user}, absPaths...)
	if err := cli.Shell(ctx, name, nil, io.Discard, argv...); err != nil {
		return fmt.Errorf("stage in chown: %w", err)
	}
	return nil
}

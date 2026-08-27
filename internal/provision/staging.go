package provision

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
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
func guestHome(ctx context.Context, cli guestRunner, name, user string) (string, error) {
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
//
// It uses ShellStreamOut (stdout only), NOT Shell (merged stdout+stderr): the
// gzip stream is tar's stdout, and `limactl shell` emits a `cd <host-cwd>` "No
// such file or directory" warning on stderr whenever that host path is absent
// in the guest. Merging that warning into the archive corrupts the gzip, and
// the later StageIn `tar -xzf` then aborts with exit status 2.
func StageOut(ctx context.Context, cli guestRunner, name, home string, guestPaths []string, hostArchive string) error {
	file, err := os.Create(hostArchive)
	if err != nil {
		return fmt.Errorf("create archive %s: %w", hostArchive, err)
	}
	defer file.Close()

	argv := append([]string{"sudo", "tar", "-C", home, "--ignore-failed-read", "-czf", "-"}, guestPaths...)
	if err := cli.ShellStreamOut(ctx, name, nil, file, argv...); err != nil {
		return fmt.Errorf("stage out: %w", err)
	}
	return nil
}

// ancestorDirs returns the intermediate directories a relative path passes
// through, nearest the root first and excluding the path itself:
// "github.com/octocat" -> ["github.com"]. A single-segment path (".claude") has
// none. Paths here are always guest-home-relative and slash-separated (they are
// tar member names, not host paths), so this is deliberately string surgery
// rather than filepath, which would use the HOST's separator.
func ancestorDirs(relPath string) []string {
	parts := strings.Split(strings.Trim(relPath, "/"), "/")
	var dirs []string
	for i := 1; i < len(parts); i++ {
		dirs = append(dirs, strings.Join(parts[:i], "/"))
	}
	return dirs
}

// StageIn extracts the host archive back into the guest home and re-chowns the
// restored top-level paths to the user. Extraction runs as root (so the files
// land root-owned and must be chowned back); the extract MUST complete before
// the chown, since chown targets the just-written paths.
//
// The ancestors of each restored path are created — as the user — BEFORE the
// extract, and that ordering is the whole point. tar creates a missing
// intermediate directory itself, as root, since the extract runs as root; that
// directory is not a member of the archive (`tar -C home github.com/octocat`
// stores "github.com/octocat/" and below, never "github.com/"), so the chown
// below, which targets only the restored paths, never reaches it. A restored
// project therefore left ~/github.com owned by root:root on a VM where a plain
// create leaves it owned by the user, and the next `git clone` into a sibling
// org directory failed with "Permission denied". `install -d` repairs an
// existing directory's owner and mode as well as creating a missing one, so a
// VM already left in that state by an earlier reset is healed by the next one.
func StageIn(ctx context.Context, cli guestRunner, name, home, user string, topPaths []string, hostArchive string) error {
	file, err := os.Open(hostArchive)
	if err != nil {
		return fmt.Errorf("open archive %s: %w", hostArchive, err)
	}
	defer file.Close()

	// `install -d` is happy to be handed the same directory twice, so no dedup is
	// needed for the overlapping ancestors two top-level paths could share.
	var ancestors []string
	for _, p := range topPaths {
		for _, dir := range ancestorDirs(p) {
			ancestors = append(ancestors, home+"/"+dir)
		}
	}
	if len(ancestors) > 0 {
		argv := append([]string{"sudo", "install", "-d", "-o", user, "-g", user, "-m", "755"}, ancestors...)
		if err := cli.Shell(ctx, name, nil, io.Discard, argv...); err != nil {
			return fmt.Errorf("stage in prepare parents: %w", err)
		}
	}

	if err := cli.Shell(ctx, name, file, io.Discard, "sudo", "tar", "-C", home, "-xzf", "-"); err != nil {
		return fmt.Errorf("stage in extract: %w", err)
	}

	// chown needs concrete paths, and only the ones the extract actually
	// produced. A top-level path that was missing in the SOURCE VM is missing
	// from the archive too — StageOut's --ignore-failed-read drops it rather than
	// aborting — and `chown -R` on a path that is not there fails, taking the
	// whole call with it. That turned a completed reset into a REPORTED FAILURE
	// for any VM whose user had never launched Claude Code, since ~/.claude.json
	// only appears on first use.
	absPaths := make([]string, 0, len(topPaths))
	for _, p := range topPaths {
		abs := home + "/" + p
		present, err := GuestPathExists(ctx, cli, name, abs)
		if err != nil {
			return fmt.Errorf("stage in chown: %w", err)
		}
		if present {
			absPaths = append(absPaths, abs)
		}
	}
	if len(absPaths) == 0 {
		return nil
	}
	argv := append([]string{"sudo", "chown", "-R", user + ":" + user}, absPaths...)
	if err := cli.Shell(ctx, name, nil, io.Discard, argv...); err != nil {
		return fmt.Errorf("stage in chown: %w", err)
	}
	return nil
}

// GuestPathExists reports whether path is present in the guest. It runs under
// sudo so a path inside a directory the calling user cannot traverse still
// answers honestly.
//
// A probe that could not be RUN is not the same answer as "the path is not
// there", and that difference is why this returns an error rather than a bare
// bool: Reset asks it whether the project tree needs staging out, and then
// DELETES the VM. Reading an unreachable guest — a dropped ssh transport, a
// limactl that could not attach — as "there is nothing to preserve" would
// destroy the very tree the user asked to keep. So only `test`'s own exit
// status 1 means absent; anything else comes back as an error for the caller to
// abort on, while the VM is still there.
func GuestPathExists(ctx context.Context, cli guestRunner, name, path string) (bool, error) {
	if _, err := cli.ShellOut(ctx, name, "sudo", "test", "-e", path); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("probe %s in %q: %w", path, name, err)
	}
	return true, nil
}

// direnvConfigFiles are the two names `direnv allow DIR` looks for. Both are
// probed because either one is enough for direnv to have something to approve:
// the project role writes .env, but a restored checkout may carry a .envrc the
// user wrote themselves.
var direnvConfigFiles = []string{".envrc", ".env"}

// AllowDirenv approves dir for the guest user with `direnv allow` — but only
// when dir actually holds a .envrc or .env.
//
// The guard is the fix for a reset that failed at its very last step. `direnv
// allow DIR` exits 1 with "error .envrc or .env file not found" when the
// directory has neither, and the per-org .env it is meant to approve is written
// by roles/project ONLY when a clone TOKEN was supplied. So every VM that
// cloned a PUBLIC repo — no token, no .env — failed its preserve-project reset
// here, after the VM had already been deleted, re-cloned, finalized and
// restored: the data was fine, the VM was fine, and the run was reported as a
// failure. In the TUI that also costs the run its success bookkeeping
// (manage.RecordSuccess and the secrets re-apply both hang off a clean return),
// so an edited CPU/disk/clone-URL is applied to the VM but never recorded.
//
// The probe is a separate `test -e` rather than a shell one-liner wrapped
// around direnv on purpose: the call it guards runs under `sudo -i`, which
// hands the command to the target user's LOGIN SHELL as a re-quoted string, and
// a script with quoting of its own is not something to feed through that.
//
// A failure of direnv itself, when there IS something to approve, stays fatal —
// an unapproved .env means the guest's GH_TOKEN never loads, and git operations
// fail later in a way that is much harder to connect back to the reset.
func AllowDirenv(ctx context.Context, cli guestRunner, name, user, dir string, out io.Writer) error {
	for _, f := range direnvConfigFiles {
		present, err := GuestPathExists(ctx, cli, name, dir+"/"+f)
		if err != nil {
			return err
		}
		if present {
			return cli.Shell(ctx, name, nil, out, "sudo", "-iu", user, "direnv", "allow", dir)
		}
	}
	return nil
}

// ProjectPlan is what a reset decided to do about the per-org project tree,
// having asked the GUEST rather than trusted the config.
type ProjectPlan struct {
	// OrgRel is the guest-home-relative directory the clone URL names, or "" when
	// the URL has no org segment. Set even when nothing was staged, so a caller
	// can name the directory in its "nothing to preserve" message.
	OrgRel string
	// Staged reports that OrgRel exists in the guest and was archived, so the
	// reset must restore it afterwards.
	Staged bool
	// RestoresCheckout reports that the archive contains the CHECKOUT itself, so
	// the finalize playbook's clone step would be redundant and is skipped. An
	// org directory without the checkout in it (a failed clone leaves one behind,
	// holding the .env the role wrote) is still worth preserving for that .env —
	// but skipping the clone as well would mean the reset never produced the repo.
	RestoresCheckout bool
}

// PlanProject decides what a preserve-project reset should do, and stages the
// tree when there is one, so both backends reach the same answer from one place.
//
// WHAT IS ACTUALLY IN THE GUEST decides this, not what the config implies should
// be there. `tar --ignore-failed-read` happily produces an empty archive for a
// directory that is not there, and a caller that then skips the finalize clone
// — to protect a tree that was never staged — restores nothing and leaves the VM
// with NO project at all, silently. Three ordinary paths reach that: the reset
// form's clone URL edited to a different org, an original clone that failed, or
// a user who deleted the checkout. In each, the honest answer is to clone the
// repo the config now names.
//
// A probe that could not RUN is not an answer, and the caller's next step
// deletes the VM — so an unreachable guest returns an error here rather than
// being read as "there is nothing to preserve" (see GuestPathExists).
//
// The checkout is probed FIRST because it implies the org directory it lives in:
// the healthy case costs one guest round trip, and the second probe is paid only
// when the checkout is already gone.
func PlanProject(ctx context.Context, cli guestRunner, name, home, cloneURL, hostArchive string) (ProjectPlan, error) {
	var plan ProjectPlan
	plan.OrgRel, _ = OrgRelDir(cloneURL) // "" when the URL carries no org segment
	if plan.OrgRel == "" {
		return plan, nil
	}

	checkoutRel, _ := CheckoutRelDir(cloneURL) // non-empty whenever OrgRel is
	present, err := GuestPathExists(ctx, cli, name, home+"/"+checkoutRel)
	if err != nil {
		return plan, err
	}
	plan.RestoresCheckout = present
	plan.Staged = present
	if !plan.Staged {
		if plan.Staged, err = GuestPathExists(ctx, cli, name, home+"/"+plan.OrgRel); err != nil {
			return plan, err
		}
	}
	if !plan.Staged {
		return plan, nil
	}
	if err := StageOut(ctx, cli, name, home, []string{plan.OrgRel}, hostArchive); err != nil {
		return plan, err
	}
	return plan, nil
}

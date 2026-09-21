// preserve.go is the one place that decides WHAT a reset carries across the
// rebuild and WHEN each piece goes back.
//
// Both backends' resets — internal/provision's Lima Reset and the Proxmox
// provider's resetInstance — drive it, for the same reason they already share
// StageGuard and PlanProject: the interesting part of a reset is not the
// tar commands, it is the ordering rules around them, and two copies of an
// ordering rule are two chances to get one of them wrong. Everything here is a
// free function over guestRunner, so neither backend has to know about the
// other's transport.
package provision

import (
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

// The archive names a reset stages inside its StageGuard directory. There is one
// per KIND of thing preserved rather than one shared file, because they are
// restored at different moments — before the finalize playbook or after it — and
// a single archive could not be.
//
// They are compressed, but the name does not say with what: the compressor is
// whatever the SOURCE guest turned out to have (see compress.go), so a fixed
// ".tgz" would be a lie on every guest with zstd — and a misleading one exactly
// when it is read, which is a user recovering a failed reset's data by hand from
// the path StageGuard.Fail names. A plain ".tar" is honest for both, because GNU
// tar detects the compression itself when it is reading a file it can seek.
const (
	claudeArchive  = "claude.tar"
	projectArchive = "project.tar"
	homeArchive    = "home.tar"
	extrasArchive  = "extras.tar"
)

// The noun each archive is narrated with while it moves: "Backing up home:
// 1.4 GB, 61 MB/s" (see stageprogress.go). They are short because that line is
// rendered inside a tile barely wider than it is — and they are words rather
// than file names because "home.tar" is not what a user is waiting for.
const (
	claudeLabel  = "Claude data"
	projectLabel = "project tree"
	homeLabel    = "home"
	extrasLabel  = "checkouts"
)

// claudePaths are the two home-relative paths "preserve Claude" keeps: the
// Claude Code state directory and the login/history file beside it.
var claudePaths = []string{".claude", ".claude.json"}

// homeExcludes is everything a whole-home preserve deliberately leaves behind,
// spelled as tar member names (the archive is created with `-C <home> .`, so
// every member starts "./").
//
// ~/.ssh/authorized_keys is excluded because restoring it would hand the REBUILT
// VM the OLD VM's key set — and ssh into the rebuilt VM is how the finalize
// playbook, the heartbeat and every `sand shell` get in. The two files are all
// but always identical (Lima's per-host key is shared by every instance in a
// Lima home), so restoring it buys nothing, while the case where they differ is
// a VM nobody can log into, discovered halfway through its own reset. A
// disposable dev VM's authorized_keys is machinery, not user data.
var homeExcludes = []string{"./.ssh/authorized_keys"}

// PreservePlan is everything ONE reset decided to carry across the rebuild, and
// the archives it staged for them. StagePreserve builds it while the source VM
// is still alive; RestoreBeforeFinalize and RestoreAfterFinalize put it back.
type PreservePlan struct {
	// WholeHome reports that homeArchive holds the entire guest home. It is
	// exclusive with every other field but Project: anything Claude, Extras or a
	// project tree would have preserved is already inside that one archive.
	WholeHome bool

	// Claude reports that claudeArchive holds ~/.claude and ~/.claude.json.
	Claude bool

	// Project is what the reset decided about the repo this VM was created from.
	// Its RestoresCheckout is consulted even under WholeHome (see probeProject);
	// its Staged is not, because the home archive already carries the tree.
	Project ProjectPlan

	// Extras are the guest-home-relative paths staged into extrasArchive: the
	// checkouts and worktrees the user picked by hand, minus anything already
	// covered by another preserved path.
	Extras []string
}

// StagePreserve copies everything opts asks to keep out of the RUNNING source VM
// and into stage, returning the plan the restore half is driven from. It is
// called with the guest still intact, so every error it returns is recoverable:
// the caller has not destroyed anything yet and StageGuard drops the partial
// copy.
//
// The whole-home case short-circuits the rest by construction rather than by
// convention. One archive of ~ contains the Claude login, the project tree and
// every checkout the user could have picked individually, so staging any of them
// a second time would double the copy, double the time, and give the restore two
// versions of the same file to disagree about.
//
// What the GUEST actually holds decides every branch here, never what the config
// implies should be there — see PlanProject for the reset that cloned nothing
// and restored nothing while believing it had done both.
func StagePreserve(ctx context.Context, cli guestRunner, name, home, user, cloneURL string, opts ResetOptions, stage *StageGuard, out io.Writer) (PreservePlan, error) {
	var plan PreservePlan

	if opts.PreserveHome {
		plan.WholeHome = true
		if err := StageOut(ctx, cli, name, home, user, []string{"."}, stage.Path(homeArchive), homeLabel, out, homeExcludes...); err != nil {
			return plan, err
		}
		// The tree is in the archive, but the finalize playbook still has to be
		// told not to clone over it — hence the probe, and hence Staged staying
		// false: there is nothing SEPARATE to restore afterwards.
		project, err := probeProject(ctx, cli, name, home, cloneURL)
		if err != nil {
			return plan, err
		}
		project.Staged = false
		plan.Project = project
		return plan, nil
	}

	if opts.PreserveClaude {
		if err := StageOut(ctx, cli, name, home, user, claudePaths, stage.Path(claudeArchive), claudeLabel, out); err != nil {
			return plan, err
		}
		plan.Claude = true
	}

	if opts.PreserveProject {
		project, err := PlanProject(ctx, cli, name, home, user, cloneURL, stage.Path(projectArchive), out)
		if err != nil {
			return plan, err
		}
		plan.Project = project
		if !project.Staged && project.OrgRel != "" {
			note(out, "Note: %q has no ~/%s to preserve; the project will be cloned fresh instead.", name, project.OrgRel)
		}
	}

	extras, err := planExtras(ctx, cli, name, home, opts.PreservePaths, plan.Project, out)
	if err != nil {
		return plan, err
	}
	if len(extras) > 0 {
		if err := StageOut(ctx, cli, name, home, user, extras, stage.Path(extrasArchive), extrasLabel, out); err != nil {
			return plan, err
		}
		plan.Extras = extras
	}
	return plan, nil
}

// planExtras turns the caller's list of hand-picked paths into the home-relative
// set that is actually worth archiving: normalised, de-duplicated, stripped of
// anything another preserved path already covers, and probed against the guest.
//
// A path that is not there ANY MORE is a note, not a failure: these come from
// the host-side checkout registry, which is a cache of the last sweep of a
// running guest and can name a directory the user has since deleted. A path that
// is not a legal preserve target — outside the home, or climbing out of it with
// ".." — IS a failure, and deliberately one raised here, while the VM is still
// intact and the user can simply pick again. Both halves matter: the second is
// also the gate that stops a guest-supplied string from making the restore's
// `chown -R` walk out of the home directory (see preservePathRel).
func planExtras(ctx context.Context, cli guestRunner, name, home string, paths []string, project ProjectPlan, out io.Writer) ([]string, error) {
	var rels []string
	for _, p := range paths {
		rel, err := preservePathRel(home, p)
		if err != nil {
			return nil, err
		}
		rels = append(rels, rel)
	}
	// The project toggle preserves the whole per-org directory, so a checkout
	// inside it is already coming back; archiving it again would put two copies
	// of the same tree in the staging directory.
	var covered []string
	if project.Staged {
		covered = append(covered, project.OrgRel)
	}
	rels = pruneCovered(rels, covered)

	var present []string
	for _, rel := range rels {
		ok, err := GuestPathExists(ctx, cli, name, home+"/"+rel)
		if err != nil {
			return nil, err
		}
		if !ok {
			note(out, "Note: %q has no ~/%s any more; nothing to preserve from it.", name, rel)
			continue
		}
		present = append(present, rel)
	}
	return present, nil
}

// preservePathRel turns one user- or UI-supplied preserve path into a path
// relative to the guest home, accepting the three spellings a human or a sweep
// can produce: an absolute guest path ("/home/dev/src/app", what
// internal/checkouts records), a tilde path ("~/src/app", what the CLI's help
// shows), and a bare relative one ("src/app").
//
// It rejects anything that is not strictly INSIDE the home directory. That is
// not tidiness: these strings reach `tar -C <home> <rel>` and, on the way back,
// `chown -R <user> <home>/<rel>`, so a "../.." that survived to the restore
// would hand a recursive chown to / as root. The home directory itself is
// rejected too, with a pointer at the option that means it — preserving "." by
// this route would archive the home twice over, once here and once as every
// other selected path inside it.
func preservePathRel(home, p string) (string, error) {
	raw := strings.TrimSpace(p)
	if raw == "" {
		return "", fmt.Errorf("preserve: empty path")
	}
	home = path.Clean(home)

	rel := raw
	switch {
	case raw == "~":
		rel = "."
	case strings.HasPrefix(raw, "~/"):
		rel = strings.TrimPrefix(raw, "~/")
	case strings.HasPrefix(raw, "/"):
		abs := path.Clean(raw)
		switch {
		case abs == home:
			rel = "."
		case strings.HasPrefix(abs, home+"/"):
			rel = strings.TrimPrefix(abs, home+"/")
		default:
			return "", fmt.Errorf("preserve: %s is outside the VM's home directory (%s); only paths inside the home can be preserved", raw, home)
		}
	}

	rel = path.Clean(rel)
	if rel == "." || rel == "/" {
		return "", fmt.Errorf("preserve: %s is the home directory itself; preserve the whole home instead if that is what you meant", raw)
	}
	if rel == ".." || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("preserve: %s escapes the VM's home directory; only paths inside the home can be preserved", raw)
	}
	return rel, nil
}

// pruneCovered returns rels sorted, de-duplicated, and with every path that
// lives inside another kept path (or inside one of covered) removed.
//
// A linked worktree under the now-common ".claude/worktrees/<name>" convention
// sits inside its own repository, so "keep this repo" and "keep that worktree"
// routinely name the same bytes twice — and tar would dutifully archive them
// twice. Sorting first is what makes one pass enough: a parent always sorts
// before anything beneath it.
func pruneCovered(rels, covered []string) []string {
	sort.Strings(rels)
	kept := make([]string, 0, len(rels))
	inside := func(list []string, p string) bool {
		for _, q := range list {
			if p == q || strings.HasPrefix(p, q+"/") {
				return true
			}
		}
		return false
	}
	for _, rel := range rels {
		if inside(covered, rel) || inside(kept, rel) {
			continue
		}
		kept = append(kept, rel)
	}
	return kept
}

// RestoreBeforeFinalize puts back everything that must be on disk BEFORE the
// finalize playbook runs, so the playbook lands on top of it.
//
// Which things those are follows from what the playbook does to them. It
// re-writes ~/.claude/settings.json, so the Claude login and history have to be
// there first or the settings would be layered onto nothing and the restore
// would then overwrite them. A whole home is the same argument at full size, and
// it is the entire point of preserving one: rebuilding a VM to pick up playbook
// changes is only useful if the playbook gets the last word over the files it
// owns.
func RestoreBeforeFinalize(ctx context.Context, cli guestRunner, name, home, user string, plan PreservePlan, stage *StageGuard, out io.Writer) error {
	switch {
	case plan.WholeHome:
		if err := StageIn(ctx, cli, name, home, user, []string{"."}, stage.Path(homeArchive), homeLabel, out); err != nil {
			return fmt.Errorf("restore the home directory into %q: %w", name, err)
		}
	case plan.Claude:
		if err := StageIn(ctx, cli, name, home, user, claudePaths, stage.Path(claudeArchive), claudeLabel, out); err != nil {
			return fmt.Errorf("restore Claude into %q: %w", name, err)
		}
	}
	return nil
}

// RestoreAfterFinalize puts back everything the finalize playbook must NOT get
// the last word over — the user's own working trees — and re-approves the
// restored per-org .env for direnv.
//
// The project tree goes back here, not before finalize, because the project
// role's clone step would otherwise write into the same directory. (The clone is
// skipped when Project.RestoresCheckout, but only when the checkout is genuinely
// coming back; an org directory without a checkout in it still gets cloned
// into.) Hand-picked checkouts follow the same rule for the same reason, and
// because "the playbook never touches the work you asked to keep" is one rule to
// remember rather than two.
//
// The direnv approval runs for a whole-home restore as well as a staged project.
// It is idempotent, and the alternative is a VM whose GH_TOKEN silently never
// loads because the .env came back unapproved.
func RestoreAfterFinalize(ctx context.Context, cli guestRunner, name, home, user string, plan PreservePlan, stage *StageGuard, out io.Writer) error {
	if plan.Project.Staged {
		if err := StageIn(ctx, cli, name, home, user, []string{plan.Project.OrgRel}, stage.Path(projectArchive), projectLabel, out); err != nil {
			return fmt.Errorf("restore the project into %q: %w", name, err)
		}
	}
	if len(plan.Extras) > 0 {
		if err := StageIn(ctx, cli, name, home, user, plan.Extras, stage.Path(extrasArchive), extrasLabel, out); err != nil {
			return fmt.Errorf("restore preserved checkouts into %q: %w", name, err)
		}
	}
	if plan.Project.OrgRel != "" && (plan.Project.Staged || plan.WholeHome) {
		if err := AllowDirenv(ctx, cli, name, user, home+"/"+plan.Project.OrgRel, out); err != nil {
			return fmt.Errorf("approve the restored .env in %q: %w", name, err)
		}
	}
	return nil
}

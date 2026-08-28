package drupalorg

import (
	"fmt"
	"strings"
)

// issueNamespacePrefix is the fork-path namespace every commit destination
// must fall under unless a developer deliberately overrides the guard. On
// drupal.org, every issue fork's path is "issue/<module>-<nid>" (see
// ForkPath); a commit destination outside that namespace is, on
// drupal.org, always a canonical project — writing there directly is the
// case that motivated this entire design and must never happen as a
// default or a bug.
const issueNamespacePrefix = "issue/"

// Destination names where a publication lands: the project commits are
// replayed onto, the branch on it those commits land on, and the canonical
// parent project a merge request is opened against. Every field is derived
// on the host, from module/issue/fork inputs that never touch guest-supplied
// content — see NewDestination, whose signature is what enforces that.
type Destination struct {
	// ForkPath is the project path commits are replayed onto, e.g.
	// "issue/<module>-<nid>". This is the field the issue/ guard checks.
	ForkPath string
	// Branch is the branch on ForkPath commits land on, derived from module
	// and issue the same way ForkPath is.
	Branch string
	// ParentID is the canonical parent project's numeric ID, read from
	// forked_from_project.id. Naming a canonical project here is never
	// refused: a merge request is a proposal directed at a project, not a
	// write to its code, and on drupal.org every merge request necessarily
	// runs fork -> parent, so a guard against naming a canonical project
	// here at all would forbid the one thing publication exists to do (see
	// the package doc comment and the "Destination selection and
	// confirmation" section of plan 21). ParentID/ParentPath/ParentBranch
	// are also never guest-influenced: they are read anonymously from the
	// fork's own forked_from_project, never supplied by a payload.
	ParentID int
	// ParentPath is the canonical parent's path, e.g. "project/<module>",
	// read from forked_from_project.path_with_namespace. See ParentID for
	// why this is never subject to the issue/ guard.
	ParentPath string
	// ParentBranch is the canonical parent's development branch, used as a
	// merge request's target_branch, read from
	// forked_from_project.default_branch.
	ParentBranch string
}

// NewDestination builds a Destination from host-side inputs only: module and
// issue identify the checkout and the issue being published; forkPath is the
// project path already resolved for them — normally ForkPath(module, issue),
// queried anonymously via task 3's Client.Project — and fork is the
// ProjectInfo that query returned. It takes no ChangeSet argument at all:
// the payload plays no part whatsoever in choosing a destination, which is
// the enforcement this whole design rests on (see the package doc comment).
// If a future caller wanted a payload to influence where it goes, it could
// not — this constructor has no parameter to smuggle it through.
//
// allowOutsideIssueNS is the only way to construct a Destination whose
// forkPath falls outside the "issue/" namespace: not a default, not an
// environment variable, an explicit argument a caller must pass
// deliberately. Without it, a forkPath outside that namespace is refused
// with an error naming what was refused and how to override it. The guard
// applies to forkPath (the commit destination) only — never to the parent
// derived from fork.ForkedFromProject, which necessarily names a canonical
// project and must not be refused; see ParentID's doc comment for why.
func NewDestination(module string, issue int, forkPath string, fork *ProjectInfo, allowOutsideIssueNS bool) (Destination, error) {
	// ForkPath independently validates module and issue (rejecting anything
	// that doesn't match drupal.org's actual naming rules) and supplies the
	// commit branch name. When forkPath falls inside the issue/ namespace it
	// must equal this same canonical path (checked below) — otherwise the
	// branch name derived here could belong to a different issue than
	// forkPath does.
	canonical, err := ForkPath(module, issue)
	if err != nil {
		return Destination{}, err
	}
	branch := strings.TrimPrefix(canonical, issueNamespacePrefix)

	// A fork path is a project path rather than a file path, but it is
	// subject to the same "plain, relative, canonical" rules ValidateRepoPath
	// already decides for this package (including refusing an empty path),
	// so it is decided there rather than re-derived here. It matters most on
	// the allowOutsideIssueNS branch below, which otherwise accepts any
	// string at all: that override exists to name a canonical project
	// deliberately, not to smuggle in a traversal, an absolute path, or a
	// non-canonical form.
	if err := ValidateRepoPath(forkPath); err != nil {
		return Destination{}, fmt.Errorf("drupalorg: invalid commit destination fork path: %w", err)
	}
	switch {
	case strings.HasPrefix(forkPath, issueNamespacePrefix):
		// A forkPath inside the issue/ namespace must be the one this
		// module/issue pair itself derives, never merely share the prefix:
		// otherwise a caller could pair a fork path for one issue with the
		// branch name (derived above from module/issue alone) belonging to
		// a different one.
		if forkPath != canonical {
			return Destination{}, fmt.Errorf(
				"drupalorg: commit destination %q does not match the fork path for module %q issue %d (%q)",
				forkPath, module, issue, canonical,
			)
		}
	case !allowOutsideIssueNS:
		return Destination{}, fmt.Errorf(
			"drupalorg: refusing commit destination %q: it is outside the %q namespace; "+
				"pass allowOutsideIssueNS=true to override this deliberately",
			forkPath, issueNamespacePrefix,
		)
	}

	if fork == nil || fork.ForkedFromProject == nil {
		return Destination{}, fmt.Errorf("drupalorg: %q has no forked_from_project; cannot derive its canonical parent", forkPath)
	}
	// A forked_from_project that is present but carries no id or path is no
	// more of a resolvable parent than a missing one, and it must fail here
	// for the same reason: a Destination whose ParentID is 0 or whose
	// ParentPath is empty would otherwise be carried all the way to a merge
	// request addressed at nothing, discovered only as an opaque API error
	// after a developer had already confirmed a publish. ParentBranch is
	// deliberately not required — the host decides whether a merge request
	// follows at all, and commits can be replayed onto the fork without one.
	parent := fork.ForkedFromProject
	if parent.ID <= 0 || parent.PathWithNamespace == "" {
		return Destination{}, fmt.Errorf(
			"drupalorg: %q has an unusable forked_from_project (id %d, path %q); cannot derive its canonical parent",
			forkPath, parent.ID, parent.PathWithNamespace,
		)
	}

	return Destination{
		ForkPath:     forkPath,
		Branch:       branch,
		ParentID:     parent.ID,
		ParentPath:   parent.PathWithNamespace,
		ParentBranch: parent.DefaultBranch,
	}, nil
}

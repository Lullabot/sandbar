// reviewskills.go refreshes the browser-review assistant skills inside the
// checkouts a reset just restored.
//
// The base image installs @self-review/serve and stages the skills that match
// it; the project role copies those skills into a fresh clone. A reset that
// restores a checkout instead of cloning it brings back the OLD VM's copies
// next to the NEW base's server, and the skills validate review.xml against an
// XSD they ship themselves — so a version bump between the two can break
// validation for no visible reason. Re-running the guest's own installer over
// the restored trees closes that gap.
//
// It only ever REFRESHES: a checkout is touched only when it already carries an
// untracked .agents/skills/self-review-* copy, which is the installer's own
// footprint. A checkout without one is the user's choice (or predates the
// feature), and installing into it here would be the same "install as a side
// effect" landing deliberately refuses to do.
package provision

import (
	"context"
	"fmt"
	"io"
	"strconv"
)

// reviewSkillsCommand is the guest command the base image puts on PATH
// (roles/self-review). The refresh delegates every per-file rule — skip
// tracked copies, replace stale untracked ones, link .claude/skills, write the
// global excludes — to it rather than restating them here.
const reviewSkillsCommand = "self-review-install-skills"

// How far below a search root a checkout may sit. A whole home holds checkouts
// at ~/<host>/<group>/<repo>, with GitLab subgroups adding levels, so it gets
// room for a couple of those. A restored project or picked checkout root is the
// repository parent or the checkout itself, so a short reach is enough.
const (
	reviewSkillsHomeDepth    = 5
	reviewSkillsSubtreeDepth = 3
)

// refreshReviewSkillsScript finds restored checkouts carrying sandbar-installed
// review skills and re-runs the installer in each. It is a LITERAL: the home,
// the depth and every root arrive as positional arguments ($1, $2, "$@"), so a
// path from the plan — some of which came from sweeping the old guest — is
// never parsed as shell text.
//
// The search is bounded and never follows a symlink: find runs in its default
// -P mode, a symlinked root is skipped outright, and a candidate whose .agents
// or .agents/skills is a symlink, or whose real path leaves the home, is
// ignored. Caches, Git directories and node_modules are pruned. A checkout
// nested inside one that already qualified (a linked worktree under .claude/worktrees,
// say) is dropped: the outer checkout is the one the skills were installed for.
//
// A checkout qualifies only when at least one self-review-* copy is a real
// directory Git does not track; a repository that vendors its own copies is
// left alone, as the installer itself would.
//
// The installer's per-skill chatter is discarded; one short line per refreshed
// checkout is printed instead. A failure in one checkout does not stop the
// others, and the script's status reports whether any failed.
const refreshReviewSkillsScript = `set -u
home=$1
depth=$2
shift 2
command -v self-review-install-skills >/dev/null 2>&1 || exit 0
home_real=$(cd -P -- "$home" 2>/dev/null && pwd) || exit 0

candidates=()
for rel in "$@"; do
  case "/$rel/" in */../*) continue ;; esac
  case "$rel" in /*|'') continue ;; esac
  if [ "$rel" = . ]; then root=$home; else root=$home/$rel; fi
  [ -L "$root" ] && continue
  [ -d "$root" ] || continue
  while IFS= read -r -d '' skill; do
    candidates+=("${skill%/.agents/skills/*}")
  done < <(find "$root" -mindepth 1 -maxdepth "$((depth + 3))" \
    \( -name .cache -o -name .git -o -name node_modules \) -prune \
    -o -type d -path '*/.agents/skills/self-review-*' -print0 -prune 2>/dev/null)
done
[ "${#candidates[@]}" -gt 0 ] || exit 0

failed=0
kept=()
while IFS= read -r -d '' top; do
  nested=0
  for k in "${kept[@]+"${kept[@]}"}"; do
    case "$top/" in "$k"/*) nested=1; break ;; esac
  done
  [ "$nested" = 0 ] || continue

  [ -L "$top/.agents" ] || [ -L "$top/.agents/skills" ] && continue
  top_real=$(cd -P -- "$top" 2>/dev/null && pwd) || continue
  case "$top_real/" in "$home_real"/*) ;; *) continue ;; esac
  git_top=$(git -C "$top_real" rev-parse --show-toplevel 2>/dev/null) || continue
  [ "$git_top" = "$top_real" ] || continue

  ours=0
  for s in "$top_real"/.agents/skills/self-review-*; do
    [ -d "$s" ] && [ ! -L "$s" ] || continue
    if ! git -C "$top_real" ls-files --error-unmatch -- ".agents/skills/${s##*/}" >/dev/null 2>&1; then
      ours=1
      break
    fi
  done
  [ "$ours" = 1 ] || continue
  kept+=("$top")

  shown="~/${top_real#"$home_real"/}"
  if msg=$(self-review-install-skills "$top_real" 2>&1 >/dev/null); then
    printf 'skills refreshed: %s\n' "$shown"
  else
    printf 'skills not refreshed: %s: %s\n' "$shown" "$msg" >&2
    failed=1
  fi
done < <(printf '%s\0' "${candidates[@]}" | sort -z -u)
exit "$failed"`

// reviewSkillRoots returns the home-relative roots to search for restored
// checkouts, and how deep below them a checkout may sit, for one reset's plan.
// A whole-home restore searches the home itself; otherwise the roots are the
// restored project directory and the hand-picked checkouts. A plan that
// restored no working tree returns no roots.
func reviewSkillRoots(plan PreservePlan) ([]string, int) {
	if plan.WholeHome {
		return []string{"."}, reviewSkillsHomeDepth
	}
	var roots []string
	if plan.Project.Staged && plan.Project.OrgRel != "" {
		roots = append(roots, plan.Project.OrgRel)
	}
	roots = append(roots, plan.Extras...)
	return roots, reviewSkillsSubtreeDepth
}

// RefreshReviewSkills re-runs the guest's review-skill installer over the
// checkouts plan restored, as user with HOME set to home (the installer also
// writes the user's global Git excludes file).
//
// It never fails the reset. By the time it runs, the user's data is back in a
// VM that works, and the old VM is gone — failing here would strand a
// perfectly good restore behind an error about an optional adapter. A failure
// is a warning naming the command to run by hand instead.
func RefreshReviewSkills(ctx context.Context, cli guestRunner, name, home, user string, plan PreservePlan, out io.Writer) {
	roots, depth := reviewSkillRoots(plan)
	if len(roots) == 0 {
		return
	}
	if out == nil {
		out = io.Discard
	}
	step(out, "Refreshing review skills…")
	argv := []string{
		"sudo", "-u", user, "--", "env", "HOME=" + home,
		"bash", "-c", refreshReviewSkillsScript, "sand-review-skills",
		home, strconv.Itoa(depth),
	}
	argv = append(argv, roots...)
	if err := cli.Shell(ctx, name, nil, out, argv...); err != nil {
		// The headline stays inside the tile's width; the detail and the fix go
		// on the lines below it, which the log view shows in full.
		step(out, "Warning: skills not refreshed")
		fmt.Fprintf(out, "%v\nTo refresh by hand, run in the VM: %s <checkout>\n", err, reviewSkillsCommand)
	}
}

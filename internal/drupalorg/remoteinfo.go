package drupalorg

import (
	"fmt"
	"strings"
)

// remoteInfoScript resolves, inside the guest, the two facts publication
// needs before it can name a destination: the targeted checkout's remote URL
// (which ModuleFromRemoteURL turns into a module name) and the branch's
// upstream tracking ref (which BuildCollectCommand takes as its base).
//
// It lives here, beside BuildCollectCommand and ParseCollect, rather than in
// either surface. Both `sand publish` and the Landing pane's publish action
// need exactly this step, and the plan requires resolution logic to live once
// rather than be implemented twice — when it was written per-surface the two
// copies had already drifted apart in their error text before either shipped.
//
// The upstream lookup falls back from `@{u}` to `refs/remotes/$r/$b` when the
// branch has no configured upstream, because that is the same rule the
// checkout sweep already decides pushed/unpushed by (see sweep.go's
// "tracking-ref rule is load-bearing" comment: `git push origin HEAD` without
// -u updates the tracking ref but never sets branch.<b>.merge). Without the
// fallback, such a branch — which the pane has already classified as pushed,
// and therefore offered publication for — dies here on `@{u}`'s "fatal: no
// upstream configured", which `set -e` reduces to an opaque exit status.
//
// Its diagnostics carry no surface name, deliberately: the guest cannot know
// whether a CLI or a TUI asked, and each caller wraps the returned error in
// its own idiom.
const remoteInfoScript = `set -e
b=$(git symbolic-ref --short HEAD)
r=$(git config --get "branch.$b.remote") || true
[ -n "$r" ] || r=$(git remote | head -n1)
if [ -z "$r" ]; then
  echo "this checkout has no remote configured" >&2
  exit 1
fi
u=$(git rev-parse --abbrev-ref --symbolic-full-name "@{u}" 2>/dev/null) || u=""
if [ -z "$u" ] && git rev-parse --verify --quiet "refs/remotes/$r/$b" >/dev/null; then
  u="$r/$b"
fi
if [ -z "$u" ]; then
  echo "this checkout's branch has no upstream and no remote-tracking ref to compare against" >&2
  exit 1
fi
git remote get-url "$r"
printf '%s\n' "$u"
`

// BuildRemoteInfoCommand returns the guest-side shell expression that prints
// the targeted checkout's remote URL and upstream tracking ref, one per line.
// Run it through the provider's guest-exec path with the checkout as the
// working directory, and hand its stdout to ParseRemoteInfo.
//
// Like BuildCollectCommand's script, every git read it performs is local and
// read-only — no fetch, no push, no credential. See collect.go's "No network,
// ever" note.
func BuildRemoteInfoCommand() string { return remoteInfoScript }

// ParseRemoteInfo extracts the two lines BuildRemoteInfoCommand's script
// prints. It is pure — no exec, no VM — so the parsing is unit-testable on
// its own, separately from the subprocess plumbing around it, exactly as
// ParseCollect is.
func ParseRemoteInfo(out []byte) (remoteURL, upstream string, err error) {
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 2 || strings.TrimSpace(lines[0]) == "" || strings.TrimSpace(lines[1]) == "" {
		return "", "", fmt.Errorf("unexpected output resolving the checkout's remote and upstream branch: %q", string(out))
	}
	return strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1]), nil
}

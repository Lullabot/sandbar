package provision

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/lullabot/sandbar/internal/lima"
)

// forgeWiring describes how one recognized token env-var KEY wires into git's
// credential subsystem.
type forgeWiring struct{ host, user string }

// recognizedForgeTokens is the ONE "recognized forge tokens" table — the only
// place in sand that knows a forge exists. internal/secrets stores plain
// (scope, KEY, VALUE) triples with no forge concept at all; this table is
// purely a delivery-layer lookup consulted when rendering a scope's secrets
// into the guest. Adding GitLab later is one line:
// "GITLAB_TOKEN": {host: "gitlab.com", user: "oauth2"}.
var recognizedForgeTokens = map[string]forgeWiring{
	"GH_TOKEN": {host: "github.com", user: "x-access-token"},
}

// gitCredSlug turns a scope into the filesystem-safe basename used for its
// per-scope git-credentials/gitconfig.d files: the empty (global) scope slugs
// to "default"; any other scope has every run of non-alphanumeric characters
// collapsed to a single '-' (e.g. "github.com/acme" -> "github-com-acme").
var slugNonAlnumRE = regexp.MustCompile(`[^A-Za-z0-9]+`)

func gitCredSlug(scope string) string {
	if scope == "" {
		return "default"
	}
	return slugNonAlnumRE.ReplaceAllString(scope, "-")
}

// gitCredEntry is one resolved git-credential wiring: a recognized token
// found in a scope, together with the forge host/user it authenticates as and
// the filesystem slug it renders under.
type gitCredEntry struct {
	scope, slug, host, user, token string
}

// collectGitCredEntries walks scopes (as delivered by secrets.Store.GetAll)
// and returns one gitCredEntry per (scope, KEY) pair where KEY is in
// recognizedForgeTokens AND scope is non-empty. This redesign's git-credential
// wiring covers the scoped case only — a recognized token in the global ("")
// scope is still delivered as a plain env var (see ApplySecrets/task 03) but
// does not additionally wire a VM-wide git credential helper. A KEY that is
// not in recognizedForgeTokens produces no entry at all, regardless of scope.
//
// The result is sorted first by scope then by KEY, so repeated calls over an
// equal map produce an identical sequence of guest calls (map iteration order
// is otherwise random in Go).
func collectGitCredEntries(scopes map[string]map[string]string) []gitCredEntry {
	scopeNames := make([]string, 0, len(scopes))
	for scope, pairs := range scopes {
		if scope == "" || len(pairs) == 0 {
			continue
		}
		scopeNames = append(scopeNames, scope)
	}
	sort.Strings(scopeNames)

	forgeKeys := make([]string, 0, len(recognizedForgeTokens))
	for k := range recognizedForgeTokens {
		forgeKeys = append(forgeKeys, k)
	}
	sort.Strings(forgeKeys)

	var entries []gitCredEntry
	for _, scope := range scopeNames {
		pairs := scopes[scope]
		for _, key := range forgeKeys {
			token, ok := pairs[key]
			if !ok {
				continue
			}
			w := recognizedForgeTokens[key]
			entries = append(entries, gitCredEntry{
				scope: scope,
				slug:  gitCredSlug(scope),
				host:  w.host,
				user:  w.user,
				token: token,
			})
		}
	}
	return entries
}

// renderGitCredentialLine renders the git-credential-store body for e, ported
// verbatim from main's proven format: "https://<user>:<token>@<host>\n".
func renderGitCredentialLine(e gitCredEntry) string {
	return fmt.Sprintf("https://%s:%s@%s\n", e.user, e.token, e.host)
}

// renderGitconfigInclude renders the scoped gitconfig.d/<slug> include file
// content. homeAbs MUST be an ABSOLUTE path (resolved in-guest via $HOME, not
// a host-side guess — see gitCredWriteScript). credential.helper's --file=
// argument is a plain shell word, not a git config path value, so git does
// NOT tilde-expand it: main proved empirically that `store --file=~/...`
// silently resolves to nothing. This is the opposite rule from includeIf's
// gitdir:/path=, which ARE tilde-expanded (see renderGitconfigManagedBlock).
func renderGitconfigInclude(homeAbs, slug string) string {
	return fmt.Sprintf("[credential]\n\thelper = store --file=%s/.config/sandbar/git-credentials/%s\n", homeAbs, slug)
}

// renderGitconfigManagedBlock renders the full contents of the marker-
// delimited managed block written into ~/.gitconfig: one
// `[includeIf "gitdir:~/<scope>/"]` stanza per scoped entry, followed by an
// unconditional default `[credential] helper` line for any entry with an
// empty scope. includeIf's gitdir:/path= values ARE tilde-expanded by git
// (unlike credential.helper's --file=, see renderGitconfigInclude above), so
// "~/" is correct here.
//
// Ordering is load-bearing: git's credential subsystem tries configured
// helpers in the order they appear in the resolved config and stops at the
// FIRST one that returns a full username+password — it does NOT prefer a
// more specific helper found later. Every scoped includeIf stanza is
// therefore emitted before any unconditional default helper, so a scope-
// specific credential is always reached before a VM-wide default could
// short-circuit it.
//
// homeAbs is used only by the default-helper branch; collectGitCredEntries
// never produces a scope=="" entry (this redesign wires the scoped case
// only — see its doc comment), so in ApplySecrets's actual call site this
// branch is currently dead code, kept for the renderer's generality and for
// direct unit testing of the ordering property above.
//
// An empty (or all-default-less) entry set renders "", so applying it clears
// any previously-written block — the reconciliation path a removed token
// relies on to stop authenticating.
func renderGitconfigManagedBlock(entries []gitCredEntry, homeAbs string) string {
	var b strings.Builder
	for _, e := range entries {
		if e.scope == "" {
			continue
		}
		b.WriteString(fmt.Sprintf("[includeIf \"gitdir:~/%s/\"]\n\tpath = ~/.config/sandbar/gitconfig.d/%s\n", e.scope, e.slug))
	}
	for _, e := range entries {
		if e.scope != "" {
			continue
		}
		b.WriteString(fmt.Sprintf("[credential]\n\thelper = store --file=%s/.config/sandbar/git-credentials/%s\n", homeAbs, e.slug))
	}
	return b.String()
}

// gitCredMarkerBegin and gitCredMarkerEnd delimit the managed block in
// ~/.gitconfig, mirroring the marker convention already used for
// ~/.profile/~/.bashrc in ensureProfileSourceScript, so the block can be
// regenerated or cleared without disturbing any of the user's own gitconfig
// content outside it.
const (
	gitCredMarkerBegin = "# BEGIN sandbar git-credentials"
	gitCredMarkerEnd   = "# END sandbar git-credentials"
)

// gitCredWriteScript builds the in-guest script that writes one scope's
// git-credentials file (0600, body over stdin — the token must never reach
// argv) and its paired gitconfig.d include file (0644, non-secret, rendered
// directly in-script). The include's --file= is resolved via $HOME INSIDE the
// guest ("slug" is interpolated after single-quoting, defense in depth beyond
// the already-safe [A-Za-z0-9-]+ charset gitCredSlug produces), never a
// host-side guess, per renderGitconfigInclude's doc comment.
func gitCredWriteScript(slug string) string {
	return `set -eu -o pipefail
credDir="$HOME/.config/sandbar/git-credentials"
incDir="$HOME/.config/sandbar/gitconfig.d"
install -d -m 700 "$credDir"
install -d -m 755 "$incDir"
slug=` + shellSingleQuote(slug) + `
credFile="$credDir/$slug"
incFile="$incDir/$slug"
install -m 600 /dev/null "$credFile"
cat > "$credFile"
printf '[credential]\n\thelper = store --file=%s/.config/sandbar/git-credentials/%s\n' "$HOME" "$slug" > "$incFile"
chmod 644 "$incFile"
`
}

// gitCredReconcileScript builds the in-guest script that prunes stale
// per-scope credential/include files (any file under git-credentials/ or
// gitconfig.d/ whose basename is not in desiredSlugs, i.e. left over from a
// scope whose recognized token was removed) and regenerates the
// marker-delimited managed block in ~/.gitconfig from the body streamed over
// stdin (renderGitconfigManagedBlock's output — empty clears the block). It
// runs unconditionally on every apply, mirroring main's proven
// "runs unconditionally so removals reconcile" role comment, so a removed
// token's stale on-disk files and stale includeIf stanza do not linger.
func gitCredReconcileScript(desiredSlugs []string) string {
	desired := strings.Join(desiredSlugs, " ")
	return `set -eu -o pipefail
desired=` + shellSingleQuote(desired) + `
is_desired() {
  for have in $desired; do
    [ "$have" = "$1" ] && return 0
  done
  return 1
}
for d in "$HOME/.config/sandbar/git-credentials" "$HOME/.config/sandbar/gitconfig.d"; do
  [ -d "$d" ] || continue
  for f in "$d"/*; do
    [ -e "$f" ] || continue
    b=$(basename "$f")
    if ! is_desired "$b"; then
      rm -f "$f"
    fi
  done
done
begin="` + gitCredMarkerBegin + `"
end="` + gitCredMarkerEnd + `"
gc="$HOME/.gitconfig"
touch "$gc"
tmp=$(mktemp "$gc.sandbar.XXXXXX")
skip=0
while IFS= read -r line || [ -n "$line" ]; do
  if [ "$line" = "$begin" ]; then skip=1; continue; fi
  if [ "$line" = "$end" ]; then skip=0; continue; fi
  [ "$skip" -eq 1 ] && continue
  printf '%s\n' "$line" >> "$tmp"
done < "$gc"
body=$(cat)
if [ -n "$body" ]; then
  printf '%s\n' "$begin" >> "$tmp"
  printf '%s\n' "$body" >> "$tmp"
  printf '%s\n' "$end" >> "$tmp"
fi
mv "$tmp" "$gc"
`
}

// applyGitCredEntries streams each entry's git-credentials file (token over
// stdin) plus its paired gitconfig.d include, then runs the reconcile pass
// unconditionally so scopes no longer holding a recognized token are pruned
// and the managed ~/.gitconfig block is regenerated (or cleared) from the
// current set. Called by ApplySecrets after the plain scoped env-var delivery
// (task 03), which is unaffected — a recognized token is still delivered as a
// plain env var in addition to this wiring.
func applyGitCredEntries(ctx context.Context, cli *lima.Client, name, user string, entries []gitCredEntry, out io.Writer) error {
	for _, e := range entries {
		body := renderGitCredentialLine(e)
		if err := cli.Shell(ctx, name, strings.NewReader(body), out, "sudo", "-H", "-u", user, "bash", "-c", gitCredWriteScript(e.slug)); err != nil {
			return fmt.Errorf("apply git credential for scope %q: %w", e.scope, err)
		}
	}

	slugs := make([]string, 0, len(entries))
	for _, e := range entries {
		slugs = append(slugs, e.slug)
	}
	// homeAbs is unused here: entries never contains a scope=="" (default)
	// entry (see collectGitCredEntries), so renderGitconfigManagedBlock's
	// default-helper branch never fires for this call — see its doc comment.
	blockBody := renderGitconfigManagedBlock(entries, "")
	var stdin io.Reader
	if blockBody != "" {
		stdin = strings.NewReader(blockBody)
	}
	if err := cli.Shell(ctx, name, stdin, out, "sudo", "-H", "-u", user, "bash", "-c", gitCredReconcileScript(slugs)); err != nil {
		return fmt.Errorf("reconcile git credential wiring: %w", err)
	}
	return nil
}

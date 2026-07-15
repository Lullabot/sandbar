package provision

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/lullabot/sandbar/internal/secrets"
)

// guestRunner is the narrow backend capability the free provision helpers in
// this file (and staging.go, gitcred.go) need: running a guest command with
// its output handled one of three ways. It is satisfied structurally by both
// *lima.Client (the provisioner's own core, which the provisioner passes
// directly) and provider.Provider (which app-level consumers — e.g.
// internal/ui's ApplySecrets call sites — hold instead of the concrete lima
// type), so neither side needs to import the other's package. Deliberately
// unexported: callers never name the type, they just pass a value wide
// enough to satisfy it.
type guestRunner interface {
	Shell(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error
	ShellStreamOut(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error
	ShellOut(ctx context.Context, name string, argv ...string) ([]byte, error)
}

// applySecretsScript writes the streamed env file into the guest with the same
// in-guest hygiene as inGuestScript: the target file is created 0600 (via
// `install -m 600 /dev/null`) BEFORE any secret bytes are written to it, so there
// is never an instant at which a world-readable file holds a secret. The rendered
// body arrives over STDIN (cat), never argv — a secret must not appear in a host
// `ps` listing. The enclosing `sudo -H -u <user>` runs this as the target user
// with HOME set to their home, so the created dir/file are owned by them.
const applySecretsScript = `set -eu -o pipefail
d="$HOME/.config/sandbar"
f="$d/secrets.env"
install -d -m 700 "$d"
install -m 600 /dev/null "$f"
cat > "$f"
`

// clearSecretsScript removes the guest env file. It is used when a VM has no
// global secrets, so a file written by a previous apply does not linger with
// stale values.  `rm -f` is a no-op when the file is already absent.
const clearSecretsScript = `set -eu -o pipefail
rm -f "$HOME/.config/sandbar/secrets.env"
`

// ensureProfileSourceScript idempotently (re)establishes the lines that source
// ~/.config/sandbar/secrets.env from BOTH ~/.profile (login shells) and
// ~/.bashrc (interactive non-login shells) — both are required because a
// guest shell reached via `limactl shell`/ssh is not necessarily either one
// exclusively (see the lima-shell-stderr-and-env-profile memory). This is
// pure, static script text with no secret or user-controlled interpolation —
// unlike applySecretsScript it needs no stdin and is safe entirely on argv. A
// grep-guarded marker keeps repeated applies a no-op, mirroring the intent of
// the Ansible role's blockinfile tasks (roles/user/tasks/main.yml) without
// depending on Ansible having (re)run recently: the block is appended only
// when the marker line is absent.
const ensureProfileSourceScript = `set -eu -o pipefail
begin="# BEGIN sandbar secrets"
end="# END sandbar secrets"
for f in "$HOME/.bashrc" "$HOME/.profile"; do
  touch "$f"
  if ! grep -qF "$begin" "$f"; then
    {
      printf '%s\n' "$begin"
      printf '%s\n' 'if [ -f "$HOME/.config/sandbar/secrets.env" ]; then'
      printf '%s\n' '    . "$HOME/.config/sandbar/secrets.env"'
      printf '%s\n' 'fi'
      printf '%s\n' "$end"
    } >> "$f"
  fi
done
`

// shellSingleQuote wraps s in POSIX single quotes, escaping any embedded
// quote as the four-byte sequence quote/backslash/quote/quote (close the
// quoted span, emit one escaped literal quote, reopen the span). It is a
// local copy of the identical technique in internal/secrets.Render
// (unexported there, and internal/secrets is frozen for this task); it is
// reused here for two purposes: rendering a scoped .env's values (see
// RenderDotenv) and, as defense in depth, quoting a scope name before it is
// interpolated into an in-guest script — even though secrets.ValidScope
// already restricts a scope to safe characters with no quote or shell
// metacharacter.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// RenderDotenv emits one scope's ~/<scope>/.env body in direnv's dotenv
// KEY=VALUE form: one line per pair, keys sorted ascending for byte-stable
// output, each value wrapped in single quotes — dotenv's no-expansion
// quoting, so a value containing a quote, a `$(…)`, or a backtick reaches the
// guest completely literally, mirroring secrets.Render's `export KEY='VALUE'`
// escaping but without the `export` keyword (direnv's dotenv loader is not a
// POSIX `source`, so the two destinations need different syntax even though
// they share the same escaping technique). Keys that are not
// secrets.ValidKey are skipped — Store.SetAll already rejects them before
// persistence, and this is the second line of defense, matching Render.
func RenderDotenv(pairs map[string]string) string {
	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		if !secrets.ValidKey(k) {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + shellSingleQuote(pairs[k]) + "\n")
	}
	return b.String()
}

// scopeEnvScript builds the in-guest script that writes one scope's
// ~/<scope>/.env from stdin. scope is single-quoted before interpolation
// (see shellSingleQuote). The directory is created 0755, not 0700: it may
// hold a real project checkout (matching main's project role, which used
// 0755 for ~/<scope>). The file itself is created 0600 via
// `install -m 600 /dev/null` BEFORE any bytes are written — the same
// create-before-write hygiene as applySecretsScript — and the body arrives
// over stdin (cat), never argv.
func scopeEnvScript(scope string) string {
	return fmt.Sprintf(`set -eu -o pipefail
d="$HOME"/%s
install -d -m 755 "$d"
install -m 600 /dev/null "$d/.env"
cat > "$d/.env"
`, shellSingleQuote(scope))
}

// ApplySecrets converges the guest to the VM's full scope map, as returned by
// secrets.Store.GetAll: the global scope ("") renders into
// ~/.config/sandbar/secrets.env (sourced from both ~/.profile and ~/.bashrc),
// and each non-empty scope renders into ~/<scope>/.env, approved with
// `direnv allow`. Every rendered body is streamed over STDIN only — never
// argv — so no secret ever appears in a host process listing, and every
// multi-line script runs as `sudo -H -u <user>` — NEVER `sudo -i`, which runs
// the target's *login shell*, re-parsing the command so a multi-line script
// arrives mangled ("set: -c: invalid option") and the write silently fails
// (verified against a live guest). The single-word `direnv allow` may use
// `sudo -iu`, matching the existing reset path in provision.go.
//
// Reconciliation: the global scope's guest location is fixed
// (~/.config/sandbar/secrets.env), so it is always exactly converged from the
// current map alone — written when present, cleared via clearSecretsScript
// when absent/empty. This covers "clearing the whole store" for the global
// scope. A SCOPE's guest location, by contrast, is named by the scope string
// itself: once a scope is dropped from the store, its name — and therefore
// its ~/<scope>/.env path — is no longer known to this function, so a
// dropped scope's stale .env is NOT removed here. Doing so would require
// either guest-side state tracking (deliberately out of scope for this
// delivery layer — see plan 12 task 03's implementation notes) or an unsafe
// enumeration of the guest's home tree that risks touching a legitimate,
// unrelated project .env file. Every apply DOES write-or-refresh every scope
// currently present in the map, so this reduces to: a scope's guest file
// lags one apply behind its removal from the store. This limitation is
// shared with task 04's forge-credential pruning.
func ApplySecrets(ctx context.Context, cli guestRunner, name, user string, scopes map[string]map[string]string, out io.Writer) error {
	global := scopes[""]
	if len(global) == 0 {
		if err := cli.Shell(ctx, name, nil, out, "sudo", "-H", "-u", user, "bash", "-c", clearSecretsScript); err != nil {
			return fmt.Errorf("clear global secrets: %w", err)
		}
	} else {
		body := secrets.Render(global)
		if err := cli.Shell(ctx, name, strings.NewReader(body), out, "sudo", "-H", "-u", user, "bash", "-c", applySecretsScript); err != nil {
			return fmt.Errorf("apply global secrets: %w", err)
		}
	}

	if err := cli.Shell(ctx, name, nil, out, "sudo", "-H", "-u", user, "bash", "-c", ensureProfileSourceScript); err != nil {
		return fmt.Errorf("ensure secrets are sourced from ~/.profile and ~/.bashrc: %w", err)
	}

	// Collect the non-empty scopes deterministically (map iteration order is
	// random in Go) so repeated applies of the same store make the same
	// sequence of guest calls.
	scopeNames := make([]string, 0, len(scopes))
	for scope, pairs := range scopes {
		if scope == "" || len(pairs) == 0 {
			continue
		}
		scopeNames = append(scopeNames, scope)
	}
	if len(scopeNames) > 0 {
		sort.Strings(scopeNames)

		// direnv allow needs an absolute guest path, so resolve $HOME once,
		// host-side, rather than per scope.
		home, err := guestHome(ctx, cli, name, user)
		if err != nil {
			return fmt.Errorf("resolve guest home for %q: %w", user, err)
		}

		for _, scope := range scopeNames {
			body := RenderDotenv(scopes[scope])
			if err := cli.Shell(ctx, name, strings.NewReader(body), out, "sudo", "-H", "-u", user, "bash", "-c", scopeEnvScript(scope)); err != nil {
				return fmt.Errorf("apply scoped secrets ~/%s/.env: %w", scope, err)
			}
			if err := cli.Shell(ctx, name, nil, out, "sudo", "-iu", user, "direnv", "allow", home+"/"+scope); err != nil {
				return fmt.Errorf("direnv allow ~/%s: %w", scope, err)
			}
		}
	}

	// Git-credential wiring (task 04): for each recognized forge token
	// (see recognizedForgeTokens in gitcred.go — the ONLY place in sand that
	// knows a forge exists) found in a non-empty scope, additionally render
	// main's proven per-scope git-credentials + gitconfig.d include +
	// includeIf "gitdir:~/<scope>/" wiring. This runs unconditionally
	// (even when there are zero recognized tokens) so a removed token's
	// stale on-disk files and ~/.gitconfig stanza are reconciled away.
	entries := collectGitCredEntries(scopes)
	if err := applyGitCredEntries(ctx, cli, name, user, entries, out); err != nil {
		return err
	}
	return nil
}

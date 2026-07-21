// sweep.go is the pure, host-side half of the detection sweep: it builds the
// guest command a sweep shell runs, and it parses that command's output back
// into checkout rows. It knows nothing about how the command actually gets to
// a guest — no limactl, no Bubble Tea, no goroutines or timers. That wiring
// belongs to internal/ui/sweepshell.go (the long-lived sweep shell, a sibling
// of the heartbeat) and cmd/sand/land.go (the headless `sand land` one-shot
// sweep); both call BuildSweepCommand and ParseSweep so the detection and
// classification logic is written, and tested, exactly once.
//
// # Mirroring the heartbeat's "deliberately dumb guest side"
//
// internal/ui/heartbeat.go's guestScript is a `cat`/`df`/`sleep` loop on
// purpose: "a clever guest script is a thing that breaks on a distro nobody
// tested." The sweep command below follows the same philosophy — a plain
// `find` plus a handful of read-only `git` reads, no bespoke guest program —
// and pushes every interesting decision (worktree parent linkage, push-state
// classification, remote URL parsing) into Go, where it can be unit tested
// against captured/synthetic text instead of a real VM.
//
// # Two streams, two delimiters
//
// The sweep runs on its own long-lived shell, a sibling of the heartbeat's,
// so the two streams are never mixed on the wire — but sweepRecordDelim is
// still deliberately distinct from heartbeatDelim so a host-side bug that
// ever read the wrong stream (or a future refactor that merges parsers) fails
// loudly on a delimiter mismatch instead of silently interleaving stats and
// checkout records.
//
// # No network, ever
//
// Every git read here is local: `symbolic-ref`, `config --get`, `remote`,
// `remote get-url`, `rev-parse`, `rev-list --count`, `status --porcelain`.
// None of them contacts the forge. Push state is derived from the LOCAL
// remote-tracking ref, which a prior `git push` already updated — never from
// `git ls-remote` or any other network round-trip. Remote truth is confirmed
// host-side, later, by the `gh pr list` check (internal/landgh, driven by the
// Landing pane), not by the guest.
package checkouts

import (
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	// sweepMaxDepth bounds how far `find` descends from the guest's $HOME.
	// Six levels reaches the common "~/src/org/repo" and "~/work/client/app"
	// shapes without wandering into arbitrarily deep unrelated trees.
	sweepMaxDepth = 6

	// sweepMaxCheckouts caps how many discovered `.git` entries are processed
	// in one sweep. ~50 is generous for a real developer's guest home while
	// keeping one sweep pass (each entry timeout-wrapped) bounded in wall
	// time. A guest that legitimately has more sees the rest on account of
	// the Truncated flag, never a silent drop.
	sweepMaxCheckouts = 50

	// sweepPerRepoTimeout is the `timeout` (seconds) wrapped around every
	// read-only git invocation for a single checkout, so one pathological
	// repo (a huge status walk, a wedged filesystem) cannot stall the rest of
	// the sweep, matching the heartbeat's own "nothing may block the loop"
	// discipline.
	sweepPerRepoTimeout = 5

	// sweepRecordDelim ends one checkout's record in the guest sweep stream.
	// Deliberately distinct from heartbeat.go's heartbeatDelim: the two
	// streams run on separate shells/connections and must never be
	// confusable by a host-side parser.
	sweepRecordDelim = "---sand-sweep-record---"

	// sweepTruncatedMarker is emitted as its own line, once, when the
	// discovered-checkout count exceeded sweepMaxCheckouts and the guest cut
	// the list down to the cap — so truncation is a flag ParseSweep surfaces,
	// never a fact silently dropped along with the entries past the cap.
	sweepTruncatedMarker = "SWEEP_TRUNCATED"
)

// sweepScriptTemplate is the guest-side sweep command, before its tunable
// constants are substituted in by BuildSweepCommand. It is intentionally
// small:
//
//  1. A bounded `find` from $HOME for `.git` entries — matching BOTH
//     directories (ordinary checkouts) and files (worktree pointers) —
//     pruning common noise directories so the walk doesn't wander into
//     dependency trees, and capped at sweepMaxCheckouts total.
//  2. For each entry, a `g` helper (`timeout N git --no-optional-locks -C
//     "$dir"`) reads: the checked-out branch, the branch's configured remote
//     (falling back to the first configured remote — never assuming
//     "origin"), that remote's URL, whether a remote-tracking ref exists for
//     (remote, branch) and, if so, the ahead/behind counts against it, and
//     the dirty (uncommitted) file count. A worktree's `.git` FILE is `cat`
//     and its `gitdir: ` pointer passed through raw — Go, not the shell,
//     resolves the parent repo path from it (see parentFromGitdirPointer),
//     which is what makes that logic unit-testable against synthetic text.
//  3. Every field is emitted as a `key=value` line, one record per checkout,
//     terminated by sweepRecordDelim.
//
// __TOKENS__ are substituted by BuildSweepCommand via strings.Replacer, not
// fmt.Sprintf, so the shell's own `%s` in its printf format strings needs no
// escaping.
const sweepScriptTemplate = `set -f
g() { timeout __TIMEOUT__ git --no-optional-locks -C "$dir" "$@" 2>/dev/null; }
found=$(find "$HOME" -maxdepth __DEPTH__ \( -name node_modules -o -name .cache -o -name .cargo -o -name .npm \) -prune -o -name .git -print 2>/dev/null)
count=$(printf '%s\n' "$found" | grep -c .)
if [ "$count" -gt __CAP__ ]; then
  echo "__TRUNC__"
  found=$(printf '%s\n' "$found" | head -n __CAP__)
fi
printf '%s\n' "$found" | while IFS= read -r gitpath; do
  [ -z "$gitpath" ] && continue
  dir=$(dirname "$gitpath")
  gitdirptr=
  if [ -d "$gitpath" ]; then
    kind=repo
  else
    kind=worktree
    gitdirptr=$(cat "$gitpath" 2>/dev/null)
    gitdirptr=${gitdirptr#gitdir: }
  fi
  branch=$(g symbolic-ref --short HEAD)
  remote=$(g config --get "branch.$branch.remote")
  if [ -z "$remote" ]; then
    remote=$(g remote | head -n 1)
  fi
  url=
  if [ -n "$remote" ]; then
    url=$(g remote get-url "$remote")
  fi
  tracking=0
  ahead=0
  behind=0
  if [ -n "$remote" ] && [ -n "$branch" ] && g rev-parse --verify --quiet "refs/remotes/$remote/$branch" >/dev/null; then
    tracking=1
    ahead=$(g rev-list --count "refs/remotes/$remote/$branch..HEAD")
    behind=$(g rev-list --count "HEAD..refs/remotes/$remote/$branch")
  fi
  defbranch=
  if [ -n "$remote" ]; then
    defbranch=$(g symbolic-ref --short "refs/remotes/$remote/HEAD")
    defbranch=${defbranch#"$remote/"}
  fi
  dirty=$(g status --porcelain | grep -c .)
  printf 'path=%s\nkind=%s\ngitdirptr=%s\nbranch=%s\nremote=%s\nurl=%s\ntracking=%s\nahead=%s\nbehind=%s\ndirty=%s\ndefbranch=%s\n__DELIM__\n' \
    "$dir" "$kind" "$gitdirptr" "$branch" "$remote" "$url" "$tracking" "$ahead" "$behind" "$dirty" "$defbranch"
done
`

// BuildSweepCommand returns the single shell command a sweep shell runs
// against a guest: a bounded, read-only `find` + per-checkout `git` reads,
// exactly as documented on sweepScriptTemplate. It takes no arguments and
// performs no I/O itself — the TUI's sweep shell wraps it in its own
// long-lived `limactl shell` + ~60s loop, and `sand land` runs it once for a
// headless one-shot sweep; both feed its stdout to ParseSweep.
func BuildSweepCommand() string {
	r := strings.NewReplacer(
		"__DEPTH__", strconv.Itoa(sweepMaxDepth),
		"__CAP__", strconv.Itoa(sweepMaxCheckouts),
		"__TIMEOUT__", strconv.Itoa(sweepPerRepoTimeout),
		"__DELIM__", sweepRecordDelim,
		"__TRUNC__", sweepTruncatedMarker,
	)
	return r.Replace(sweepScriptTemplate)
}

// sweepFieldKeys is the set of `key=value` field names ParseSweep recognizes.
// Anything else on a line — a motd banner, a login shell's profile output,
// `limactl shell`'s own noise — is ignored rather than misparsed, mirroring
// the heartbeat parser's "anything unrecognized is noise, not an error"
// tolerance for a stream that runs through a real login shell.
var sweepFieldKeys = map[string]bool{
	"path": true, "kind": true, "gitdirptr": true, "branch": true,
	"remote": true, "url": true, "tracking": true, "ahead": true,
	"behind": true, "dirty": true, "defbranch": true,
}

// ParseSweep converts one sweep's raw guest output into a VMCheckouts: one
// Checkout per record (delimited by sweepRecordDelim), each classified by
// Kind, worktree Parent linkage, remote Forge/OrgRepo, and PushState — plus
// the Truncated flag when the guest's own cap trimmed the result. It is a
// pure function of raw: no clock dependency beyond stamping LastSeen/SweptAt
// with the moment of parsing (there is nothing else honest to stamp them
// with), which is what lets it be driven entirely by synthetic/captured text
// in tests, no guest or VM required.
func ParseSweep(raw string) VMCheckouts {
	now := time.Now()

	var result VMCheckouts
	rec := map[string]string{}

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)

		switch trimmed {
		case sweepRecordDelim:
			if rec["path"] != "" {
				result.Checkouts = append(result.Checkouts, checkoutFromRecord(rec, now))
			}
			rec = map[string]string{}
			continue
		case sweepTruncatedMarker:
			result.Truncated = true
			continue
		case "":
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok || !sweepFieldKeys[key] {
			continue // noise: not a recognized key=value field line
		}
		rec[key] = value
	}

	result.SweptAt = now
	return result
}

// checkoutFromRecord turns one fully-collected key=value record into a
// Checkout, applying every classification rule the sweep exists for.
func checkoutFromRecord(rec map[string]string, now time.Time) Checkout {
	kind := KindRepo
	if rec["kind"] == "worktree" {
		kind = KindWorktree
	}

	parent := ""
	if kind == KindWorktree {
		parent = parentFromGitdirPointer(rec["gitdirptr"])
	}

	forge, orgRepo := parseRemoteURL(rec["url"])

	tracking := atoiOr(rec["tracking"], 0)
	ahead := atoiOr(rec["ahead"], 0)
	behind := atoiOr(rec["behind"], 0)

	// The tracking-ref rule is load-bearing: pushed vs unpushed vs never is
	// decided ENTIRELY by whether a remote-tracking ref exists and how far
	// HEAD is ahead of it — never by the branch's configured upstream
	// (branch.<b>.merge), which a `git push origin HEAD` without `-u` never
	// sets even though it updates the tracking ref. This function never even
	// looks at upstream config; it only sees what the guest already resolved
	// from the tracking ref, so a -u-less push is indistinguishable here from
	// an -u'd one, which is the point.
	var state PushState
	switch {
	case tracking == 0:
		state = PushStateNever
		ahead, behind = 0, 0
	case ahead == 0:
		state = PushStatePushed
	default:
		state = PushStateUnpushed
	}

	return Checkout{
		Path:          rec["path"],
		Kind:          kind,
		Parent:        parent,
		Branch:        rec["branch"],
		Forge:         forge,
		OrgRepo:       orgRepo,
		PushState:     state,
		Ahead:         ahead,
		Behind:        behind,
		Dirty:         atoiOr(rec["dirty"], 0),
		DefaultBranch: rec["defbranch"],
		LastSeen:      now,
	}
}

// parentFromGitdirPointer resolves a linked worktree's parent repo path from
// the raw content of its `.git` FILE (the "gitdir: " prefix already stripped
// by the guest script) — e.g.
// "/home/user/proj/.git/worktrees/feature-x" -> "/home/user/proj". `git
// worktree add` always lays a linked worktree's private git-dir under the
// parent's "<parent>/.git/worktrees/<name>", so climbing three path
// components off the pointer recovers the parent unconditionally, with no
// dependence on git-version-specific rev-parse output (relative vs.
// absolute) the way asking git itself would have.
//
// It uses the "path" package, not "path/filepath": sweep text always carries
// POSIX guest paths, regardless of what OS is running `go test`.
func parentFromGitdirPointer(ptr string) string {
	ptr = strings.TrimSpace(ptr)
	if ptr == "" {
		return ""
	}
	ptr = strings.TrimRight(ptr, "/")
	worktrees := path.Dir(ptr)    // .../.git/worktrees
	gitDir := path.Dir(worktrees) // .../.git
	return path.Dir(gitDir)       // .../<repo>
}

// parseRemoteURL parses a git remote URL into (forge host, org/repo slug),
// handling both forms a `git remote get-url` can return:
//
//   - SSH scp-like: "git@github.com:org/repo.git" -> ("github.com", "org/repo")
//   - HTTPS(/HTTP): "https://gitlab.com/group/sub/repo.git" ->
//     ("gitlab.com", "group/sub/repo") — GitLab's nested groups are kept
//     whole, not truncated to the last two path segments.
//   - ssh://[user@]host/org/repo(.git) is also accepted for completeness.
//
// A trailing ".git" is stripped either way. An empty or unrecognized URL
// (no remote configured, or a scheme this never needs to support) yields two
// empty strings — never a guess.
func parseRemoteURL(raw string) (forge, orgRepo string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}

	switch {
	case strings.HasPrefix(raw, "https://"):
		return splitHostPath(strings.TrimPrefix(raw, "https://"))
	case strings.HasPrefix(raw, "http://"):
		return splitHostPath(strings.TrimPrefix(raw, "http://"))
	case strings.HasPrefix(raw, "ssh://"):
		rest := strings.TrimPrefix(raw, "ssh://")
		if i := strings.Index(rest, "@"); i >= 0 {
			rest = rest[i+1:]
		}
		return splitHostPath(rest)
	default:
		// scp-like syntax: [user@]host:path
		rest := raw
		if i := strings.Index(rest, "@"); i >= 0 {
			rest = rest[i+1:]
		}
		i := strings.Index(rest, ":")
		if i < 0 {
			return "", ""
		}
		host := rest[:i]
		p := strings.TrimSuffix(rest[i+1:], ".git")
		p = strings.Trim(p, "/")
		return host, p
	}
}

// splitHostPath splits "host/org/repo(.git)" (an https:// or ssh:// URL with
// its scheme and any userinfo already stripped) into (host, org/repo),
// stripping a trailing ".git" and any stray slashes from the path.
func splitHostPath(rest string) (host, orgRepo string) {
	i := strings.Index(rest, "/")
	if i < 0 {
		return rest, ""
	}
	host = rest[:i]
	p := strings.TrimSuffix(rest[i+1:], ".git")
	p = strings.Trim(p, "/")
	return host, p
}

// atoiOr parses s as a decimal int, returning def if s is empty or not a
// valid integer — the guest's `git`/`grep -c` reads are all read-only and
// should never emit anything but digits, but a timeout-wrapped command that
// hit its timeout produces no output at all, and this must not crash on that.
func atoiOr(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

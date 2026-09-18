// collect.go is the guest/host pair for gathering a change set off a
// developer's checkout: BuildCollectCommand produces the guest-side script,
// and ParseCollect turns that script's captured stdout back into a
// ChangeSet (payload.go). It mirrors internal/checkouts/sweep.go's split
// deliberately: the guest side stays "plain git reads plus base64", and
// every interesting decision — classification, encoding choice, path
// validation, size enforcement — is made here, in Go, where it is testable
// against synthetic captured text instead of a real VM.
//
// # Why this needed its own framing (not the sweep's)
//
// The sweep's stream carries short, single-line, host-trusted-shape fields
// (branch names, remote URLs, small counters): a bare "key=value\n" line per
// field, ended by a record delimiter, is safe because none of those fields
// can contain a newline or collide with the delimiter text.
//
// This stream is different in kind, not just size: a commit message is
// arbitrary multi-line text the guest does not control (an issue
// contributor wrote it), and a file's content may be arbitrary binary bytes
// of any length. Either could, in principle, contain a line equal to
// whatever delimiter this package chose — the exact case the plan calls out
// as this task's unresolved design problem.
//
// The fix is the same trick applied twice, for two different reasons:
//
//  1. Every variable-length, potentially-multiline or binary field (author
//     name, author email, commit message, file paths, file content) is
//     base64-encoded on the wire before it is ever put on its own
//     "key=value" line. Base64's alphabet is `[A-Za-z0-9+/=]` — it contains
//     no "-", so a base64 line can never equal, or even contain, a
//     delimiter built from dashes (collectCommitDelim, collectFileDelim),
//     no matter what bytes it decodes to. That is what makes the framing
//     collision-proof rather than merely "distinct by convention", and it
//     is why the adversarial test below (a commit message containing the
//     delimiter verbatim) passes without sanitising the message.
//  2. Once ParseCollect has decoded those wire-safe base64 bytes back to
//     their real content, EncodeContent (payload.go) is applied to choose
//     the PAYLOAD's encoding: "text" when the decoded bytes are valid UTF-8,
//     so an ordinary patch reads as a normal diff in the confirmation UI,
//     and "base64" otherwise. Wire encoding and payload encoding solve
//     different problems — the wire encoding exists so this parser cannot
//     be confused, the payload encoding exists so GitLab's content API
//     receives what it asked for — and conflating them (e.g. reusing the
//     wire's base64 as the payload's encoding unconditionally) would make
//     every ordinary text patch unreadable in the confirmation the plan
//     promises the user.
//
// # No network, ever
//
// Every git read the guest script runs is local: `rev-list`, `log`,
// `diff-tree`, `cat-file -e`, `cat-file -s`, `show`. None of them contacts a
// remote, and nothing here fetches.
//
// Both base refs the script needs arrive already resolved. forkBase ("what
// the fork branch already holds") is the checkout's own local
// remote-tracking ref, resolved by the caller before this command is built.
// projectBase ("what the canonical project's base branch already carries")
// is a full SHA the HOST read from drupal.org's API — which is the one place
// remote truth enters this flow, and it enters as an argument rather than as
// a fetch. The consequence is that the guest can be asked about a commit it
// does not have, so the script checks for the object before it uses it (step
// 0) instead of letting `rev-list` fail opaquely.
package drupalorg

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	// collectMaxFileBytes is the guest-side per-file cap: BuildCollectCommand's
	// script refuses to emit a file's content once its blob size (read via
	// `git cat-file -s`, checked BEFORE any content is streamed) exceeds
	// this, marking the record `oversize=1` instead. That keeps a single
	// pathological file (a committed binary, a generated asset) from ever
	// producing an unbounded line on the wire, which is exactly the
	// "possibly large" half of this task's design problem. 8 MiB comfortably
	// covers real patch content — source files, small images, generated CSS
	// — while staying well under the pain points of moving a base64 blob
	// through a guest shell pipe.
	collectMaxFileBytes int64 = 8 << 20

	// collectCommitDelim and collectFileDelim are documented in the package
	// doc comment's "Why this needed its own framing" section.
	collectCommitDelim = "---sand-collect-commit---"
	collectFileDelim   = "---sand-collect-file---"
)

// collectMaxTotalBytes is the host-side cap on the sum of a whole change
// set's encoded content (payload.CommitEncodedSize per commit, summed
// incrementally by ParseCollect's flushCommit as each commit completes). It
// is set to GitLab's own content-API rate-limit threshold — documented on
// CommitEncodedSize in payload.go — so a change set this parser accepts
// never even approaches the point GitLab itself would start rate-limiting
// it, let alone the 300 MB hard rejection above that. A var, not a const,
// so tests can shrink it rather than constructing real multi-megabyte
// fixtures.
var collectMaxTotalBytes int64 = 20 << 20

// collectScriptTemplate is the guest-side collection command, before
// BuildCollectCommand substitutes its tokens in. It is intentionally small,
// mirroring sweep.go's sweepScriptTemplate:
//
//  0. `git cat-file -e "<projectBase>^{commit}"` proves the checkout
//     actually holds the canonical project's base-branch tip before
//     anything is enumerated against it. projectBase is a SHA resolved
//     HOST-side from drupal.org's API (Client.BranchTip), so the guest may
//     genuinely not have that object: nothing in this flow fetches, and the
//     issue fork's own copy of the base branch is not auto-synced and in
//     practice never synced by hand — which is exactly why the fork's copy
//     is not what this excludes against. Without this probe the missing
//     object surfaces as `rev-list`'s bare "fatal: bad object", which says
//     nothing about what to do; with it the developer is told to fetch the
//     canonical project and rebase.
//
//  1. `git rev-list --reverse --no-merges HEAD --not "<forkBase>"
//     "<projectBase>"` lists the commits to replay, oldest first — the
//     order a replay must send them in.
//
//     TWO exclusions, not one, and the second is the whole point. Excluding
//     only forkBase (the checkout's own upstream tracking ref — what the
//     fork branch already holds) answers "what has not yet reached the
//     fork", which is NOT the same question as "which commits are this
//     contributor's". The moment the canonical base branch enters the
//     branch's ancestry — a back-merge of the destination branch, or a
//     rebase onto a newer base — every upstream commit it brought along is
//     reachable from HEAD and unreachable from forkBase, so a single
//     exclusion sweeps other people's already-published work into the
//     change set and replays it onto the merge request under the
//     publishing account's name. Excluding the canonical project's base tip
//     as well is what makes the range mean "mine, and not yet published".
//
//     --no-merges stays, but it is no longer load-bearing on its own: a
//     merge commit carries no file changes of its own, so `diff-tree` emits
//     nothing for it and it would arrive here as a commit with zero file
//     actions, which the publisher refuses. Step 1a below now refuses such a
//     range outright and explains why, rather than silently dropping the
//     merge and publishing whatever the two exclusions left behind.
//
//     1a. `git rev-list --merges ...` over the SAME range decides whether this
//     range is publishable at all. The content API lands one commit per call
//     from a list of file actions and has no way to express a second parent,
//     so a merge is not something publication can reproduce — and silently
//     skipping it means publishing a history that never existed. Rather than
//     exit non-zero (whose stdout a captured run may never show a human),
//     the offending SHAs are emitted as ordinary `merge=` field lines and
//     the script stops before collecting anything: ParseCollect sees them
//     and refuses the whole change set with MergeCommitsError. That keeps
//     the decision in Go, where it is testable against synthetic text, which
//     is this file's standing rule.
//
//  2. For each commit, one line per author-name/author-email/message field,
//     each base64-encoded (see the package doc comment for why). Each field
//     is captured into a shell variable BEFORE it is base64-encoded,
//     deliberately: `git log --format=` always appends its own trailing
//     newline after the formatted entry, on top of whatever the field
//     itself contains (a commit message conventionally already ends with
//     one, and — since git does not always strip them — may legitimately
//     end with several). name/email are single-line header fields that can
//     never legitimately carry a trailing newline of their own, so a plain
//     `$(...)` capture (which strips ALL trailing newlines) is exactly
//     right for them. The message is different: it is arbitrary,
//     multi-line, guest-supplied text that must reach drupal.org verbatim
//     (see Commit's doc comment in payload.go), so stripping "all" trailing
//     newlines would silently rewrite a message with deliberate trailing
//     blank lines. msg is therefore captured with the standard
//     `$(cmd && printf X); msg=${msg%X}` idiom — appending a sentinel
//     character after a SUCCESSFUL run keeps `$(...)` from stripping
//     anything (the sentinel, not a newline, is now last), so every
//     newline %B produced survives capture — and only then is exactly the
//     one newline `git log` itself appended trimmed off, via
//     `${msg%$'\n'}` (a shortest-suffix removal, so at most one newline is
//     ever removed). Chaining the sentinel with `&&` rather than `;` means
//     a failing `git log` (impossible in ordinary use, but see point 5 on
//     `set -e`) is not masked by the sentinel command's own success.
//
//  3. `git diff-tree --no-commit-id --name-status -M -r --root` lists that
//     commit's changed files with their status (`A`, `M`, `D`, or an
//     `R<nn>`-style rename carrying both paths) — `--root` so an initial
//     commit (rare here, since these are commits ahead of an existing base)
//     still gets a diff instead of diff-tree's default empty output for a
//     parentless commit, and `-c core.quotePath=false` so a path with any
//     non-ASCII byte in it (git's default quotes those as
//     `"caf\303\251.txt"`, double quotes and backslash escapes included)
//     arrives as its real bytes. Without it such a path is unreadable by
//     `git cat-file`/`git show` AND rejected by ValidateRepoPath's
//     backslash rule, so a single accented filename anywhere in the range
//     would fail the whole collection.
//
//  4. For every non-delete entry, the file's resulting content at that
//     commit (`git show "$c:$path"`), unless `git cat-file -s` reports it
//     over collectMaxFileBytes, in which case an `oversize=1` marker is
//     emitted instead of content. The size probe is `|| size=""`-guarded so
//     its failure reaches the `[ -n "$size" ]` test below rather than being
//     swallowed by `set -e` as an unexplained abort of the whole run — the
//     guard is otherwise unreachable, since a failed assignment would have
//     killed the script before it.
//
//  5. `set -e` plus (bash's) `set -o pipefail` turn a failed git read
//     anywhere in the script into an immediate, loud abort — inherited into
//     every nested subshell the `| while read` pipelines spawn — rather
//     than a silent empty result. Without pipefail specifically, a failing
//     `git show` piped into `base64` would still let the pipeline "succeed"
//     (base64 happily encodes zero bytes from a closed pipe), which is
//     indistinguishable on the wire from a genuinely empty file; content is
//     therefore captured into its own variable (`content=$(git show ... |
//     b64)`) rather than inlined directly as a printf argument, specifically
//     so that assignment's failure is a fatal, checked exit status instead
//     of silent empty text swallowed by printf.
//
// __TOKENS__ are substituted via strings.Replacer, not fmt.Sprintf, so the
// shell's own `%s` (there is none here, but the discipline is the same as
// sweep.go's) never needs escaping.
const collectScriptTemplate = `set -ef -o pipefail
b64() { base64 -w0; }
if ! git cat-file -e "__PROJECTBASE__^{commit}" 2>/dev/null; then
  echo "this checkout does not contain __PROJECTBASE__, the current tip of the canonical project's base branch; fetch the canonical project and rebase this branch onto it before publishing" >&2
  exit 1
fi
merges=$(git rev-list --merges HEAD --not "__FORKBASE__" "__PROJECTBASE__")
if [ -n "$merges" ]; then
  printf '%s\n' "$merges" | while IFS= read -r m; do
    [ -z "$m" ] && continue
    printf 'merge=%s\n' "$m"
  done
  exit 0
fi
git rev-list --reverse --no-merges HEAD --not "__FORKBASE__" "__PROJECTBASE__" | while IFS= read -r c; do
  [ -z "$c" ] && continue
  name=$(git log -1 --format=%an "$c")
  email=$(git log -1 --format=%ae "$c")
  msg=$(git log -1 --format=%B "$c" && printf X)
  msg="${msg%X}"
  msg="${msg%$'\n'}"
  printf 'name=%s\n' "$(printf '%s' "$name" | b64)"
  printf 'email=%s\n' "$(printf '%s' "$email" | b64)"
  printf 'msg=%s\n' "$(printf '%s' "$msg" | b64)"
  git -c core.quotePath=false diff-tree --no-commit-id --name-status -M -r --root "$c" | while IFS=$'\t' read -r status p1 p2; do
    [ -z "$status" ] && continue
    path="$p1"
    prev=""
    case "$status" in
      R*) path="$p2"; prev="$p1" ;;
    esac
    printf 'kind=%s\n' "$status"
    printf 'path=%s\n' "$(printf '%s' "$path" | b64)"
    if [ -n "$prev" ]; then
      printf 'prevpath=%s\n' "$(printf '%s' "$prev" | b64)"
    fi
    case "$status" in
      D) : ;;
      *)
        size=$(git cat-file -s "$c:$path" 2>/dev/null) || size=""
        if [ -n "$size" ] && [ "$size" -gt __MAXFILE__ ]; then
          printf 'oversize=1\n'
        else
          content=$(git show "$c:$path" | b64)
          printf 'content=%s\n' "$content"
        fi
        ;;
    esac
    printf '%s\n' "__FILE_DELIM__"
  done
  printf '%s\n' "__COMMIT_DELIM__"
done
`

// baseRefPattern is the allow-list a base ref must match before it is
// substituted into collectScriptTemplate: letters, digits, and the
// characters a real git ref name uses (`.`, `_`, `/`, `-`). BuildCollectCommand
// refuses (rather than escapes or quotes around) anything outside this set,
// the same "refuse, don't repair" discipline ValidateRepoPath applies to
// file paths — a base ref reaches the guest's shell as literal script text
// (there is no argv slot for it: see the doc comment below), so it must be
// provably inert before it is ever substituted in.
var baseRefPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// commitSHAPattern is what a projectBase must match. It is deliberately far
// stricter than baseRefPattern: a projectBase is never a name a human typed
// or a guest reported, it is always a full object id this package itself
// just read from drupal.org's API (Client.BranchTip), so anything that is
// not 40 hex characters means a caller wired the wrong value through rather
// than a user made a typo — and it is about to be substituted into script
// text either way.
var commitSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// validateCommitSHA refuses a projectBase that is not a full commit SHA.
func validateCommitSHA(sha string) error {
	if !commitSHAPattern.MatchString(sha) {
		return fmt.Errorf("drupalorg: %q is not a full 40-character commit SHA; the canonical project's base-branch tip must be resolved before a change set can be collected against it", sha)
	}
	return nil
}

// validateBaseRef refuses a base ref that could do anything other than name
// a git ref once embedded, double-quoted, in collectScriptTemplate.
func validateBaseRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("drupalorg: base ref is empty")
	}
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("drupalorg: base ref %q must not start with \"-\" (would be read as an option)", ref)
	}
	if strings.Contains(ref, "..") {
		return fmt.Errorf("drupalorg: base ref %q contains \"..\", which is not a valid git ref component", ref)
	}
	if !baseRefPattern.MatchString(ref) {
		return fmt.Errorf("drupalorg: base ref %q contains characters outside the safe set [A-Za-z0-9._/-]", ref)
	}
	return nil
}

// BuildCollectCommand returns the single guest-side command
// (internal/provider.Provider.RunArgv's `expr`) that collects the change
// set for baseRef..HEAD in whatever checkout the caller runs it against.
//
// Deliberately, this takes no checkout-path parameter and never embeds one:
// exactly like internal/ui/landing.go's commitAndPushExpr, the checkout is
// selected entirely by the working directory RunArgv sets (its own argv
// element, `--workdir <path>`), never by splicing a path into this script's
// text. A checkout path is the lowest-trust string in this system — it is
// discovered by sweeping the guest's filesystem — and RunArgv's contract
// exists precisely so that data never has to reach a guest shell as text.
// The script below therefore operates on the process's current directory
// throughout.
//
// The two base refs are different: both are host-computed, not
// guest-observed, and RunArgv's (workdir, expr) signature leaves no argv
// slot to pass either any other way. They are therefore substituted into
// the script as text, mirroring sweep.go's strings.Replacer token style —
// but only after validateBaseRef and validateCommitSHA refuse anything
// outside their respective safe sets, which is what keeps those
// substitutions safe rather than merely convenient.
//
// forkBase is what the fork branch already holds: the checkout's own
// upstream tracking ref, resolved by BuildRemoteInfoCommand (see the
// package doc comment's "No network, ever" section).
//
// projectBase is the tip of the CANONICAL project's base branch, as a full
// SHA read host-side from drupal.org. It is deliberately not the fork's own
// copy of that branch: an issue fork's base branch is not auto-synced and
// is in practice never synced by hand, so the fork's copy answers a
// question about a snapshot nobody has refreshed. Excluding both is what
// makes the collected range mean "this contributor's commits, not yet
// published" rather than "everything that has not reached the fork branch",
// which is a materially different set the moment the base branch enters
// HEAD's ancestry — see collectScriptTemplate's step 1.
func BuildCollectCommand(forkBase, projectBase string) (string, error) {
	if err := validateBaseRef(forkBase); err != nil {
		return "", err
	}
	if err := validateCommitSHA(projectBase); err != nil {
		return "", err
	}
	r := strings.NewReplacer(
		"__FORKBASE__", forkBase,
		"__PROJECTBASE__", projectBase,
		"__MAXFILE__", strconv.FormatInt(collectMaxFileBytes, 10),
		"__FILE_DELIM__", collectFileDelim,
		"__COMMIT_DELIM__", collectCommitDelim,
	)
	return r.Replace(collectScriptTemplate), nil
}

// collectCommitFieldKeys and collectFileFieldKeys are the recognized
// "key=value" field names at each of the two record scopes ParseCollect
// tracks. As in sweep.go's sweepFieldKeys, anything else — a login shell's
// banner, stray output from a misbehaving guest — is ignored as noise
// rather than misparsed. The two sets are disjoint by construction, which
// is what lets ParseCollect route a line to the right scope by key alone,
// with no separate "which record am I in" marker needed.
var (
	collectCommitFieldKeys = map[string]bool{"name": true, "email": true, "msg": true}
	collectFileFieldKeys   = map[string]bool{"kind": true, "path": true, "prevpath": true, "content": true, "oversize": true}
)

// MergeCommitsError reports that the range to publish contains merge
// commits, which publication cannot carry, and names them.
//
// It is a typed error rather than a formatted string because the two
// surfaces phrase their own wrapping differently and a test should be able
// to assert the condition without matching prose — the same reason
// ErrForkMoved exists in publish.go. The guidance it carries is the
// actionable half: a content-API replay has no way to express a second
// parent, so a branch that has the destination branch merged INTO it cannot
// be published as-is, and rebasing is the operation that produces a
// publishable shape.
type MergeCommitsError struct {
	// SHAs are the merge commits found in the range, as the guest reported
	// them. They are the guest's own output and so are only ever printed,
	// never fed back to git or to drupal.org.
	SHAs []string
}

func (e *MergeCommitsError) Error() string {
	return fmt.Sprintf(
		"drupalorg: the range to publish contains %d merge commit(s) (%s), which publication cannot carry: "+
			"each commit is replayed through drupal.org's content API, which lands one commit per call from a list "+
			"of file actions and has no way to express a merge's second parent. "+
			"Rebase this branch onto the canonical project's base branch instead of merging that branch into it, then publish again",
		len(e.SHAs), strings.Join(e.SHAs, ", "),
	)
}

// ParseCollect converts one collection run's raw guest stdout into a
// ChangeSet, decoding every base64-carried field, classifying each
// diff-tree status into a payload.ActionKind, choosing each file's payload
// Encoding via EncodeContent, and validating every path via
// ValidateFileAction (which itself calls ValidateRepoPath). It refuses the
// WHOLE change set — returning an error rather than a partial ChangeSet —
// if any single path is invalid, any file was marked oversize by the guest,
// or the change set's total encoded size exceeds collectMaxTotalBytes: a
// publication with a silently-dropped or silently-truncated file is worse
// than no publication at all.
//
// Like ParseSweep, this is a pure function of raw: it performs no I/O, has
// no guest or VM dependency, and is driven entirely by captured or
// synthetic text in tests.
func ParseCollect(raw string) (ChangeSet, error) {
	var cs ChangeSet
	var total int64
	var merges []string
	commitRec := map[string]string{}
	fileRec := map[string]string{}
	var actions []FileAction

	flushFile := func() error {
		if len(fileRec) == 0 {
			return nil
		}
		fa, err := fileActionFromRecord(fileRec)
		if err != nil {
			return err
		}
		actions = append(actions, fa)
		clear(fileRec)
		return nil
	}

	flushCommit := func() error {
		if err := flushFile(); err != nil {
			return err
		}
		if len(commitRec) == 0 && len(actions) == 0 {
			return nil // a stray/duplicate delimiter with nothing accumulated
		}
		c, err := commitFromRecord(commitRec, actions)
		if err != nil {
			return err
		}
		cs.Commits = append(cs.Commits, c)
		total += CommitEncodedSize(c)
		clear(commitRec)
		actions = nil
		return nil
	}

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)

		switch trimmed {
		case collectFileDelim:
			if err := flushFile(); err != nil {
				return ChangeSet{}, err
			}
			continue
		case collectCommitDelim:
			if err := flushCommit(); err != nil {
				return ChangeSet{}, err
			}
			continue
		case "":
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue // noise: not a "key=value" field line
		}
		switch {
		case key == "merge":
			// Record-less and scope-less, unlike every other key here: the
			// script emits these INSTEAD of a change set and stops, so there
			// is no commit or file record for one to belong to.
			merges = append(merges, value)
		case collectFileFieldKeys[key]:
			fileRec[key] = value
		case collectCommitFieldKeys[key]:
			commitRec[key] = value
		default:
			continue // noise: unrecognized key
		}
	}

	// Checked before the truncation guard below, and before anything is
	// returned: a range containing a merge is refused whole, so a partial
	// or empty commit list alongside it is not a second, separate problem
	// to report. The guest stops before collecting when it finds one, so in
	// practice nothing else has accumulated.
	if len(merges) > 0 {
		return ChangeSet{}, &MergeCommitsError{SHAs: merges}
	}

	// Anything still pending here means the stream ended before its final
	// collectCommitDelim: every complete commit already flushed itself (and
	// reset commitRec/fileRec/actions to empty) as its closing delimiter
	// was seen, so leftover state can only mean the capture was cut off
	// mid-commit — a truncated network read, a killed guest process, a
	// future bug that reads the wrong stream. Silently returning the
	// commits collected so far would drop the trailing one without any
	// sign that anything was missing, which is exactly the "silently
	// truncated is worse than no publication" failure this package exists
	// to avoid — so this refuses the whole change set instead.
	if len(fileRec) != 0 || len(commitRec) != 0 || len(actions) != 0 {
		return ChangeSet{}, fmt.Errorf("drupalorg: guest output ended before its closing delimiter (%d commit(s) already parsed); the capture was truncated — refusing the partial change set", len(cs.Commits))
	}

	if total > collectMaxTotalBytes {
		return ChangeSet{}, fmt.Errorf("drupalorg: collected change set is %d bytes, exceeding the %d byte total cap; refusing to publish", total, collectMaxTotalBytes)
	}

	return cs, nil
}

// fileActionFromRecord decodes one accumulated file-scoped record (see
// collectFileFieldKeys) into a FileAction. kind classification follows `git
// diff-tree --name-status -M`'s vocabulary exactly: "A" and "M" need no
// second path, "D" carries no content, and any "R<nn>" rename carries both
// the previous path (diff-tree's first path) and the resulting path
// (diff-tree's second path) — which is the reason an earlier "paths and
// their resulting contents" design (see payload.go's ActionKind doc
// comment) could not express a rename at all.
func fileActionFromRecord(rec map[string]string) (FileAction, error) {
	status := rec["kind"]

	var kind ActionKind
	switch {
	case status == "A":
		kind = ActionCreate
	case status == "M":
		kind = ActionUpdate
	case status == "D":
		kind = ActionDelete
	case strings.HasPrefix(status, "R"):
		kind = ActionMove
	default:
		return FileAction{}, fmt.Errorf("drupalorg: guest emitted an unrecognized diff-tree status %q", status)
	}

	p, err := decodeB64Field(rec, "path")
	if err != nil {
		return FileAction{}, err
	}
	fa := FileAction{Kind: kind, Path: p}

	if kind == ActionMove {
		prev, err := decodeB64Field(rec, "prevpath")
		if err != nil {
			return FileAction{}, err
		}
		fa.PreviousPath = prev
	}

	if kind != ActionDelete {
		if rec["oversize"] == "1" {
			return FileAction{}, fmt.Errorf("drupalorg: file %q exceeds the guest-side per-file cap (%d bytes) and was not collected; refusing the whole change set rather than publishing it with a file missing", fa.Path, collectMaxFileBytes)
		}
		content, err := decodeB64RawField(rec, "content")
		if err != nil {
			return FileAction{}, err
		}
		fa.Encoding, fa.Content = EncodeContent(content)
	}

	if err := ValidateFileAction(fa); err != nil {
		return FileAction{}, err
	}
	return fa, nil
}

// commitFromRecord decodes one accumulated commit-scoped record (see
// collectCommitFieldKeys) plus its already-collected actions into a Commit.
func commitFromRecord(rec map[string]string, actions []FileAction) (Commit, error) {
	name, err := decodeB64Field(rec, "name")
	if err != nil {
		return Commit{}, err
	}
	email, err := decodeB64Field(rec, "email")
	if err != nil {
		return Commit{}, err
	}
	msg, err := decodeB64Field(rec, "msg")
	if err != nil {
		return Commit{}, err
	}
	return Commit{
		Message:     msg,
		AuthorName:  name,
		AuthorEmail: email,
		Actions:     actions,
	}, nil
}

// decodeB64Field base64-decodes rec[key] (the guest's WIRE encoding — see
// the package doc comment) into a string, for fields that are always text
// (names, emails, messages, paths).
func decodeB64Field(rec map[string]string, key string) (string, error) {
	raw, err := decodeB64RawField(rec, key)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// decodeB64RawField base64-decodes rec[key] into raw bytes, for content
// that must be inspected (via EncodeContent) rather than assumed textual.
func decodeB64RawField(rec map[string]string, key string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(rec[key])
	if err != nil {
		return nil, fmt.Errorf("drupalorg: decoding guest field %q: %w", key, err)
	}
	return raw, nil
}

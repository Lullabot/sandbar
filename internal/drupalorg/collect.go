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
// `diff-tree`, `cat-file -s`, `show`. None of them contacts a remote. The
// base ref this task needs ("what has not yet reached the fork") is either
// resolved from the guest's own local remote-tracking ref by the caller
// before this command is built, or supplied directly — either way, nothing
// here fetches. Remote truth is confirmed host-side, by the publish flow
// itself, not by the guest.
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
//  1. `git rev-list --reverse --no-merges "<base>..HEAD"` lists the commits
//     not yet on the base ref, oldest first — the order a replay must send
//     them in.
//
//     --no-merges is load-bearing rather than tidy. A merge commit carries no
//     file changes of its own, so `diff-tree` emits nothing for it and it
//     would arrive here as a commit with zero file actions — which the
//     publisher refuses, failing the WHOLE change set after the human has
//     already confirmed it. Skipping merges is also the only coherent reading
//     for a content-API replay: the API lands one commit per call from a list
//     of file actions and cannot express a second parent, so a merge is not
//     something publication could reproduce even if it were carried. The
//     changes a merge brought in travel as the commits themselves.
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
git rev-list --reverse --no-merges "__BASE__..HEAD" | while IFS= read -r c; do
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
// baseRef is different: it is host-computed (either the caller's own
// resolution of the fork's local remote-tracking ref, or an explicit
// fallback — see the package doc comment's "No network, ever" section), not
// guest-observed, and RunArgv's (workdir, expr) signature leaves no argv
// slot to pass it any other way. It is therefore substituted into the
// script as text, mirroring sweep.go's strings.Replacer token style — but
// only after validateBaseRef refuses anything outside a plain git-ref
// character set, which is what keeps that substitution safe rather than
// merely convenient.
func BuildCollectCommand(baseRef string) (string, error) {
	if err := validateBaseRef(baseRef); err != nil {
		return "", err
	}
	r := strings.NewReplacer(
		"__BASE__", baseRef,
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
		case collectFileFieldKeys[key]:
			fileRec[key] = value
		case collectCommitFieldKeys[key]:
			commitRec[key] = value
		default:
			continue // noise: unrecognized key
		}
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

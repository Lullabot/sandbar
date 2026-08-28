// Package drupalorg implements host-side publication of a guest's changes to
// drupal.org, via GitLab's repository content API rather than git. The design
// splits authority along one line: the guest decides WHAT changes — the
// commits, their messages, and their file contents — and the host decides
// WHERE they go — which fork, which branch, whether a merge request follows.
// The guest never sees a credential, a project, a branch name, or a URL; it
// only ever produces the payload type defined in this file, which is
// structurally incapable of naming a destination. That is enforced by the
// type itself, not by convention, so no amount of prompt injection or agent
// compromise can make a payload smuggle in where it goes.
package drupalorg

import (
	"encoding/base64"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"
)

// ActionKind identifies what a FileAction does to a single path. GitLab's
// content API distinguishes these explicitly, and so must this type: an
// earlier design described the payload as "paths and their resulting
// contents", which cannot express a deletion (no content) or a rename (two
// paths).
type ActionKind string

const (
	ActionCreate ActionKind = "create"
	ActionUpdate ActionKind = "update"
	ActionDelete ActionKind = "delete"
	ActionMove   ActionKind = "move"
)

// Encoding names how FileAction.Content is carried. GitLab's content API
// accepts "text" or "base64"; base64 is required for content that is not
// valid UTF-8, since the API transports content as a JSON string.
type Encoding string

const (
	EncodingText   Encoding = "text"
	EncodingBase64 Encoding = "base64"
)

// FileAction is one change to one path within a commit. Path is always
// repository-relative and must be passed through ValidateRepoPath (directly,
// or via ValidateFileAction) before use — nothing in this type validates
// itself on construction. PreviousPath is populated only for ActionMove,
// naming the path being moved from. Content and Encoding are populated for
// the kinds that carry content (create, update, and optionally move-with-
// edit); a delete carries no content.
type FileAction struct {
	Kind         ActionKind
	Path         string
	PreviousPath string
	Content      string
	Encoding     Encoding
}

// Commit is one unit of history to replay on drupal.org. AuthorName and
// AuthorEmail are the one guest-supplied fields that reach drupal.org
// verbatim, and that is deliberate: replaying a commit without its original
// author would rewrite it. This does not weaken the guest/host split — an
// author name cannot redirect a write anywhere, and the committer of record
// on drupal.org is always the PAT owner, regardless of what a commit claims
// as its author. Actions is ordered because GitLab's content API applies a
// commit's actions in the order given.
type Commit struct {
	Message     string
	AuthorName  string
	AuthorEmail string
	Actions     []FileAction
}

// ChangeSet is the full publication payload: an ordered list of commits to
// replay, one API call per commit, preserving the guest's history rather
// than squashing it. No type reachable from ChangeSet names a project,
// branch, remote, URL, or host — see the package doc comment. Where this
// change set goes is decided entirely on the host, outside this type.
type ChangeSet struct {
	Commits []Commit
}

// ValidateRepoPath refuses any path that is not a plain, repository-relative
// path, and it refuses rather than repairs: a hostile path is rejected with
// an error naming it, never silently rewritten into something plausible.
// Both the sand surfaces and the publish client call this same function, so
// there is exactly one place path safety is decided.
//
// Repository paths use "/" semantics unconditionally (the "path" package,
// not "path/filepath"), because a repository path is always slash-separated
// regardless of the host OS sand runs on.
func ValidateRepoPath(p string) error {
	if p == "" {
		return fmt.Errorf("repository path %q is empty", p)
	}
	if p == "." {
		return fmt.Errorf("repository path %q is the current directory, not a file", p)
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("repository path %q is absolute, not repository-relative", p)
	}
	if len(p) > 1 && p[1] == ':' {
		return fmt.Errorf("repository path %q looks like a Windows drive letter", p)
	}
	if strings.ContainsRune(p, '\\') {
		return fmt.Errorf("repository path %q contains a backslash", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return fmt.Errorf("repository path %q contains a \"..\" traversal element", p)
		}
	}
	if cleaned := path.Clean(p); cleaned != p {
		return fmt.Errorf("repository path %q is not in canonical form (would normalise to %q); refusing rather than rewriting it", p, cleaned)
	}
	return nil
}

// ValidateFileAction validates a FileAction as a whole: its path (or paths,
// for a move) via ValidateRepoPath, and the content constraints the plan
// calls out explicitly — a delete carries no content, a move carries both a
// Path and a PreviousPath (both validated), only a move may carry a
// PreviousPath at all, and any action carrying content must name a valid
// Encoding for it — GitLab's content API accepts only "text" or "base64",
// and this is the one place that value is checked before it reaches the API.
func ValidateFileAction(a FileAction) error {
	if a.Kind != ActionMove && a.PreviousPath != "" {
		return fmt.Errorf("%s action for %q carries a PreviousPath (%q); only a move action may set one", a.Kind, a.Path, a.PreviousPath)
	}
	switch a.Kind {
	case ActionCreate, ActionUpdate:
		if err := ValidateRepoPath(a.Path); err != nil {
			return err
		}
		if !validEncoding(a.Encoding) {
			return fmt.Errorf("%s action for %q has invalid encoding %q; must be %q or %q", a.Kind, a.Path, a.Encoding, EncodingText, EncodingBase64)
		}
	case ActionDelete:
		if err := ValidateRepoPath(a.Path); err != nil {
			return err
		}
		if a.Content != "" {
			return fmt.Errorf("delete action for %q carries content; a delete must have none", a.Path)
		}
	case ActionMove:
		if a.Path == "" || a.PreviousPath == "" {
			return fmt.Errorf("move action requires both Path (%q) and PreviousPath (%q)", a.Path, a.PreviousPath)
		}
		if err := ValidateRepoPath(a.Path); err != nil {
			return err
		}
		if err := ValidateRepoPath(a.PreviousPath); err != nil {
			return err
		}
		if a.Content != "" && !validEncoding(a.Encoding) {
			return fmt.Errorf("move action for %q carries content with invalid encoding %q; must be %q or %q", a.Path, a.Encoding, EncodingText, EncodingBase64)
		}
	default:
		return fmt.Errorf("unknown file action kind %q", a.Kind)
	}
	return nil
}

// validEncoding reports whether e is one of the two encodings GitLab's
// content API accepts.
func validEncoding(e Encoding) bool {
	return e == EncodingText || e == EncodingBase64
}

// EncodeContent chooses how to carry data as a FileAction's Content: text
// when data is valid UTF-8 (what GitLab's content API expects for
// encoding: text), base64 (StdEncoding, matching the API's encoding:
// base64) otherwise. Both branches round-trip data unchanged: text content
// is the raw bytes as a string, and base64 content decodes back to the
// exact original bytes.
func EncodeContent(data []byte) (Encoding, string) {
	if utf8.Valid(data) {
		return EncodingText, string(data)
	}
	return EncodingBase64, base64.StdEncoding.EncodeToString(data)
}

// CommitEncodedSize reports the total encoded content bytes across a
// commit's actions, so a caller can weigh a commit against the content
// API's 20 MB rate-limit threshold and 300 MB hard rejection threshold
// before sending it.
func CommitEncodedSize(c Commit) int64 {
	var total int64
	for _, a := range c.Actions {
		total += int64(len(a.Content))
	}
	return total
}

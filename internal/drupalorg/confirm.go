package drupalorg

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// maxRenderedContentLines bounds how many lines of a single file's content
// RenderConfirmation prints before eliding the remainder. This is not a
// summary count: everything up to the limit renders in full, and what
// follows is named by a clearly marked line count rather than hidden
// silently — the plan is explicit that the confirmation must show "what
// will change and where in a form a human can actually read, not a summary
// count", and this limit exists only to keep a single pathological file
// from making the confirmation itself unreadable.
const maxRenderedContentLines = 2000

// maxRenderedContentBytes bounds how many bytes of a single file's content
// renderContent will scan before eliding the remainder, independent of
// maxRenderedContentLines. A file with few or no newlines (a minified
// asset, a data file on one line) would otherwise never hit the line-count
// limit at all and could still make the confirmation itself unreadable —
// exactly the pathological case maxRenderedContentLines exists to guard
// against, just shaped differently.
const maxRenderedContentBytes = 2 << 20 // 2 MiB

// RenderConfirmation renders the human-readable confirmation a developer
// must see before any publish: the destination it all lands on, and for
// each commit in order, its message and author, and for each file action
// its kind, path (and previous path for a move), and the resulting content.
// Binary content is named as binary with its size rather than dumped.
//
// It is pure — no I/O, no terminal escapes — so cmd/sand can print it and
// the TUI can put it in a view without either reimplementing the
// confirmation.
//
// Every guest-supplied string it renders (commit messages, author fields,
// paths, and file content) goes through sanitizeLine or sanitizeContentLine
// first. That is not cosmetic: this text is the human's ONLY control over a
// permanent, public write, and the package doc comment's threat model is a
// guest agent that may be compromised or prompt-injected. Rendered raw, a
// commit message carrying a newline could forge a second "Destination:" line,
// and one carrying ANSI escapes could scroll, clear, or overwrite the real
// destination the human is about to approve. The repository already states
// this rule for untrusted text reaching a plain renderer (see the ansi.Strip
// note at internal/ui/model.go's job-buffer comment); this is the same rule
// applied at the same kind of boundary.
func RenderConfirmation(cs ChangeSet, dest Destination) string {
	var b strings.Builder

	// The destination fields are host-derived, not guest-supplied, but they
	// are sanitized too: a destination line that cannot be forged is worth
	// more than one that merely happens not to be today.
	fmt.Fprintf(&b, "Destination: %s (branch %q)\n", sanitizeLine(dest.ForkPath), sanitizeLine(dest.Branch))
	fmt.Fprintf(&b, "Merge request target: %s (branch %q)\n", sanitizeLine(dest.ParentPath), sanitizeLine(dest.ParentBranch))
	fmt.Fprintf(&b, "\n%d commit(s) will be published:\n", len(cs.Commits))

	for i, c := range cs.Commits {
		fmt.Fprintf(&b, "\nCommit %d/%d: %s\n", i+1, len(cs.Commits), sanitizeLine(c.Message))
		fmt.Fprintf(&b, "  Author: %s <%s>\n", sanitizeLine(c.AuthorName), sanitizeLine(c.AuthorEmail))
		for _, a := range c.Actions {
			renderFileAction(&b, a)
		}
	}

	return b.String()
}

// sanitizeLine reduces s to a single line safe to print verbatim into the
// confirmation: ANSI escape sequences are stripped, and every remaining
// control character — newlines included — is rendered as a visible Go-style
// escape rather than acted on. Escaping rather than dropping is deliberate:
// a developer approving a public write should SEE that a commit message
// contained a newline or a terminal escape, not have it quietly removed.
func sanitizeLine(s string) string {
	return escapeControl(ansi.Strip(s), false)
}

// sanitizeContentLine is sanitizeLine for one line of file content, which
// has already been split on "\n" and is fenced behind a "    | " prefix. A
// tab is kept as-is here — it is ordinary, meaningful content in source
// files and cannot break out of the fenced line — while every other control
// character is escaped exactly as in sanitizeLine.
func sanitizeContentLine(s string) string {
	return escapeControl(ansi.Strip(s), true)
}

// escapeControl replaces control characters in s with visible Go-style
// escapes, optionally keeping a literal tab. It returns s unchanged (no
// allocation) when there is nothing to escape, which is the overwhelmingly
// common case.
func escapeControl(s string, keepTab bool) string {
	needs := strings.IndexFunc(s, func(r rune) bool {
		return isControl(r, keepTab)
	})
	if needs < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !isControl(r, keepTab) {
			b.WriteRune(r)
			continue
		}
		// strconv.QuoteRune yields "'\n'", "'\x1b'" and so on; the
		// surrounding quotes are trimmed so the escape reads inline.
		b.WriteString(strings.Trim(strconv.QuoteRune(r), "'"))
	}
	return b.String()
}

// isControl reports whether r must be escaped rather than printed. utf8.RuneError
// is included because ansi.Strip leaves invalid UTF-8 in place, and a lone
// replacement character in the confirmation is less informative than a visible
// escape naming it.
func isControl(r rune, keepTab bool) bool {
	if r == '\t' && keepTab {
		return false
	}
	return r == utf8.RuneError || unicode.IsControl(r)
}

// renderFileAction renders one FileAction's kind, path (and previous path
// for a move), and its resulting content, if any.
func renderFileAction(b *strings.Builder, a FileAction) {
	switch a.Kind {
	case ActionMove:
		fmt.Fprintf(b, "  move: %s -> %s\n", sanitizeLine(a.PreviousPath), sanitizeLine(a.Path))
	case ActionDelete:
		fmt.Fprintf(b, "  delete: %s\n", sanitizeLine(a.Path))
		// A delete never carries content (ValidateFileAction enforces
		// this). Should an unvalidated change set reach here carrying some
		// anyway, say so rather than drop it silently: this text is the
		// human's only view of what is about to be written.
		if a.Content != "" {
			fmt.Fprintf(b, "    (warning: this delete carries content, which will not be published)\n")
		}
		return
	case ActionCreate, ActionUpdate:
		fmt.Fprintf(b, "  %s: %s\n", a.Kind, sanitizeLine(a.Path))
	default:
		// Same reason as the delete-with-content warning above: nothing
		// guarantees ValidateFileAction ran, so a kind this package does not
		// know can reach here — an empty Kind most easily of all. Naming it
		// as unknown beats rendering a bare "  : path" line that reads like
		// an ordinary action whose kind simply failed to print.
		fmt.Fprintf(b, "  unknown action %q: %s\n", sanitizeLine(string(a.Kind)), sanitizeLine(a.Path))
	}

	if a.Content == "" {
		if a.Kind != ActionMove {
			fmt.Fprintf(b, "    (empty file)\n")
		}
		return
	}
	renderContent(b, a.Content, a.Encoding)
}

// renderContent renders a FileAction's resulting content: binary content
// (EncodingBase64) is named as binary with its decoded size rather than
// dumped, and text content is rendered as readable lines, elided past
// maxRenderedContentLines with a clearly marked count of what was elided
// rather than collapsed into a summary.
func renderContent(b *strings.Builder, content string, encoding Encoding) {
	switch encoding {
	case EncodingBase64:
		raw, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			// Malformed content should have been rejected before it ever
			// reached here (ValidateFileAction only checks the encoding
			// name, not that base64 content decodes); report what is
			// knowable rather than panic or silently drop it.
			fmt.Fprintf(b, "    <binary, undecodable base64 content, %d encoded bytes>\n", len(content))
			return
		}
		fmt.Fprintf(b, "    <binary, %d bytes>\n", len(raw))
		return
	case EncodingText:
		// Rendered as lines below.
	default:
		// "text" and "base64" are the only encodings GitLab's content API
		// accepts and the only ones ValidateFileAction allows, but this
		// renderer deliberately does not validate, so an unvalidated change
		// set can still arrive with something else — an empty Encoding most
		// easily of all. Falling through to the text branch would dump bytes
		// of an unknown shape as if they had been read and vouched for; name
		// the encoding instead.
		fmt.Fprintf(b, "    <content in unknown encoding %q, %d bytes>\n", sanitizeLine(string(encoding)), len(content))
		return
	}

	byteElided := 0
	if len(content) > maxRenderedContentBytes {
		byteElided = len(content) - maxRenderedContentBytes
		content = truncateUTF8(content, maxRenderedContentBytes)
	}

	// A file's trailing newline terminates its last line rather than starting
	// an empty one, so it is trimmed before splitting: otherwise every
	// ordinary text file renders one phantom blank line and reports one more
	// elided line than it actually has. SplitN then stops at the limit,
	// leaving everything past it unsplit in the final element, so the elided
	// lines are counted without building a slice entry for each of them —
	// content reaches here already capped at maxRenderedContentBytes, but
	// only maxRenderedContentLines of it is ever rendered.
	shown := strings.SplitN(strings.TrimSuffix(content, "\n"), "\n", maxRenderedContentLines+1)
	lineElided := 0
	if len(shown) > maxRenderedContentLines {
		lineElided = strings.Count(shown[maxRenderedContentLines], "\n") + 1
		shown = shown[:maxRenderedContentLines]
	}
	for _, line := range shown {
		// A "\r\n" file arrives here with a trailing "\r" on every line;
		// sanitizeContentLine escapes it visibly rather than letting a bare
		// carriage return move the terminal's cursor back over the fence.
		fmt.Fprintf(b, "    | %s\n", sanitizeContentLine(line))
	}
	if lineElided > 0 {
		fmt.Fprintf(b, "    | ... (%d more line(s) elided)\n", lineElided)
	}
	if byteElided > 0 {
		fmt.Fprintf(b, "    | ... (%d more byte(s) elided)\n", byteElided)
	}
}

// truncateUTF8 returns the prefix of s that is at most maxBytes long,
// backing off to the nearest earlier UTF-8 rune boundary so the returned
// string is never split mid-rune.
func truncateUTF8(s string, maxBytes int) string {
	// s[maxBytes] would panic when maxBytes indexes the end of s, which the
	// one current caller never does (it truncates only when s is longer).
	// Guarding here keeps that a property of this function rather than of
	// its caller.
	if maxBytes >= len(s) {
		return s
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

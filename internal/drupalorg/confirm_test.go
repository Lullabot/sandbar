package drupalorg

import (
	"fmt"
	"strings"
	"testing"
)

// binaryContent is a small non-UTF-8 payload, base64-encoded the way
// EncodeContent (payload.go) would encode it — Content for an
// EncodingBase64 action is always a base64 string, never raw bytes.
var binaryContent = func() string {
	_, encoded := EncodeContent([]byte{0xff, 0xd8, 0xff, 0x00, 0x01})
	return encoded
}()

// testDestination is the Destination shared by every RenderConfirmation test
// below that doesn't care about its exact values, only that it is stated.
func testDestination() Destination {
	return Destination{
		ForkPath:     "issue/drupal-1",
		Branch:       "drupal-1",
		ParentID:     1,
		ParentPath:   "project/drupal",
		ParentBranch: "11.x",
	}
}

// TestRenderConfirmation_DeleteAndMove is the golden-ish test the task
// calls for: a delete and a move — the two kinds the earlier
// "paths and their resulting contents" payload design could not express at
// all — must render legibly, along with an ordinary create/update and the
// destination itself.
func TestRenderConfirmation_DeleteAndMove(t *testing.T) {
	dest := Destination{
		ForkPath:     "issue/drupal-3181657",
		Branch:       "drupal-3181657",
		ParentID:     59858,
		ParentPath:   "project/drupal",
		ParentBranch: "11.x",
	}
	cs := ChangeSet{
		Commits: []Commit{
			{
				Message:     "Fix the timezone bug",
				AuthorName:  "Jane Developer",
				AuthorEmail: "jane@example.com",
				Actions: []FileAction{
					{Kind: ActionUpdate, Path: "src/timezone.php", Content: "<?php\necho 'fixed';\n", Encoding: EncodingText},
					{Kind: ActionDelete, Path: "src/old_timezone.php"},
					{Kind: ActionMove, Path: "src/new_name.php", PreviousPath: "src/timezone_helper.php"},
					{Kind: ActionCreate, Path: "logo.png", Content: binaryContent, Encoding: EncodingBase64},
				},
			},
		},
	}

	out := RenderConfirmation(cs, dest)

	// The destination must be stated explicitly.
	for _, want := range []string{"issue/drupal-3181657", "drupal-3181657", "project/drupal", "11.x"} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderConfirmation() missing destination detail %q; got:\n%s", want, out)
		}
	}

	// Commit message and author.
	for _, want := range []string{"Fix the timezone bug", "Jane Developer", "jane@example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderConfirmation() missing %q; got:\n%s", want, out)
		}
	}

	// Update: path and content readable, not a summary count.
	if !strings.Contains(out, "src/timezone.php") || !strings.Contains(out, "echo 'fixed';") {
		t.Errorf("RenderConfirmation() must render the update's path and content; got:\n%s", out)
	}

	// Delete: path named, no content, and named as a delete (not silently
	// dropped or expressed as a diff-less "change" the earlier design
	// could not represent).
	if !strings.Contains(out, "src/old_timezone.php") {
		t.Errorf("RenderConfirmation() must name the deleted path; got:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "delete") {
		t.Errorf("RenderConfirmation() must name the delete action's kind; got:\n%s", out)
	}

	// Move: both the previous path and the new path must appear.
	if !strings.Contains(out, "src/timezone_helper.php") || !strings.Contains(out, "src/new_name.php") {
		t.Errorf("RenderConfirmation() must render both paths of a move; got:\n%s", out)
	}

	// Binary content is named as binary with its size, not dumped.
	if !strings.Contains(strings.ToLower(out), "binary") {
		t.Errorf("RenderConfirmation() must name binary content as binary; got:\n%s", out)
	}
	if strings.Contains(out, binaryContent) {
		t.Errorf("RenderConfirmation() must not dump the raw base64 content; got:\n%s", out)
	}
}

// TestRenderConfirmation_LongContentNotHidden proves that a long file isn't
// collapsed into a meaningless summary count: an elision marker may appear,
// but content from both the start and something identifying the extent of
// what follows must still be present.
func TestRenderConfirmation_LongContentNotHidden(t *testing.T) {
	var lines []string
	for i := 0; i < 5000; i++ {
		lines = append(lines, "line content marker")
	}
	content := strings.Join(lines, "\n")

	dest := testDestination()
	cs := ChangeSet{
		Commits: []Commit{
			{
				Message:     "Add a long file",
				AuthorName:  "Jane Developer",
				AuthorEmail: "jane@example.com",
				Actions: []FileAction{
					{Kind: ActionCreate, Path: "big.txt", Content: content, Encoding: EncodingText},
				},
			},
		},
	}

	out := RenderConfirmation(cs, dest)
	if !strings.Contains(out, "line content marker") {
		t.Errorf("RenderConfirmation() must not hide a whole file's content; got %d bytes", len(out))
	}

	// Assert the actual elision arithmetic, not merely that some content
	// survived: "content is present" holds just as well when the renderer
	// shows one line as when it shows the two thousand it promises, so on its
	// own it cannot fail for the reason this test exists.
	if got, want := strings.Count(out, "    | line content marker\n"), maxRenderedContentLines; got != want {
		t.Errorf("RenderConfirmation() rendered %d content lines, want exactly %d (maxRenderedContentLines)", got, want)
	}
	wantElided := fmt.Sprintf("(%d more line(s) elided)", 5000-maxRenderedContentLines)
	if !strings.Contains(out, wantElided) {
		t.Errorf("RenderConfirmation() must name the exact extent of what it elided (%q); got:\n%s", wantElided, lastLines(out, 3))
	}
}

// TestRenderConfirmation_TrailingNewlineIsNotAPhantomLine proves a file's
// terminating newline is treated as terminating its last line rather than
// starting an empty one: an ordinary text file must not render a trailing
// blank content line, which would also inflate an elided-line count by one.
func TestRenderConfirmation_TrailingNewlineIsNotAPhantomLine(t *testing.T) {
	dest := testDestination()
	cs := ChangeSet{
		Commits: []Commit{
			{
				Message:     "Add a file",
				AuthorName:  "Jane Developer",
				AuthorEmail: "jane@example.com",
				Actions: []FileAction{
					{Kind: ActionCreate, Path: "f.txt", Content: "alpha\nbeta\n", Encoding: EncodingText},
				},
			},
		},
	}

	out := RenderConfirmation(cs, dest)
	if got, want := strings.Count(out, "    | "), 2; got != want {
		t.Errorf("RenderConfirmation() rendered %d content lines for a 2-line file, want %d; got:\n%s", got, want, out)
	}
	if strings.Contains(out, "    | \n") {
		t.Errorf("RenderConfirmation() rendered a phantom blank line for a file's trailing newline; got:\n%s", out)
	}
}

// TestRenderConfirmation_UnknownEncodingNotDumpedAsText proves content whose
// encoding this package does not know is named rather than dumped. Nothing
// guarantees ValidateFileAction ran before RenderConfirmation (it is
// documented as usable on its own by cmd/sand and the TUI), so an empty or
// unrecognised Encoding is reachable, and rendering it as text would present
// bytes of an unknown shape as if they had been read and vouched for.
func TestRenderConfirmation_UnknownEncodingNotDumpedAsText(t *testing.T) {
	dest := testDestination()
	for _, enc := range []Encoding{"", "gzip"} {
		cs := ChangeSet{
			Commits: []Commit{
				{
					Message:     "Add a file",
					AuthorName:  "Jane Developer",
					AuthorEmail: "jane@example.com",
					Actions: []FileAction{
						{Kind: ActionCreate, Path: "f.bin", Content: "secret-marker-content", Encoding: enc},
					},
				},
			},
		}

		out := RenderConfirmation(cs, dest)
		if strings.Contains(out, "secret-marker-content") {
			t.Errorf("RenderConfirmation() with encoding %q dumped the content as text; got:\n%s", enc, out)
		}
		if !strings.Contains(out, "unknown encoding") {
			t.Errorf("RenderConfirmation() with encoding %q must name the encoding as unknown; got:\n%s", enc, out)
		}
	}
}

// lastLines returns the final n lines of s, for error messages about output
// that would otherwise be thousands of lines long.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// TestRenderConfirmation_LongSingleLineNotUnbounded proves a pathological
// file with no newlines (so the line-count elision never triggers) is still
// bounded: content up to maxRenderedContentBytes is shown, and an elision
// marker names what follows rather than dumping it all.
func TestRenderConfirmation_LongSingleLineNotUnbounded(t *testing.T) {
	content := strings.Repeat("x", maxRenderedContentBytes+1000)

	dest := testDestination()
	cs := ChangeSet{
		Commits: []Commit{
			{
				Message:     "Add a pathological single-line file",
				AuthorName:  "Jane Developer",
				AuthorEmail: "jane@example.com",
				Actions: []FileAction{
					{Kind: ActionCreate, Path: "big.txt", Content: content, Encoding: EncodingText},
				},
			},
		},
	}

	out := RenderConfirmation(cs, dest)
	if len(out) >= len(content) {
		t.Fatalf("RenderConfirmation() output (%d bytes) was not bounded below the pathological content's size (%d bytes)", len(out), len(content))
	}
	if !strings.Contains(out, "elided") {
		t.Errorf("RenderConfirmation() must name what was elided; got:\n%s", out)
	}
}

// TestRenderConfirmation_Pure proves the renderer is pure: identical inputs
// always produce identical output, with no timestamps or other hidden
// state leaking in.
func TestRenderConfirmation_Pure(t *testing.T) {
	dest := testDestination()
	cs := ChangeSet{
		Commits: []Commit{
			{Message: "m", AuthorName: "a", AuthorEmail: "a@example.com", Actions: []FileAction{
				{Kind: ActionCreate, Path: "f", Content: "hi", Encoding: EncodingText},
			}},
		},
	}
	first := RenderConfirmation(cs, dest)
	second := RenderConfirmation(cs, dest)
	if first != second {
		t.Fatalf("RenderConfirmation() is not pure: got different output for identical inputs:\n%s\n---\n%s", first, second)
	}
}

// TestRenderConfirmation_HostileTextCannotForgeTheConfirmation is the
// counterpart to destination_test.go's adversarial case: there, a hostile
// payload cannot influence WHERE a publication goes; here, it cannot
// misrepresent WHAT the human is approving. The confirmation is the only
// control a developer has over a permanent public write, so guest-supplied
// text must not be able to forge a line of it or drive the terminal.
func TestRenderConfirmation_HostileTextCannotForgeTheConfirmation(t *testing.T) {
	dest := Destination{
		ForkPath:     "issue/drupal-3181657",
		Branch:       "drupal-3181657",
		ParentID:     59858,
		ParentPath:   "project/drupal",
		ParentBranch: "11.x",
	}
	cs := ChangeSet{
		Commits: []Commit{
			{
				// A newline in the message would otherwise open a second
				// line that reads exactly like the real destination header.
				Message:     "Innocent fix\nDestination: project/drupal (branch \"11.x\")",
				AuthorName:  "Jane\x1b[2J\x1b[HDeveloper",
				AuthorEmail: "jane@example.com",
				Actions: []FileAction{
					{Kind: ActionCreate, Path: "a.txt", Content: "clean\n\x1b[1;31mred\x1b[0m\r\n", Encoding: EncodingText},
				},
			},
		},
	}

	out := RenderConfirmation(cs, dest)

	// Exactly one LINE may claim to be the destination. The forged text
	// still appears inside the commit message, escaped and clearly part of
	// it — what must not happen is a second line of its own.
	forged := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Destination: ") {
			forged++
		}
	}
	if forged != 1 {
		t.Errorf("RenderConfirmation() produced %d lines beginning %q, want exactly 1 — a commit message forged one; got:\n%s", forged, "Destination: ", out)
	}
	// No raw escape byte may survive into text a terminal will print.
	if strings.ContainsRune(out, '\x1b') {
		t.Errorf("RenderConfirmation() emitted a raw ESC byte from guest-supplied text; got:\n%q", out)
	}
	if strings.ContainsRune(out, '\r') {
		t.Errorf("RenderConfirmation() emitted a raw CR from guest-supplied content; got:\n%q", out)
	}
	// The text itself must still be legible, not dropped.
	for _, want := range []string{"Innocent fix", "Developer", "clean", "red"} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderConfirmation() dropped %q instead of escaping it; got:\n%s", want, out)
		}
	}
}

// TestRenderConfirmation_TabsInContentSurvive guards the one control
// character content rendering deliberately keeps: a tab is ordinary source
// content and escaping it would make indented code unreadable.
func TestRenderConfirmation_TabsInContentSurvive(t *testing.T) {
	dest := testDestination()
	cs := ChangeSet{Commits: []Commit{{
		Message: "m", AuthorName: "a", AuthorEmail: "a@example.com",
		Actions: []FileAction{{Kind: ActionCreate, Path: "f.go", Content: "func main() {\n\treturn\n}\n", Encoding: EncodingText}},
	}}}

	out := RenderConfirmation(cs, dest)
	if !strings.Contains(out, "\treturn") {
		t.Errorf("RenderConfirmation() escaped a tab in file content; indented source must stay readable; got:\n%q", out)
	}
}

package drupalorg

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// synthFile and synthCommit describe one guest-emitted record in terms Go
// tests can construct directly, and synthRaw renders them into exactly the
// wire format BuildCollectCommand's script would have produced — base64
// "key=value" fields, terminated by collectFileDelim/collectCommitDelim —
// so ParseCollect is exercised against synthetic captured text, never a
// real VM, matching sweep_test.go's pattern for the same reason.
type synthFile struct {
	kind     string // diff-tree status: "A", "M", "D", "R100", ...
	path     string
	prevPath string // only for a rename
	content  []byte // nil for a delete
	oversize bool   // guest refused to emit content above its per-file cap
}

type synthCommit struct {
	name, email, msg string
	files            []synthFile
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func synthRaw(commits []synthCommit) string {
	var b strings.Builder
	for _, c := range commits {
		fmt.Fprintf(&b, "name=%s\n", b64(c.name))
		fmt.Fprintf(&b, "email=%s\n", b64(c.email))
		fmt.Fprintf(&b, "msg=%s\n", b64(c.msg))
		for _, f := range c.files {
			fmt.Fprintf(&b, "kind=%s\n", f.kind)
			fmt.Fprintf(&b, "path=%s\n", b64(f.path))
			if f.prevPath != "" {
				fmt.Fprintf(&b, "prevpath=%s\n", b64(f.prevPath))
			}
			if f.kind != "D" {
				if f.oversize {
					b.WriteString("oversize=1\n")
				} else {
					fmt.Fprintf(&b, "content=%s\n", base64.StdEncoding.EncodeToString(f.content))
				}
			}
			b.WriteString(collectFileDelim + "\n")
		}
		b.WriteString(collectCommitDelim + "\n")
	}
	return b.String()
}

// TestParseCollect_MultiCommitStream is the parser's main proof: a
// multi-commit stream exercising every action kind (create, update, delete,
// rename), a binary file, a UTF-8 filename, and — the adversarial case that
// proves the framing itself, not just the parsing — a commit message
// containing collectCommitDelim's literal text. If the wire format were
// bare delimiter-framed text instead of base64, that message would be
// misread as ending the commit record early; because it travels base64
// (see collect.go's package doc comment), it round-trips untouched.
func TestParseCollect_MultiCommitStream(t *testing.T) {
	binary := []byte{0x00, 0x01, 0xff, 0xfe, 0xde, 0xad, 0xbe, 0xef}
	adversarialMsg := "Fix the thing\n\n" + collectCommitDelim + "\nnot actually a delimiter, just quoted in the message"

	raw := "some stray login-shell banner line that is not a field\n" + synthRaw([]synthCommit{
		{
			name: "Ada Lovelace", email: "ada@example.com", msg: adversarialMsg,
			files: []synthFile{
				{kind: "A", path: "README.md", content: []byte("hello world")},
			},
		},
		{
			name: "Grace Hopper", email: "grace@example.com", msg: "Second commit",
			files: []synthFile{
				{kind: "M", path: "src/app.py", content: []byte("print('hi')\n")},
				{kind: "D", path: "old.txt"},
				{kind: "R100", path: "docs/b.md", prevPath: "docs/a.md", content: []byte("moved content")},
				{kind: "A", path: "assets/logo.png", content: binary},
				{kind: "A", path: "résumé/日本語.txt", content: []byte("unicode path test")},
			},
		},
	})

	cs, err := ParseCollect(raw)
	if err != nil {
		t.Fatalf("ParseCollect returned error: %v", err)
	}
	if len(cs.Commits) != 2 {
		t.Fatalf("got %d commits, want 2", len(cs.Commits))
	}

	c0 := cs.Commits[0]
	if c0.AuthorName != "Ada Lovelace" || c0.AuthorEmail != "ada@example.com" {
		t.Errorf("commit 0 author = %q <%q>, want Ada Lovelace <ada@example.com>", c0.AuthorName, c0.AuthorEmail)
	}
	if c0.Message != adversarialMsg {
		t.Errorf("commit 0 message was mangled:\ngot:  %q\nwant: %q", c0.Message, adversarialMsg)
	}
	if len(c0.Actions) != 1 || c0.Actions[0] != (FileAction{Kind: ActionCreate, Path: "README.md", Content: "hello world", Encoding: EncodingText}) {
		t.Errorf("commit 0 actions = %+v, want a single create of README.md", c0.Actions)
	}

	c1 := cs.Commits[1]
	if len(c1.Actions) != 5 {
		t.Fatalf("commit 1 has %d actions, want 5: %+v", len(c1.Actions), c1.Actions)
	}

	wantUpdate := FileAction{Kind: ActionUpdate, Path: "src/app.py", Content: "print('hi')\n", Encoding: EncodingText}
	if c1.Actions[0] != wantUpdate {
		t.Errorf("action 0 = %+v, want %+v", c1.Actions[0], wantUpdate)
	}

	wantDelete := FileAction{Kind: ActionDelete, Path: "old.txt"}
	if c1.Actions[1] != wantDelete {
		t.Errorf("action 1 = %+v, want %+v", c1.Actions[1], wantDelete)
	}

	wantMove := FileAction{Kind: ActionMove, Path: "docs/b.md", PreviousPath: "docs/a.md", Content: "moved content", Encoding: EncodingText}
	if c1.Actions[2] != wantMove {
		t.Errorf("action 2 = %+v, want %+v", c1.Actions[2], wantMove)
	}

	binAction := c1.Actions[3]
	if binAction.Kind != ActionCreate || binAction.Path != "assets/logo.png" || binAction.Encoding != EncodingBase64 {
		t.Errorf("binary action = %+v, want create/base64 for assets/logo.png", binAction)
	}
	gotBin, err := base64.StdEncoding.DecodeString(binAction.Content)
	if err != nil || string(gotBin) != string(binary) {
		t.Errorf("binary action content decoded to %x (err %v), want %x", gotBin, err, binary)
	}

	utf8Action := c1.Actions[4]
	wantUTF8 := FileAction{Kind: ActionCreate, Path: "résumé/日本語.txt", Content: "unicode path test", Encoding: EncodingText}
	if utf8Action != wantUTF8 {
		t.Errorf("UTF-8-filename action = %+v, want %+v", utf8Action, wantUTF8)
	}
}

// TestParseCollect_RefusesWholeSetOnOneBadPath proves the "refuse the whole
// change set" rule: a single hostile or malformed path anywhere in the
// stream must fail the entire parse, not just drop that one file.
func TestParseCollect_RefusesWholeSetOnOneBadPath(t *testing.T) {
	raw := synthRaw([]synthCommit{
		{
			name: "A", email: "a@example.com", msg: "msg",
			files: []synthFile{
				{kind: "A", path: "ok.txt", content: []byte("fine")},
				{kind: "A", path: "/etc/passwd", content: []byte("hostile")},
			},
		},
	})

	cs, err := ParseCollect(raw)
	if err == nil {
		t.Fatalf("ParseCollect(%q) = %+v, nil; want an error refusing the hostile path", raw, cs)
	}
	if !strings.Contains(err.Error(), "/etc/passwd") {
		t.Errorf("error %q does not name the offending path", err.Error())
	}
}

// TestParseCollect_OversizeFileRefusesTheSet proves the guest-side
// per-file-cap marker (oversize=1, emitted instead of content) is treated
// as a hard failure host-side, never as a file silently published without
// its content.
func TestParseCollect_OversizeFileRefusesTheSet(t *testing.T) {
	raw := synthRaw([]synthCommit{
		{
			name: "A", email: "a@example.com", msg: "msg",
			files: []synthFile{
				{kind: "A", path: "huge.bin", oversize: true},
			},
		},
	})

	_, err := ParseCollect(raw)
	if err == nil {
		t.Fatal("ParseCollect on an oversize-marked file = nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "huge.bin") {
		t.Errorf("error %q does not name the oversize file", err.Error())
	}
}

// TestParseCollect_TotalSizeCapEnforced proves the host-side total-size cap
// (the second half of this task's "size check" requirement, distinct from
// the guest's own per-file cap) is enforced with a clear message. It
// shrinks collectMaxTotalBytes for the duration of the test rather than
// constructing a real multi-megabyte fixture.
func TestParseCollect_TotalSizeCapEnforced(t *testing.T) {
	orig := collectMaxTotalBytes
	collectMaxTotalBytes = 10
	t.Cleanup(func() { collectMaxTotalBytes = orig })

	raw := synthRaw([]synthCommit{
		{
			name: "A", email: "a@example.com", msg: "msg",
			files: []synthFile{
				{kind: "A", path: "a.txt", content: []byte("this content is well over ten bytes long")},
			},
		},
	})

	_, err := ParseCollect(raw)
	if err == nil {
		t.Fatal("ParseCollect over the total-size cap = nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "10 byte") {
		t.Errorf("error %q does not name the cap", err.Error())
	}
}

// TestParseCollect_UnrecognizedStatusRefused proves a diff-tree status this
// parser does not know about (anything outside A/M/D/R<nn>) is refused
// loudly rather than silently misclassified.
func TestParseCollect_UnrecognizedStatusRefused(t *testing.T) {
	raw := synthRaw([]synthCommit{
		{
			name: "A", email: "a@example.com", msg: "msg",
			files: []synthFile{
				{kind: "T", path: "weird.txt", content: []byte("x")},
			},
		},
	})

	_, err := ParseCollect(raw)
	if err == nil {
		t.Fatal("ParseCollect on an unrecognized status = nil error, want a refusal")
	}
}

// TestParseCollect_TruncatedStreamRefused proves a capture cut off before
// its final collectCommitDelim is refused outright rather than silently
// returning only the commits that happened to complete before the cut —
// dropping the trailing commit without any sign it was ever there would be
// worse than refusing the whole change set.
func TestParseCollect_TruncatedStreamRefused(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "cut mid file record, no file or commit delimiter",
			raw: synthRaw([]synthCommit{{name: "A", email: "a@example.com", msg: "first, complete", files: []synthFile{
				{kind: "A", path: "ok.txt", content: []byte("fine")},
			}}}) + "name=" + b64("B") + "\nemail=" + b64("b@example.com") + "\nmsg=" + b64("second, cut short") + "\nkind=A\npath=" + b64("cut.txt"),
		},
		{
			name: "commit fields present but stream ends before any file or commit delimiter",
			raw:  "name=" + b64("A") + "\nemail=" + b64("a@example.com") + "\nmsg=" + b64("no delimiter follows"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs, err := ParseCollect(tc.raw)
			if err == nil {
				t.Fatalf("ParseCollect(truncated stream) = %+v, nil; want a refusal", cs)
			}
		})
	}
}

// TestBuildCollectCommand exercises the command builder: a well-formed base
// ref produces a script containing the substituted ref, the per-file cap,
// and both delimiters (and nothing that looks like a checkout path — see
// BuildCollectCommand's doc comment on why it takes none); anything outside
// a plain git-ref character set is refused outright rather than escaped.
func TestBuildCollectCommand(t *testing.T) {
	got, err := BuildCollectCommand("issue/drupal-3181657")
	if err != nil {
		t.Fatalf("BuildCollectCommand returned error: %v", err)
	}
	for _, want := range []string{
		`"issue/drupal-3181657..HEAD"`,
		"8388608", // collectMaxFileBytes
		collectFileDelim,
		collectCommitDelim,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("built command does not contain %q:\n%s", want, got)
		}
	}

	cases := []struct {
		name    string
		baseRef string
	}{
		{"empty", ""},
		{"leading dash reads as an option", "-x"},
		{"double dot", "a..b"},
		{"shell metacharacters", "a; rm -rf $(whoami)"},
		{"embedded double quote", `a"; rm -rf ~; echo "`},
		{"backtick command substitution", "a`whoami`"},
		{"whitespace", "a b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := BuildCollectCommand(tc.baseRef); err == nil {
				t.Errorf("BuildCollectCommand(%q) = nil error, want a refusal", tc.baseRef)
			}
		})
	}
}

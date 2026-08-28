package drupalorg

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// TestValidateRepoPath_HostilePaths is the security-critical test: every
// input that could redirect a write outside the repository tree, or that
// could only be made safe by silently rewriting it, must be refused rather
// than "corrected".
func TestValidateRepoPath_HostilePaths(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"absolute path", "/etc/passwd", true},
		{"traversal element", "a/../etc/passwd", true},
		{"traversal element resolving inside tree", "a/../b", true},
		{"leading traversal", "../b", true},
		{"bare traversal", "..", true},
		{"empty path", "", true},
		{"leading dot-slash", "./a/b", true},
		{"windows drive letter", "C:/Users/x", true},
		{"windows drive letter with backslash", `C:\Users\x`, true},
		{"double slash", "a//b", true},
		{"trailing slash", "a/b/", true},
		{"valid nested path", "a/b/c.txt", false},
		{"valid single-segment path", "README.md", false},
		{"valid path with dots not traversal", "a/b.c.d", false},
		{"bare current directory", ".", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRepoPath(tc.path)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateRepoPath(%q) = nil, want error", tc.path)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateRepoPath(%q) = %v, want nil", tc.path, err)
			}
			if tc.wantErr && err != nil && !strings.Contains(err.Error(), fmt.Sprintf("%q", tc.path)) {
				t.Fatalf("ValidateRepoPath(%q) error %q does not name the offending path", tc.path, err.Error())
			}
		})
	}
}

// TestValidateFileAction_KindConstraints exercises the two structural
// constraints called out in the plan: a delete carries no content, and a
// move carries both a Path and a PreviousPath, both of which are validated.
func TestValidateFileAction_KindConstraints(t *testing.T) {
	cases := []struct {
		name    string
		action  FileAction
		wantErr bool
	}{
		{
			name:    "delete with content is refused",
			action:  FileAction{Kind: ActionDelete, Path: "a/b.txt", Content: "oops"},
			wantErr: true,
		},
		{
			name:    "delete without content is valid",
			action:  FileAction{Kind: ActionDelete, Path: "a/b.txt"},
			wantErr: false,
		},
		{
			name:    "move without previous path is refused",
			action:  FileAction{Kind: ActionMove, Path: "a/b.txt"},
			wantErr: true,
		},
		{
			name:    "move without new path is refused",
			action:  FileAction{Kind: ActionMove, PreviousPath: "a/old.txt"},
			wantErr: true,
		},
		{
			name:    "move with hostile previous path is refused",
			action:  FileAction{Kind: ActionMove, Path: "a/new.txt", PreviousPath: "../old.txt"},
			wantErr: true,
		},
		{
			name:    "move with both paths is valid",
			action:  FileAction{Kind: ActionMove, Path: "a/new.txt", PreviousPath: "a/old.txt"},
			wantErr: false,
		},
		{
			name:    "create with hostile path is refused",
			action:  FileAction{Kind: ActionCreate, Path: "/etc/passwd", Content: "x", Encoding: EncodingText},
			wantErr: true,
		},
		{
			name:    "update with valid path is valid",
			action:  FileAction{Kind: ActionUpdate, Path: "a/b.txt", Content: "x", Encoding: EncodingText},
			wantErr: false,
		},
		{
			name:    "unknown kind is refused",
			action:  FileAction{Kind: ActionKind("rename"), Path: "a/b.txt"},
			wantErr: true,
		},
		{
			name:    "create without encoding is refused",
			action:  FileAction{Kind: ActionCreate, Path: "a/b.txt", Content: "x"},
			wantErr: true,
		},
		{
			name:    "create with invalid encoding is refused",
			action:  FileAction{Kind: ActionCreate, Path: "a/b.txt", Content: "x", Encoding: Encoding("gzip")},
			wantErr: true,
		},
		{
			name:    "create with a stray PreviousPath is refused",
			action:  FileAction{Kind: ActionCreate, Path: "a/b.txt", Content: "x", Encoding: EncodingText, PreviousPath: "a/old.txt"},
			wantErr: true,
		},
		{
			name:    "delete with a stray PreviousPath is refused",
			action:  FileAction{Kind: ActionDelete, Path: "a/b.txt", PreviousPath: "a/old.txt"},
			wantErr: true,
		},
		{
			name:    "move with content and no encoding is refused",
			action:  FileAction{Kind: ActionMove, Path: "a/new.txt", PreviousPath: "a/old.txt", Content: "x"},
			wantErr: true,
		},
		{
			name:    "move with content and a valid encoding is valid",
			action:  FileAction{Kind: ActionMove, Path: "a/new.txt", PreviousPath: "a/old.txt", Content: "x", Encoding: EncodingText},
			wantErr: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFileAction(tc.action)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateFileAction(%+v) = nil, want error", tc.action)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateFileAction(%+v) = %v, want nil", tc.action, err)
			}
		})
	}
}

// TestNoDestinationField is the design's structural guarantee: the payload
// types must be incapable of naming where a change set goes. Walking the
// exported fields by reflection means a later field addition (e.g. someone
// adding a convenience "Branch" field to Commit) fails this test rather than
// slipping through review unnoticed.
func TestNoDestinationField(t *testing.T) {
	forbidden := []string{
		"Project", "Branch", "Remote", "URL", "Host",
		"Target", "Namespace", "Fork", "Repo",
	}
	types := []any{FileAction{}, Commit{}, ChangeSet{}}
	for _, v := range types {
		typ := reflect.TypeOf(v)
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			for _, bad := range forbidden {
				if strings.Contains(field.Name, bad) {
					t.Fatalf(
						"%s.%s names a destination-shaped concept (%q): the payload "+
							"must be structurally incapable of naming where a change "+
							"set goes, so no field here may name a project, branch, "+
							"remote, URL, or host",
						typ.Name(), field.Name, bad,
					)
				}
			}
		}
	}
}

// TestEncodeContent_RoundTrips checks that valid UTF-8 content is carried as
// text and arbitrary binary content is carried as base64, and that both
// round-trip to the exact original bytes.
func TestEncodeContent_RoundTrips(t *testing.T) {
	cases := []struct {
		name    string
		data    []byte
		wantEnc Encoding
	}{
		{"empty content", []byte(""), EncodingText},
		{"ascii text", []byte("hello, world\n"), EncodingText},
		{"utf-8 text", []byte("héllo wörld 🎉"), EncodingText},
		{"binary content", []byte{0x00, 0xFF, 0xFE, 0x01, 0x02, 0x80, 0x81}, EncodingBase64},
		{"invalid utf-8 sequence", []byte{0xC3, 0x28}, EncodingBase64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enc, content := EncodeContent(tc.data)
			if enc != tc.wantEnc {
				t.Fatalf("EncodeContent(%v) encoding = %v, want %v", tc.data, enc, tc.wantEnc)
			}
			var got []byte
			switch enc {
			case EncodingText:
				got = []byte(content)
			case EncodingBase64:
				var err error
				got, err = base64.StdEncoding.DecodeString(content)
				if err != nil {
					t.Fatalf("base64 decode of EncodeContent output: %v", err)
				}
			}
			if !bytes.Equal(got, tc.data) {
				t.Fatalf("round trip mismatch: got %v, want %v", got, tc.data)
			}
		})
	}
}

// TestCommitEncodedSize checks the size helper sums the encoded content
// bytes across a commit's actions, which is what a caller compares against
// the content API's 20 MB rate-limit and 300 MB rejection thresholds before
// sending.
func TestCommitEncodedSize(t *testing.T) {
	c := Commit{
		Message:     "test commit",
		AuthorName:  "Dries",
		AuthorEmail: "dries@example.com",
		Actions: []FileAction{
			{Kind: ActionCreate, Path: "a.txt", Content: "hello", Encoding: EncodingText},
			{Kind: ActionUpdate, Path: "b.txt", Content: "world!", Encoding: EncodingText},
			{Kind: ActionDelete, Path: "c.txt"},
			{Kind: ActionMove, Path: "e.txt", PreviousPath: "d.txt"},
		},
	}
	want := int64(len("hello") + len("world!"))
	if got := CommitEncodedSize(c); got != want {
		t.Fatalf("CommitEncodedSize() = %d, want %d", got, want)
	}
}

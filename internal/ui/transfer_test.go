package ui

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/lullabot/sandbar/internal/browse"
	"github.com/lullabot/sandbar/internal/lima"
)

// copyArgsRunner records the argv of the `limactl copy` it is asked to stream, so
// a test can assert on the endpoints the UI actually hands to limactl rather than
// on an intermediate the UI computes and might not use.
type copyArgsRunner struct {
	mu   sync.Mutex
	args []string
}

func (r *copyArgsRunner) Output(context.Context, ...string) ([]byte, error) { return nil, nil }

func (r *copyArgsRunner) Stream(_ context.Context, _ io.Reader, _ io.Writer, args ...string) error {
	if len(args) > 0 && args[0] == "copy" {
		r.mu.Lock()
		r.args = append([]string(nil), args...)
		r.mu.Unlock()
	}
	return nil
}

func (r *copyArgsRunner) StreamOut(_ context.Context, _ io.Reader, _ io.Writer, _ ...string) error {
	return nil
}

func (r *copyArgsRunner) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.args
}

// The destination handed to limactl is the directory the user picked, VERBATIM —
// for a directory source exactly as for a file. lima.Copy places the source INSIDE
// the destination (scp's semantics, and why that backend is pinned), so appending
// the source's basename here would nest a directory one level too deep the moment
// the destination already contained it: a re-upload of mydir landing in
// dest/mydir/mydir. That appending is what this test replaced.
func TestTransferDestIsTheUsersDirectoryVerbatim(t *testing.T) {
	cases := []struct {
		name             string
		upload           bool
		recursive        bool
		destDir, srcPath string
		wantSrc, wantDst string
	}{
		{
			name: "upload a directory", upload: true, recursive: true,
			destDir: "/home/a.guest/proj", srcPath: "/Users/a/work/mydir",
			wantSrc: "/Users/a/work/mydir", wantDst: "web:/home/a.guest/proj",
		},
		{
			name: "download a directory", upload: false, recursive: true,
			destDir: "/Users/a/dl", srcPath: "/home/a.guest/proj/mydir",
			wantSrc: "web:/home/a.guest/proj/mydir", wantDst: "/Users/a/dl",
		},
		{
			name: "upload a file", upload: true, recursive: false,
			destDir: "/home/a.guest/proj", srcPath: "/Users/a/work/notes.txt",
			wantSrc: "/Users/a/work/notes.txt", wantDst: "web:/home/a.guest/proj",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &copyArgsRunner{}
			m := newTestModelWithCli(t, lima.New(rec))
			m.view = viewDest
			m.transferVM = "web"
			m.transferSrc = tc.srcPath
			m.transferUpload = tc.upload
			m.transferRecursive = tc.recursive
			m.dest, _ = browse.NewDestInput("Destination dir: ", tc.destDir, nil)

			after, cmd := m.Update(ctrlKey('s'))
			m = after.(model)
			l := newTeaLoop(t, m)
			l.exec(cmd)
			l.pump("the copy to reach limactl", func(model) bool { return rec.seen() != nil })

			args := rec.seen()
			src, dst := args[len(args)-2], args[len(args)-1]
			if src != tc.wantSrc || dst != tc.wantDst {
				t.Fatalf("limactl copy endpoints = (%q, %q), want (%q, %q)\nfull argv: %v",
					src, dst, tc.wantSrc, tc.wantDst, args)
			}
		})
	}
}

package paste

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/lullabot/sandbar/internal/clipboard"
	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/vm"
)

// fakeRunner records the argv AND stdin bytes of every call it is asked to
// make, so a test can assert exactly what would have reached a real limactl
// without spawning one — the same seam internal/lima/client_test.go's
// fakeRunner exercises (reimplemented here because that one is unexported to
// package lima).
type fakeRunner struct {
	calls    [][]string
	stdins   [][]byte
	streamed int
}

func (f *fakeRunner) Output(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	return nil, nil
}

func (f *fakeRunner) Stream(_ context.Context, stdin io.Reader, _ io.Writer, args ...string) error {
	f.streamed++
	f.calls = append(f.calls, args)
	var b []byte
	if stdin != nil {
		var err error
		b, err = io.ReadAll(stdin)
		if err != nil {
			return err
		}
	}
	f.stdins = append(f.stdins, b)
	return nil
}

func (f *fakeRunner) StreamOut(_ context.Context, stdin io.Reader, _ io.Writer, args ...string) error {
	return f.Stream(context.Background(), stdin, nil, args...)
}

// fakeProvider satisfies guestWriter: Shell delegates to a real *lima.Client
// wired to fakeRunner (so Client.Shell's own argv-building runs unmodified —
// only the Runner beneath it is faked), and GuestHome returns a fixed absolute
// path, standing in for a resolved lima.GuestHome (that resolution itself is
// covered by internal/lima/guest_test.go; this test is about what PasteImage
// does with the result).
type fakeProvider struct {
	core *lima.Client
	home string
}

func (p *fakeProvider) Shell(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error {
	return p.core.Shell(ctx, name, stdin, out, argv...)
}

func (p *fakeProvider) GuestHome(v vm.VM) string { return p.home }

func TestPasteImageNoImageTouchesNoGuest(t *testing.T) {
	orig := readClipboardImage
	defer func() { readClipboardImage = orig }()
	readClipboardImage = func() ([]byte, error) { return nil, clipboard.ErrNoImage }

	r := &fakeRunner{}
	p := &fakeProvider{core: lima.New(r), home: "/home/u.guest"}

	got, err := PasteImage(context.Background(), p, vm.VM{Name: "web"})
	if err != nil {
		t.Fatalf("PasteImage() error: %v", err)
	}
	if got.Status != NoImage {
		t.Fatalf("Status = %v, want NoImage", got.Status)
	}
	if len(r.calls) != 0 {
		t.Fatalf("got %d guest calls, want 0: %v", len(r.calls), r.calls)
	}
}

func TestPasteImageStagesOverStdinToAbsolutePath(t *testing.T) {
	orig := readClipboardImage
	defer func() { readClipboardImage = orig }()
	png := []byte("\x89PNG\r\n\x1a\nfake-bytes")
	readClipboardImage = func() ([]byte, error) { return png, nil }

	r := &fakeRunner{}
	p := &fakeProvider{core: lima.New(r), home: "/home/u.guest"}

	got, err := PasteImage(context.Background(), p, vm.VM{Name: "web"})
	if err != nil {
		t.Fatalf("PasteImage() error: %v", err)
	}
	if got.Status != Staged {
		t.Fatalf("Status = %v, want Staged", got.Status)
	}
	wantPath := "/home/u.guest/.sand/clip/latest.png"
	if got.GuestPath != wantPath {
		t.Fatalf("GuestPath = %q, want %q", got.GuestPath, wantPath)
	}

	if len(r.calls) != 1 {
		t.Fatalf("got %d guest calls, want exactly 1: %v", len(r.calls), r.calls)
	}
	wantArgv := []string{
		"shell", "web", "sh", "-c",
		`mkdir -p -- "$1"; chmod 700 "$1"; cat > "$2"; chmod 600 "$2"`,
		"sand", "/home/u.guest/.sand/clip", wantPath,
	}
	if !reflect.DeepEqual(r.calls[0], wantArgv) {
		t.Fatalf("argv = %v, want %v", r.calls[0], wantArgv)
	}
	if len(r.stdins) != 1 || !bytes.Equal(r.stdins[0], png) {
		t.Fatalf("stdin = %v, want the clipboard bytes %v", r.stdins, png)
	}

	// The bytes must never appear as an argv element — only on stdin.
	for _, a := range r.calls[0] {
		if a == string(png) {
			t.Fatalf("image bytes leaked into argv: %v", r.calls[0])
		}
	}
}

func TestPasteImagePropagatesOtherClipboardErrors(t *testing.T) {
	orig := readClipboardImage
	defer func() { readClipboardImage = orig }()
	wantErr := errors.New("boom")
	readClipboardImage = func() ([]byte, error) { return nil, wantErr }

	r := &fakeRunner{}
	p := &fakeProvider{core: lima.New(r), home: "/home/u.guest"}

	_, err := PasteImage(context.Background(), p, vm.VM{Name: "web"})
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("PasteImage() error = %v, want wrapping %v", err, wantErr)
	}
	if len(r.calls) != 0 {
		t.Fatalf("got %d guest calls, want 0 on a clipboard error: %v", len(r.calls), r.calls)
	}
}

//go:build linux

package clipboard

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"
)

// fakeCall records one invocation of the injectable run seam, so a test can
// assert exactly which commands were (and were not) issued.
type fakeCall struct {
	name string
	args []string
}

func containsArg(args []string, val string) bool {
	for _, a := range args {
		if a == val {
			return true
		}
	}
	return false
}

// withFakeRun substitutes the package's run seam for the duration of the
// calling test and restores the original on cleanup.
func withFakeRun(t *testing.T, fn func(ctx context.Context, name string, args ...string) ([]byte, error)) *[]fakeCall {
	t.Helper()
	var calls []fakeCall
	orig := run
	t.Cleanup(func() { run = orig })
	run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, fakeCall{name: name, args: append([]string(nil), args...)})
		return fn(ctx, name, args...)
	}
	return &calls
}

// This is the security-critical case: a clipboard advertising ONLY text/plain
// must yield ErrNoImage AND must never issue any command that could fetch
// clipboard content (image or text). Proves the gate-then-fetch property —
// a text clipboard produces zero fetched bytes.
func TestReadImagePNG_TextOnlyClipboardReturnsErrNoImageWithoutFetch(t *testing.T) {
	calls := withFakeRun(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "xclip" && containsArg(args, "TARGETS") {
			return []byte("text/plain\nUTF8_STRING\n"), nil
		}
		return nil, fmt.Errorf("unexpected command: %s %v", name, args)
	})

	_, err := ReadImagePNG()
	if !errors.Is(err, ErrNoImage) {
		t.Fatalf("ReadImagePNG() error = %v, want ErrNoImage", err)
	}
	for _, c := range *calls {
		if containsArg(c.args, "image/png") {
			t.Fatalf("unexpected image/png fetch call recorded: %s %v", c.name, c.args)
		}
	}
}

// When TARGETS advertises image/png, the gate passes and the fetch runs,
// returning exactly the fetched bytes.
func TestReadImagePNG_ImagePNGAdvertisedFetchesBytes(t *testing.T) {
	want := []byte("PNGDATA")
	withFakeRun(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "xclip" {
			switch {
			case containsArg(args, "TARGETS"):
				return []byte("image/png\ntext/plain\n"), nil
			case containsArg(args, "image/png"):
				return want, nil
			}
		}
		return nil, fmt.Errorf("unexpected command: %s %v", name, args)
	})

	got, err := ReadImagePNG()
	if err != nil {
		t.Fatalf("ReadImagePNG() error = %v, want nil", err)
	}
	if string(got) != string(want) {
		t.Fatalf("ReadImagePNG() = %q, want %q", got, want)
	}
}

// A non-PNG image type (e.g. only image/jpeg advertised) is treated as no
// image in v1 — no converter, no fetch of the jpeg bytes.
func TestReadImagePNG_NonPNGImageTypeReturnsErrNoImageWithoutFetch(t *testing.T) {
	calls := withFakeRun(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "xclip" && containsArg(args, "TARGETS") {
			return []byte("image/jpeg\n"), nil
		}
		return nil, fmt.Errorf("unexpected command: %s %v", name, args)
	})

	_, err := ReadImagePNG()
	if !errors.Is(err, ErrNoImage) {
		t.Fatalf("ReadImagePNG() error = %v, want ErrNoImage", err)
	}
	for _, c := range *calls {
		if len(c.args) > 0 && (containsArg(c.args, "image/png") || containsArg(c.args, "image/jpeg")) && !containsArg(c.args, "TARGETS") {
			t.Fatalf("unexpected fetch call recorded: %s %v", c.name, c.args)
		}
	}
}

// When xclip's binary is absent, the gate falls back to wl-paste -l.
func TestReadImagePNG_XclipAbsentFallsBackToWlPaste(t *testing.T) {
	want := []byte("WLPNGDATA")
	withFakeRun(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "xclip":
			return nil, exec.ErrNotFound
		case "wl-paste":
			switch {
			case containsArg(args, "-l"):
				return []byte("image/png\n"), nil
			case containsArg(args, "image/png"):
				return want, nil
			}
		}
		return nil, fmt.Errorf("unexpected command: %s %v", name, args)
	})

	got, err := ReadImagePNG()
	if err != nil {
		t.Fatalf("ReadImagePNG() error = %v, want nil", err)
	}
	if string(got) != string(want) {
		t.Fatalf("ReadImagePNG() = %q, want %q", got, want)
	}
}

// When neither xclip nor wl-paste is present, the platform is treated as
// unsupported rather than erroring on a missing type.
func TestReadImagePNG_NeitherBinaryPresentReturnsErrUnsupported(t *testing.T) {
	withFakeRun(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, exec.ErrNotFound
	})

	_, err := ReadImagePNG()
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("ReadImagePNG() error = %v, want ErrUnsupported", err)
	}
}

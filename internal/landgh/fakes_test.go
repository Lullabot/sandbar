package landgh

import (
	"context"
	"errors"
)

// fakeRunner records the argv of every call and returns a canned response
// keyed by the exact argv (joined with a single separator unlikely to appear
// in gh args), so a test can pin distinct outputs to distinct calls without
// caring about call order. It never spawns a real gh binary.
type fakeRunner struct {
	calls   [][]string
	outputs map[string][]byte
	errs    map[string]error
	// err, when non-nil, is returned for every call regardless of key — used
	// to simulate "gh missing/unauthenticated" across the board.
	err error
}

func argvKey(args []string) string {
	key := ""
	for i, a := range args {
		if i > 0 {
			key += "\x00"
		}
		key += a
	}
	return key
}

func (f *fakeRunner) Output(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if f.err != nil {
		return nil, f.err
	}
	key := argvKey(args)
	return f.outputs[key], f.errs[key]
}

// sequentialRunner records argv like fakeRunner but returns canned
// output/error by call ORDER, for exercising a multi-step call chain (e.g.
// CreateDraftPR's default-branch lookup, then commit-message lookup, then
// the create POST) where each step's canned response differs from the last
// even though their argv shapes overlap.
type sequentialRunner struct {
	calls   [][]string
	outputs [][]byte
	errs    []error
	i       int
}

func (r *sequentialRunner) Output(_ context.Context, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	idx := r.i
	r.i++
	var out []byte
	var err error
	if idx < len(r.outputs) {
		out = r.outputs[idx]
	}
	if idx < len(r.errs) {
		err = r.errs[idx]
	}
	return out, err
}

// errGhMissing simulates exec.LookPath's "not found" failure mode.
var errGhMissing = errors.New("exec: \"gh\": executable file not found in $PATH")

// fakeOpener records every URL it was asked to open and never launches
// anything.
type fakeOpener struct {
	calls []string
	err   error
}

func (f *fakeOpener) open(_ context.Context, target string) error {
	f.calls = append(f.calls, target)
	return f.err
}

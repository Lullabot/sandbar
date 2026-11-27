package provision

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/vm"
)

// limaHomeRunner models one Lima home shared by several sandbar processes: the
// instances limactl knows, AND their directories on disk under LIMA_HOME, which
// is what cleanupInstance stats and removes. It answers the way limactl 2.2.1
// does where that matters here (each checked against the real binary):
//
//   - `list <name>` for an unknown name fails ("No instance matching <name>
//     found." / "unmatched instances", exit 1);
//   - `clone <src> <dst>` onto an existing <dst> fails ("instance `dst` already
//     exists", exit 1) and leaves the existing directory untouched;
//   - `delete <name> -f` kills a running instance and removes its directory.
//
// hook runs outside the lock before a call is answered, so a test can hold one
// process at a precise point while the other runs.
type limaHomeRunner struct {
	mu     sync.Mutex
	home   string
	status map[string]string
	calls  [][]string
	hook   func([]string)
}

func (r *limaHomeRunner) record(args []string) {
	r.mu.Lock()
	r.calls = append(r.calls, args)
	r.mu.Unlock()
	if r.hook != nil {
		r.hook(args)
	}
}

func (r *limaHomeRunner) snapshot() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.calls...)
}

func (r *limaHomeRunner) Output(ctx context.Context, args ...string) ([]byte, error) {
	r.record(args)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case len(args) >= 2 && args[0] == "list":
		st, ok := r.status[args[1]]
		if !ok {
			return nil, fmt.Errorf("exit status 1: No instance matching %s found.\nunmatched instances", args[1])
		}
		return []byte(st + "\n"), nil
	case len(args) >= 2 && args[0] == "delete":
		delete(r.status, args[1])
		return nil, os.RemoveAll(filepath.Join(r.home, args[1]))
	case len(args) >= 2 && (args[0] == "shell" || args[0] == "edit"):
		name := args[1]
		if args[0] == "edit" {
			name = args[len(args)-1]
		}
		if _, ok := r.status[name]; !ok {
			return nil, fmt.Errorf("exit status 1: instance %q does not exist", name)
		}
	}
	return nil, nil
}

func (r *limaHomeRunner) Stream(ctx context.Context, _ io.Reader, _ io.Writer, args ...string) error {
	r.record(args)
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case len(args) == 3 && args[0] == "clone":
		dst := args[2]
		if _, err := os.Stat(filepath.Join(r.home, dst)); err == nil {
			return errors.New("exit status 1") // "instance `dst` already exists"; the message goes to the stream
		}
		if err := os.MkdirAll(filepath.Join(r.home, dst), 0o700); err != nil {
			return err
		}
		r.status[dst] = "Stopped"
	case len(args) >= 2 && (args[0] == "start" || args[0] == "stop" || args[0] == "shell"):
		name := args[1]
		if _, ok := r.status[name]; !ok {
			return fmt.Errorf("exit status 1: instance %q does not exist", name)
		}
		switch args[0] {
		case "start":
			r.status[name] = "Running"
		case "stop":
			r.status[name] = "Stopped"
		}
	}
	return nil
}

func (r *limaHomeRunner) StreamOut(ctx context.Context, stdin io.Reader, out io.Writer, args ...string) error {
	return r.Stream(ctx, stdin, out, args...)
}

// TestConcurrentCreatesOfOneNameNeverDeleteTheWinner drives two sandbar
// processes creating the same VM name at once, in the one interleaving that
// matters: both pass the "already exists" guard, then queue on the base lock. The
// first clones the VM, releases the lock and starts booting it; only then does
// the second get the lock. The second must give up with "already exists" and
// leave the first one's VM alone. It used to try the clone anyway, fail because
// the instance directory existed, and run its failed-clone cleanup, which found
// that directory and force-deleted the other process's booting VM.
//
// The interleaving is forced with barriers, not sleeps: both guards must have
// answered before either process goes on, and the winner's boot is held until
// the loser has returned.
func TestConcurrentCreatesOfOneNameNeverDeleteTheWinner(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIMA_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "sandbar-base"), 0o700); err != nil {
		t.Fatal(err)
	}

	loserDone := make(chan struct{})
	var guards sync.WaitGroup
	guards.Add(2)
	var gmu sync.Mutex
	guardCalls, starts := 0, 0
	r := &limaHomeRunner{home: home, status: map[string]string{"sandbar-base": "Stopped"}}
	r.hook = func(args []string) {
		switch {
		case len(args) >= 2 && args[0] == "list" && args[1] == "web":
			gmu.Lock()
			guardCalls++
			first := guardCalls <= 2
			gmu.Unlock()
			if first { // the two processes' guards: neither proceeds until both have looked
				guards.Done()
				guards.Wait()
			}
		case len(args) == 2 && args[0] == "start" && args[1] == "web":
			gmu.Lock()
			starts++
			winner := starts == 1
			gmu.Unlock()
			if winner { // the winner is booting: hold it there until the loser is done
				<-loserDone
			}
		}
	}

	cfg := vm.CreateConfig{Name: "web", BaseName: "sandbar-base", User: "andrew", CPUs: 2, Memory: "2GiB", Disk: "20GiB"}
	errs := make(chan error, 2)
	var once sync.Once
	for range 2 {
		go func() {
			p := &Provisioner{Lima: lima.New(r), PlaybookDir: "/playbook"}
			err := p.CreateVM(context.Background(), cfg, io.Discard)
			errs <- err
			once.Do(func() { close(loserDone) })
		}()
	}

	var got []error
	for range 2 {
		select {
		case err := <-errs:
			got = append(got, err)
		case <-time.After(30 * time.Second):
			t.Fatalf("creates did not finish (deadlock?); calls so far: %v", r.snapshot())
		}
	}

	for _, c := range r.snapshot() {
		if len(c) >= 2 && c[0] == "delete" && c[1] == "web" {
			t.Errorf("a create force-deleted %q, which only the other create had made: %v", "web", c)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "web")); err != nil {
		t.Errorf("the winning create's instance directory is gone: %v", err)
	}
	var ok, exists int
	for _, err := range got {
		switch {
		case err == nil:
			ok++
		case strings.Contains(err.Error(), "already exists"):
			exists++
		default:
			t.Errorf("unexpected create error: %v", err)
		}
	}
	if ok != 1 || exists != 1 {
		t.Errorf("want one create to succeed and the other to report \"already exists\"; got %v", got)
	}
}

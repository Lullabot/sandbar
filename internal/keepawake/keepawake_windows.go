//go:build windows

package keepawake

import (
	"fmt"
	"runtime"

	"golang.org/x/sys/windows"
)

// EXECUTION_STATE flags, from winbase.h. ES_SYSTEM_REQUIRED alone resets the
// idle timer once; with ES_CONTINUOUS it holds until the state is cleared.
// ES_DISPLAY_REQUIRED is deliberately absent: the display may sleep and the
// screen may lock.
const (
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001
)

var procSetThreadExecutionState = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadExecutionState")

// acquireOS sets the execution state on a dedicated, locked OS thread.
// SetThreadExecutionState is PER THREAD: set from an ordinary goroutine, the
// state would sit on whichever thread the scheduler happened to use, and a
// clear issued later from another thread would leave it behind. So one
// goroutine owns the whole lifetime — set, wait, clear — on one thread, and
// exits without unlocking, so the runtime retires that thread too (Windows
// drops a thread's execution state when the thread ends).
//
// No reason string: Windows' SetThreadExecutionState has nowhere to show one.
// (PowerCreateRequest does, but is not worth the extra surface for a platform
// sandbar does not ship on.)
func acquireOS(string) (func() error, error) {
	if err := procSetThreadExecutionState.Find(); err != nil {
		return nil, fmt.Errorf("keepawake: %w", err)
	}
	started := make(chan error, 1)
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		// Returns the previous state, or 0 on failure.
		if prev, _, err := procSetThreadExecutionState.Call(esContinuous | esSystemRequired); prev == 0 {
			started <- fmt.Errorf("keepawake: SetThreadExecutionState: %w", err)
			return
		}
		started <- nil
		<-stop
		if prev, _, err := procSetThreadExecutionState.Call(esContinuous); prev == 0 {
			done <- fmt.Errorf("keepawake: SetThreadExecutionState clear: %w", err)
			return
		}
		done <- nil
	}()
	if err := <-started; err != nil {
		return nil, err
	}
	return func() error {
		close(stop)
		return <-done
	}, nil
}

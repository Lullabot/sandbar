//go:build unix

package lima

import "syscall"

// flock(2) is the lock for every platform sand has shipped to date (linux and
// darwin, per .goreleaser). It is advisory, held per open file description, and
// dropped when the descriptor closes -- so localLock.Close doubles as a release
// even if Unlock was never reached.

func (l *localLock) TryLock() (bool, error) {
	err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if err == syscall.EWOULDBLOCK {
		return false, nil // held by someone else — wait and retry
	}
	return false, err
}

func (l *localLock) Unlock() error { return syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN) }

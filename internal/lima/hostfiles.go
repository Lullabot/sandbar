package lima

// hostfiles.go is the second half of the host-access seam. The first half,
// Runner (runner.go), abstracts RUNNING limactl; this half abstracts READING the
// files limactl leaves on the host it runs on — the per-instance ssh.config /
// cloud-config.yaml / lima.yaml, the qcow2 disk image, sand's own version stamp
// and base lock, and the pid/log files the up-since / last-used sampling reads.
//
// Today those two things are the same machine, and the local implementation here
// is byte-for-byte the direct os.* / filepath calls it replaces. The seam exists
// because they need not stay the same machine: the remote-Lima provider
// (sshhost.go) runs limactl over SSH, and then the instance files live on the
// REMOTE host — reading them off the local filesystem would return nothing. That
// provider satisfies the same interface by reading over SSH, and every caller
// that goes through the seam works remotely without knowing it moved.

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// Host is the full host-access seam: the machine limactl runs on, exposing both
// its subprocess execution (Runner) and its Lima instance files (HostFiles).
// Local Lima is a Runner plus localFiles; the remote-Lima provider implements
// the whole thing over SSH (sshhost.go). Nothing in this file constructs a
// Host — it is the interface the providers (internal/provider) are built
// against; the local callers here reach the two halves through Runner and
// LocalFiles() directly.
type Host interface {
	Runner
	HostFiles
}

// HostFiles reads and mutates the Lima instance state on the host where limactl
// runs. Paths are absolute as that host sees them (for local Lima, this machine;
// for remote Lima, the remote host). The local implementation (localFiles) is a
// thin wrapper over os.*; a remote implementation satisfies the same methods over
// SSH.
type HostFiles interface {
	// ReadFile reads the file at path. A missing file must report an error
	// satisfying errors.Is(err, fs.ErrNotExist), as os.ReadFile does.
	ReadFile(path string) ([]byte, error)
	// Stat stats path. A missing path must report an error satisfying
	// errors.Is(err, fs.ErrNotExist) so callers can tell "absent" from "present
	// but unreadable" exactly as they do against the local filesystem.
	Stat(path string) (fs.FileInfo, error)
	// WriteFile writes data to path, first creating parent directories with
	// dirPerm (os.MkdirAll) and then the file with filePerm (os.WriteFile).
	WriteFile(path string, data []byte, dirPerm, filePerm fs.FileMode) error
	// MkdirAll creates path and any missing parents with perm.
	MkdirAll(path string, perm fs.FileMode) error
	// RemoveAll removes path and any children (os.RemoveAll semantics): a
	// non-existent path is not an error.
	RemoveAll(path string) error
	// DiskAllocBytes returns the ALLOCATED on-disk size of the file at path
	// (st_blocks × 512) — a qcow2's sparse size, not its virtual length — or -1
	// when it cannot be measured. Allocated blocks is a platform-specific probe,
	// so the non-unix build returns -1.
	DiskAllocBytes(path string) int64
	// LimaHome is the Lima home on the limactl host: $LIMA_HOME, else ~/.lima,
	// else "" when the home directory cannot be resolved. Both Lima's own
	// per-instance state and sand's state ABOUT an instance (the base version
	// stamp and its lock, under _sand/) live beneath it.
	LimaHome() string
	// StagePlaybook makes the playbook directory localDir available on the host
	// where limactl runs and returns the path to mount as /mnt/playbook. For local
	// Lima that host IS this machine, so it returns localDir unchanged. For remote
	// Lima the playbook lives on the laptop but limactl (and its bind mount) run on
	// the remote host, so the implementation copies the fileset to a stable path
	// under the remote LimaHome and returns THAT path — otherwise `limactl start`
	// would mount a directory that does not exist on the remote host. The staged
	// copy is left in place (refreshed each build): the base overlay's mount points
	// at it, and a clone's finalize still bind-mounts it, so it must outlive the
	// build that created it, exactly as the local checkout/extracted dir does.
	StagePlaybook(ctx context.Context, localDir string) (mountPath string, err error)
	// OpenLock opens or creates the advisory lock file at path (which the caller
	// has already ensured a parent directory for) with perm, returning a LockFile
	// the base-image serializer flocks. See internal/provision/baselock.go.
	OpenLock(path string, perm fs.FileMode) (LockFile, error)
}

// LockFile is an advisory exclusive lock on a file on the limactl host. It backs
// the base-image lock that serializes base preparation across concurrent creates
// and across separate sand processes (internal/provision/baselock.go).
type LockFile interface {
	// TryLock attempts a non-blocking exclusive lock. It returns (true, nil) on
	// success, (false, nil) when another holder has it (the caller should wait and
	// retry), and (false, err) on any other failure (the caller proceeds
	// unserialized rather than failing the build).
	TryLock() (acquired bool, err error)
	// Unlock releases a held lock.
	Unlock() error
	// Close releases the underlying handle.
	Close() error
}

// localFiles is the HostFiles implementation for local Lima: the real local
// filesystem, exactly as the callers touched it before the seam existed.
type localFiles struct{}

// LocalFiles returns the host-access file seam for local Lima — the default
// everywhere, and the implementation a remote provider swaps out.
func LocalFiles() HostFiles { return localFiles{} }

func (localFiles) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (localFiles) Stat(path string) (fs.FileInfo, error) { return os.Stat(path) }

func (localFiles) MkdirAll(path string, perm fs.FileMode) error { return os.MkdirAll(path, perm) }

func (localFiles) RemoveAll(path string) error { return os.RemoveAll(path) }

func (localFiles) WriteFile(path string, data []byte, dirPerm, filePerm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return err
	}
	return os.WriteFile(path, data, filePerm)
}

func (localFiles) LimaHome() string {
	if home := os.Getenv("LIMA_HOME"); home != "" {
		return home
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".lima")
	}
	return ""
}

// StagePlaybook is a no-op for local Lima: limactl runs on this machine, so it
// bind-mounts localDir directly — nothing to copy anywhere.
func (localFiles) StagePlaybook(_ context.Context, localDir string) (string, error) {
	return localDir, nil
}

func (localFiles) OpenLock(path string, perm fs.FileMode) (LockFile, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, perm)
	if err != nil {
		return nil, err
	}
	return &localLock{f: f}, nil
}

// localLock flocks a real *os.File. syscall.Flock is intentionally unguarded by a
// build tag: sand runs only where Lima does (Linux and macOS, per .goreleaser),
// and Flock exists on both — the same reasoning baselock.go's doc comment gives
// for not shipping a non-unix fallback.
type localLock struct{ f *os.File }

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

func (l *localLock) Close() error { return l.f.Close() }

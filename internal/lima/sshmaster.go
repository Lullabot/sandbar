package lima

import (
	"errors"
	"path/filepath"
)

// ErrHostCommandsUnavailable is returned by Client.HostOutput when the Runner
// cannot run a host command, as with the test fakes. Callers whose host command
// is best-effort can recognise it and stay quiet.
var ErrHostCommandsUnavailable = errors.New("lima: host commands unavailable")

// StopSSHMasterArgv builds the host argv that stops Lima's shared ssh master for
// the instance in instanceDir, so the next `limactl shell` authenticates a new
// guest login instead of riding the old one.
//
// Lima runs every ssh to an instance with ControlMaster=auto, ControlPersist=yes
// and ControlPath=<instance dir>/ssh.sock, and its host agent opens that master
// at boot for the read-only /mnt/playbook mount. The guest login behind it is
// therefore older than finalize, and keeps the supplementary groups it was
// authenticated with: sshd forks every multiplexed session from that one
// post-auth process without calling initgroups() again. So a login finalize has
// just added to `docker` got shells without that group until the VM restarted.
//
// `-O stop` makes the master stop accepting sessions and remove its socket
// without cutting off the ones it already carries, so the playbook mount stays
// up; `-O exit` would end them. -F /dev/null keeps the user's ssh_config out of
// it, as Lima does. The destination is a placeholder ssh requires
// syntactically; with an explicit ControlPath it is never resolved.
func StopSSHMasterArgv(instanceDir string) []string {
	return []string{
		"ssh", "-F", "/dev/null",
		"-o", "ControlPath=" + filepath.Join(instanceDir, "ssh.sock"),
		"-O", "stop", "lima-instance",
	}
}

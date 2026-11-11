//go:build linux

package keepawake

import (
	"context"
	"fmt"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

// busTimeout bounds the whole logind exchange (connect, authenticate, Inhibit).
// A missing or wedged system bus must cost the caller a moment, not stall the
// start of a reset.
const busTimeout = 2 * time.Second

// acquireOS takes a systemd-logind inhibitor lock. "sleep:idle" in "block" mode
// stops logind (and desktop power managers that ask it to suspend) from
// sleeping the machine while the lock is held. logind's default
// LidSwitchIgnoreInhibited=yes still lets a lid close suspend.
//
// Inhibit returns a file descriptor: the lock lasts exactly as long as some
// process holds that fd open, so closing it is the release. The D-Bus
// connection is NOT needed to keep the lock, and is closed before returning —
// godbus hands back the raw fd it received via SCM_RIGHTS without tracking it,
// so it is ours alone and survives the connection's Close.
//
// No logind (a container, a non-systemd distribution) surfaces as an error,
// which Hold turns into a no-op. So does polkit refusing the lock: a user in
// an active local desktop session may take a sleep inhibitor by default, but
// a caller logind cannot tie to one — sandbar run over SSH, typically — gets
// "Interactive authentication required" (inhibit-block-sleep's allow_any is
// auth_admin_keep). That machine's idle sleep is not the workstation's
// anyway, so the refusal costs nothing.
func acquireOS(reason string) (func() error, error) {
	ctx, cancel := context.WithTimeout(context.Background(), busTimeout)
	defer cancel()

	// WithContext makes godbus close the connection when ctx expires, which
	// is what unblocks a stalled authentication handshake: Connect itself
	// takes no context.
	conn, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("keepawake: connect to system bus: %w", err)
	}
	defer conn.Close()

	var fd dbus.UnixFD
	err = conn.Object("org.freedesktop.login1", "/org/freedesktop/login1").
		CallWithContext(ctx, "org.freedesktop.login1.Manager.Inhibit", 0,
			"sleep:idle", "sandbar", reason, "block").
		Store(&fd)
	if err != nil {
		return nil, fmt.Errorf("keepawake: logind Inhibit: %w", err)
	}

	// sandbar starts ssh and limactl children for the whole of the operation
	// this lock guards. An inheritable fd would let a child that outlives
	// the release (a ControlMaster ssh, say) keep the machine awake
	// indefinitely. Go's ReadMsgUnix already requests MSG_CMSG_CLOEXEC on
	// Linux; setting it again is cheap and does not depend on that detail.
	syscall.CloseOnExec(int(fd))

	return func() error { return syscall.Close(int(fd)) }, nil
}

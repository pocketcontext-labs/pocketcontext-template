//go:build !windows

package bundle

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive, non-blocking lock that the OS releases when the
// process exits, so a crashed server never leaves a stale lock.
func lockFile(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }

// detach starts the server in its own session, independent of the caller's terminal.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

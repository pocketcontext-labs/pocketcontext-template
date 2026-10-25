//go:build windows

package bundle

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive, non-blocking lock that Windows releases when the
// process exits, so a crashed server never leaves a stale lock.
func lockFile(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
}

// detach starts the server without a console, outliving the agent's shell.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
}

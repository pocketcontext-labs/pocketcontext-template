//go:build !windows

package tracing

import (
	"os"
	"syscall"
)

const openFlags = syscall.O_NOFOLLOW | syscall.O_NONBLOCK

func privateMode(info os.FileInfo) bool { return info.Mode().Perm()&0077 == 0 }

//go:build !windows

package database

import "os"

func privateMode(info os.FileInfo) bool { return info.Mode().Perm()&0077 == 0 }

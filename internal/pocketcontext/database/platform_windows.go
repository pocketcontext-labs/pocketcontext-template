//go:build windows

package database

import "os"

// Mode bits do not describe Windows ACLs; the marker lives in the user's private cache.
func privateMode(os.FileInfo) bool { return true }

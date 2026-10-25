//go:build windows

package tracing

import "os"

// Windows has no O_NOFOLLOW; trace files are opened inside the user's private
// profile. Mode bits do not describe Windows ACLs, so they cannot prove privacy.
const openFlags = 0

func privateMode(os.FileInfo) bool { return true }

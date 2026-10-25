// Package sqliteuri builds SQLite file: URIs that are valid on every platform.
package sqliteuri

import (
	"net/url"
	"path/filepath"
	"strings"
)

// File returns the file: URI for an absolute path. Windows paths use forward
// slashes and a leading slash (file:///C:/...); a backslash path would otherwise
// be encoded as a URI authority, which SQLite rejects.
func File(abs string) url.URL {
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return url.URL{Scheme: "file", Path: p}
}

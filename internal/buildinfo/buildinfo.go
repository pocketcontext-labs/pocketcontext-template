// Package buildinfo holds the values stamped at build time.
package buildinfo

var (
	// Version is the release, set with -ldflags -X.
	Version = "dev"
	// Commit is the Git revision of the build.
	Commit = ""
)

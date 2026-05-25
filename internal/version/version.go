// Package version exposes build-time identifiers stitched in via -ldflags.
package version

var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

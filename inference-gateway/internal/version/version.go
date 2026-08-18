// Package version exposes build metadata injected by Makefile/Docker linker flags.
package version

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func Version() string { return version }
func Commit() string  { return commit }
func Date() string    { return date }

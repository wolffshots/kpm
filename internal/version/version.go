// Package version holds the compiled-in kpm version string.
package version

// Version is set at build time via -ldflags "-X kpm/internal/version.Version=<v>".
var Version = "dev"

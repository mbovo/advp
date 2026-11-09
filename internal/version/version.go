// Package version holds build metadata, injected at link time with
// -ldflags "-X github.com/mbovo/advp/internal/version.Version=...".
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Build metadata; overridden at link time.
var (
	Version = "dev" // semantic version, e.g. v1.2.3
	Commit  = ""    // git commit hash
	Date    = ""    // build date (RFC 3339)
)

func init() {
	// Fall back to VCS data embedded by the Go toolchain (go install / go build).
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	if Version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		Version = info.Main.Version
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if Commit == "" {
				Commit = s.Value
			}
		case "vcs.time":
			if Date == "" {
				Date = s.Value
			}
		}
	}
}

// String returns a human readable version line.
func String() string {
	c := Commit
	if len(c) > 12 {
		c = c[:12]
	}
	s := "advp " + Version
	if c != "" {
		s += " (" + c
		if Date != "" {
			s += ", " + Date
		}
		s += ")"
	}
	return fmt.Sprintf("%s %s/%s %s", s, runtime.GOOS, runtime.GOARCH, runtime.Version())
}

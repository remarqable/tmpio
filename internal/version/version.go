// Package version reports which build a running instance came from.
//
// Without this, the only way to tell one deployment from another is to notice
// a behaviour change, which is a slow and unreliable way to discover that a
// pull fetched the same image twice.
package version

import (
	"runtime/debug"
	"time"
)

// Set by the linker at build time:
//
//	-X github.com/remarqable/tmpio/internal/version.Version=v1.3.0
//	-X github.com/remarqable/tmpio/internal/version.Date=2026-09-20
var (
	Version string
	Date    string
)

// Info returns the version and build date. When the linker flags were not set
// — a plain `go build`, or `go run` during development — it falls back to the
// stamp the Go toolchain records from git, so a developer build still says
// something true rather than nothing.
func Info() (string, string) {
	v, d := Version, Date
	if v == "" || d == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				switch s.Key {
				case "vcs.revision":
					if v == "" && len(s.Value) >= 7 {
						v = s.Value[:7]
					}
				case "vcs.time":
					if d == "" {
						if t, err := time.Parse(time.RFC3339, s.Value); err == nil {
							d = t.UTC().Format("2006-01-02")
						}
					}
				}
			}
		}
	}
	if v == "" {
		v = "dev"
	}
	return v, d
}

// String is the one-line form shown in the interface: "v1.3.0 · 2026-09-20",
// or just the version when there is no date to show.
func String() string {
	v, d := Info()
	if d == "" {
		return v
	}
	return v + " · " + d
}

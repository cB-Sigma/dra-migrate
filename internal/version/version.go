// Package version holds build information, set with -ldflags at release time.
package version

import "runtime/debug"

// Build information.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// URL is the project home page.
const URL = "https://github.com/cb-Sigma/dra-migrate"

// String returns a human readable version.
func String() string {
	v := Version
	if v == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			v = bi.Main.Version
		}
	}
	if Commit != "" {
		v += " (" + Commit
		if Date != "" {
			v += ", " + Date
		}
		v += ")"
	}
	return v
}

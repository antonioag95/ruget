package main

import (
	"fmt"
	"runtime/debug"
)

const (
	appName = "ruget"
	author  = "antonioag95"
)

// version is the release version. Override at build time with:
//
//	go build -ldflags "-X main.version=1.2.3" -o ruget.exe .
var version = "1.1.0"

// buildInfo returns a compact "commit <sha> (<date>)" string when VCS metadata
// is embedded in the binary, otherwise "".
func buildInfo() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var rev, when string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			when = s.Value
		}
	}
	if rev == "" {
		return ""
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if when != "" {
		return fmt.Sprintf("commit %s (%s)", rev, when)
	}
	return "commit " + rev
}

// fullVersion renders a one-line version string, e.g. "ruget v1.0.0".
func fullVersion() string {
	line := fmt.Sprintf("%s v%s", appName, version)
	if b := buildInfo(); b != "" {
		line += " · " + b
	}
	return line
}

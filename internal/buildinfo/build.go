package buildinfo

import "runtime/debug"

// Commit, Date, and GoVersion are set at build time via -ldflags.
// Example:
//
//	go build -ldflags "-X github.com/toasterbook88/axis/internal/buildinfo.Commit=abc1234
//	                    -X github.com/toasterbook88/axis/internal/buildinfo.Date=2026-04-01T12:00:00Z
//	                    -X github.com/toasterbook88/axis/internal/buildinfo.GoVersion=go1.26.2"
//
// When those ldflags are empty, ResolvedCommit and ResolvedDate fall back to
// the vcs.revision and vcs.time settings the Go toolchain stamps into the binary.
var (
	Commit    string
	Date      string
	GoVersion string
)

// ResolvedCommit returns the ldflags commit, or the toolchain vcs.revision.
func ResolvedCommit() string {
	if Commit != "" {
		return Commit
	}
	return setting("vcs.revision")
}

// ResolvedDate returns the ldflags build time, or the toolchain vcs.time.
func ResolvedDate() string {
	if Date != "" {
		return Date
	}
	return setting("vcs.time")
}

func setting(key string) string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info == nil {
		return ""
	}
	return settingFrom(info.Settings, key)
}

func settingFrom(settings []debug.BuildSetting, key string) string {
	for _, s := range settings {
		if s.Key == key {
			return s.Value
		}
	}
	return ""
}

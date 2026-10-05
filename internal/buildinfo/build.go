package buildinfo

import "runtime/debug"

// Commit, Date, and GoVersion are set at build time via -ldflags.
// Example:
//
//	go build -ldflags "-X github.com/toasterbook88/axis/internal/buildinfo.Commit=abc1234
//	                    -X github.com/toasterbook88/axis/internal/buildinfo.Date=2026-04-01T12:00:00Z
//	                    -X github.com/toasterbook88/axis/internal/buildinfo.GoVersion=go1.26.2"
//
// When the commit ldflag is empty, ResolvedCommit uses vcs.revision and
// appends -dirty when vcs.modified is true. ResolvedDate is only the ldflag
// build time. vcs.time is the revision time and is not a build time.
var (
	Commit    string
	Date      string
	GoVersion string
)

// ResolvedCommit returns the ldflags commit, or the toolchain revision.
// A dirty toolchain revision is marked so it is not printed as a clean commit.
func ResolvedCommit() string {
	if Commit != "" {
		return Commit
	}
	info, ok := debug.ReadBuildInfo()
	if !ok || info == nil {
		return ""
	}
	return commitFromSettings(info.Settings)
}

func commitFromSettings(settings []debug.BuildSetting) string {
	rev := settingFrom(settings, "vcs.revision")
	if rev == "" {
		return ""
	}
	if settingFrom(settings, "vcs.modified") == "true" {
		return rev + "-dirty"
	}
	return rev
}

// ResolvedDate returns the ldflag build time. It does not use vcs.time.
func ResolvedDate() string {
	return dateFromSettings(Date, nil)
}

func dateFromSettings(date string, _ []debug.BuildSetting) string {
	return date
}

func settingFrom(settings []debug.BuildSetting, key string) string {
	for _, s := range settings {
		if s.Key == key {
			return s.Value
		}
	}
	return ""
}

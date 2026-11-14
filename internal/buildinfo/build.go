package buildinfo

import (
	"runtime/debug"
	"strings"
)

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

// SameCommit reports whether two commit strings name the same revision.
//
// Build paths abbreviate differently: a release build injects {{ .ShortCommit }}
// (seven characters) and `make build` injects `git rev-parse --short HEAD`, which
// grows with the repository. Comparing the strings for equality therefore reports
// one commit as two. Abbreviated hashes compare by prefix instead; the "-dirty"
// marker belongs to the build, not the revision, so it is ignored.
//
// Seven is a policy floor: git will abbreviate further on request, but a shorter
// prefix can collide and a collision here would suppress a real "stale daemon"
// warning. A value that is not a hex hash of at least that length must match
// exactly, so "unknown" never matches a real revision.
func SameCommit(a, b string) bool {
	x, y := normalisedCommit(a), normalisedCommit(b)
	if x == "" || y == "" {
		return false
	}
	if !isHexHash(x) || !isHexHash(y) || len(x) < 7 || len(y) < 7 {
		// Values this build cannot read as abbreviated hashes ("unknown" from a
		// tarball build, or a hand-injected short prefix) must still match
		// exactly, or the change would invent a warning where two identical
		// strings previously agreed.
		return x == y
	}
	short, long := x, y
	if len(short) > len(long) {
		short, long = long, short
	}
	return strings.HasPrefix(long, short)
}

// normalisedCommit lower-cases a commit string and drops the build marker, so
// two spellings of one revision compare equal. It returns "" only for empty
// input: "unknown" is a value, not an absence.
func normalisedCommit(value string) string {
	commit := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), "-dirty")
	return strings.TrimSpace(commit)
}

func isHexHash(value string) bool {
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return value != ""
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

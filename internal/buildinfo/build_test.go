package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestResolvedCommitPrefersLdflags(t *testing.T) {
	old := Commit
	t.Cleanup(func() { Commit = old })
	Commit = "abc1234"
	if got := ResolvedCommit(); got != "abc1234" {
		t.Fatalf("ResolvedCommit() = %q, want ldflags value", got)
	}
}

func TestResolvedDatePrefersLdflags(t *testing.T) {
	old := Date
	t.Cleanup(func() { Date = old })
	Date = "2026-04-01T12:00:00Z"
	if got := ResolvedDate(); got != "2026-04-01T12:00:00Z" {
		t.Fatalf("ResolvedDate() = %q, want ldflags value", got)
	}
}

func TestResolvedCommitEmptyLdflagMatchesToolchainSettings(t *testing.T) {
	old := Commit
	t.Cleanup(func() { Commit = old })
	Commit = ""
	info, ok := debug.ReadBuildInfo()
	if !ok || info == nil {
		t.Fatal("ReadBuildInfo unavailable")
	}
	want := commitFromSettings(info.Settings)
	if got := ResolvedCommit(); got != want {
		t.Fatalf("ResolvedCommit() = %q, want toolchain settings %q", got, want)
	}
}

func TestResolvedDateEmptyLdflagStaysEmpty(t *testing.T) {
	old := Date
	t.Cleanup(func() { Date = old })
	Date = ""
	if got := ResolvedDate(); got != "" {
		t.Fatalf("ResolvedDate() = %q, want empty ldflag", got)
	}
}

func TestCommitFromSettingsMarksADirtyTree(t *testing.T) {
	settings := []debug.BuildSetting{
		{Key: "vcs.revision", Value: "abc1234"},
		{Key: "vcs.modified", Value: "true"},
	}
	if got := commitFromSettings(settings); got != "abc1234-dirty" {
		t.Fatalf("commitFromSettings() = %q, want dirty revision", got)
	}
	settings[1].Value = "false"
	if got := commitFromSettings(settings); got != "abc1234" {
		t.Fatalf("commitFromSettings() = %q, want clean revision", got)
	}
	if got := commitFromSettings([]debug.BuildSetting{{Key: "vcs.modified", Value: "true"}}); got != "" {
		t.Fatalf("commitFromSettings() = %q, want empty without a revision", got)
	}
}

func TestDateFromSettingsIgnoresRevisionTime(t *testing.T) {
	settings := []debug.BuildSetting{{Key: "vcs.time", Value: "2020-01-01T00:00:00Z"}}
	if got := dateFromSettings("", settings); got != "" {
		t.Fatalf("dateFromSettings() = %q, want empty build time", got)
	}
	if got := dateFromSettings("2026-04-01T12:00:00Z", settings); got != "2026-04-01T12:00:00Z" {
		t.Fatalf("dateFromSettings() = %q, want ldflag build time", got)
	}
}

func TestSettingFrom(t *testing.T) {
	settings := []debug.BuildSetting{{Key: "vcs.revision", Value: "deadbeef"}}
	if got := settingFrom(settings, "vcs.revision"); got != "deadbeef" {
		t.Fatalf("settingFrom() = %q", got)
	}
	if got := settingFrom(settings, "vcs.time"); got != "" {
		t.Fatalf("missing key = %q, want empty", got)
	}
	if got := settingFrom(nil, "vcs.revision"); got != "" {
		t.Fatalf("nil settings = %q, want empty", got)
	}
}

func TestSameCommitComparesAbbreviations(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"equal short hashes", "abc1234", "abc1234", true},
		{"release seven against make eight", "b59faba", "b59faba1", true},
		{"make eight against release seven", "b59faba1", "b59faba", true},
		{"full revision against seven", "b59faba1f523d06d382302bef063fe7532c83b60", "b59faba", true},
		{"unrelated commits", "def5678", "abc1234", false},
		{"same length different", "abc1234", "abc1235", false},
		{"dirty against clean", "abc1234-dirty", "abc1234", true},
		{"dirty on both", "abc1234-dirty", "abc1234-dirty", true},
		{"dirty against longer clean", "abc1234-dirty", "abc1234a", true},
		{"padded input", "  abc1234  ", "abc1234", true},
		{"case difference", "ABC1234", "abc1234", true},
		{"empty side", "", "abc1234", false},
		{"unknown is not a hash", "unknown", "abc1234", false},
		{"equal full hashes", "b59faba1f523d06d382302bef063fe7532c83b60", "b59faba1f523d06d382302bef063fe7532c83b60", true},
		{"unknown on both", "unknown", "unknown", true},
		{"non hex", "abc1234z", "abc1234", false},
		{"below the seven character floor", "abc123", "abc1234", false},
		{"identical below the floor", "abc123", "abc123", true},
		{"repeated dirty marker", "abc1234-dirty-dirty", "abc1234-dirty-dirty", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameCommit(tc.a, tc.b); got != tc.want {
				t.Fatalf("SameCommit(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

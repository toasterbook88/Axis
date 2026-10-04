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

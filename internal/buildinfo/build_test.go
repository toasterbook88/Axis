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

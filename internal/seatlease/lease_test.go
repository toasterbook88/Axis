package seatlease

import (
	"testing"
	"time"
)

func TestDecideFixtureTable(t *testing.T) {
	now := time.Date(2026, 11, 13, 21, 0, 0, 0, time.UTC)
	fresh := now.Add(time.Minute)
	past := now.Add(-time.Second)
	usable := Lease{
		State: "awake", Busy: false, Foreground: true,
		ExpiresAt: fresh, CompletionURL: "https://seat.example/v1/chat/completions",
	}

	cases := []struct {
		name    string
		lease   *Lease
		refusal string
		ok      bool
	}{
		{name: "missing", lease: nil, refusal: RefusalMissing},
		{name: "unloaded", lease: &Lease{State: "notLoaded", Foreground: true, ExpiresAt: fresh, CompletionURL: usable.CompletionURL}, refusal: RefusalUnloaded},
		{name: "loading", lease: &Lease{State: "loading", Foreground: true, ExpiresAt: fresh, CompletionURL: usable.CompletionURL}, refusal: RefusalUnloaded},
		{name: "ended", lease: &Lease{State: "ended", Foreground: true, ExpiresAt: fresh, CompletionURL: usable.CompletionURL}, refusal: RefusalEnded},
		{name: "asleep", lease: &Lease{State: "awake", Foreground: false, ExpiresAt: fresh, CompletionURL: usable.CompletionURL}, refusal: RefusalAsleep},
		{name: "busy", lease: &Lease{State: "awake", Busy: true, Foreground: true, ExpiresAt: fresh, CompletionURL: usable.CompletionURL}, refusal: RefusalBusy},
		{name: "answering", lease: &Lease{State: "answering", Foreground: true, ExpiresAt: fresh, CompletionURL: usable.CompletionURL}, refusal: RefusalBusy},
		{name: "stale past", lease: &Lease{State: "awake", Foreground: true, ExpiresAt: past, CompletionURL: usable.CompletionURL}, refusal: RefusalStale},
		{name: "stale now", lease: &Lease{State: "awake", Foreground: true, ExpiresAt: now, CompletionURL: usable.CompletionURL}, refusal: RefusalStale},
		{name: "stale empty expiry", lease: &Lease{State: "awake", Foreground: true, CompletionURL: usable.CompletionURL}, refusal: RefusalStale},
		{name: "missing url", lease: &Lease{State: "awake", Foreground: true, ExpiresAt: fresh}, refusal: RefusalMissing},
		{name: "usable", lease: &usable, ok: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Decide(tc.lease, now)
			if ok != tc.ok || got != tc.refusal {
				t.Fatalf("Decide() = %q, %v; want %q, %v", got, ok, tc.refusal, tc.ok)
			}
		})
	}
}

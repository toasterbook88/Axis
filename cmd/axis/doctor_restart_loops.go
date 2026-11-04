package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Restart-loop diagnostics.
//
// A unit with Restart= that can never start stays "activating (auto-restart)"
// indefinitely. It is never "failed", so `systemctl --failed` and most
// dashboards stay clean while it burns a restart every few seconds; the
// restart counter is the only durable trace. Doctor reads it for system and
// user services and reports units past restartLoopThreshold that are looping
// now (see loopingNow). The probe is
// read-only: it never resets counters or touches a unit.

// restartLoopThreshold is the restart count past which a unit is reported.
// Occasional restarts of a flaky tunnel or a crashed worker stay below it.
const restartLoopThreshold = 20

// restartLoopRecentWindow is how long a unit must have stayed active before
// its lifetime restart count stops counting as a current loop. systemd never
// resets NRestarts while a unit stays loaded, so a unit that looped last month
// and has run cleanly since would otherwise warn forever.
const restartLoopRecentWindow = 5 * time.Minute

// restartLoopReportLimit bounds how many units the check message lists.
const restartLoopReportLimit = 5

// unitRestarts is one service's restart counter and current state.
type unitRestarts struct {
	Scope    string // "system" or "user"
	Unit     string
	Restarts int
	Active   string
	Sub      string
	// ActiveFor is how long ago the unit last entered "active", or -1 when
	// unknown or never active since it was loaded.
	ActiveFor time.Duration
}

// loopingNow reports whether a unit past the threshold is looping now: it is
// mid-restart (activating, auto-restart), failed, or became active within
// restartLoopRecentWindow. An active unit with unknown activation time is
// treated as healthy, since warning on unknowns would reintroduce the
// lifetime-count noise.
func (u unitRestarts) loopingNow() bool {
	if u.Restarts <= restartLoopThreshold {
		return false
	}
	switch {
	case u.Sub == "auto-restart", u.Active == "activating", u.Active == "failed":
		return true
	case u.Active == "active":
		return u.ActiveFor >= 0 && u.ActiveFor < restartLoopRecentWindow
	}
	return false
}

var doctorProbeRestartLoops = probeRestartLoops

// probeRestartLoops reads NRestarts for every loaded service in the system
// and user managers. A scope that cannot be queried (no user bus, say) is
// skipped; the error is returned only when no scope could be read.
func probeRestartLoops(ctx context.Context) ([]unitRestarts, error) {
	if runtime.GOOS != "linux" {
		return nil, errOwnershipProbeUnsupported
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return nil, errOwnershipProbeUnsupported
	}
	var units []unitRestarts
	var errs []error
	read := 0
	for _, scope := range []string{"system", "user"} {
		args := []string{"show", "--property=Id,NRestarts,ActiveState,SubState,ActiveEnterTimestampMonotonic", "*.service"}
		if scope == "user" {
			args = append([]string{"--user"}, args...)
		}
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		out, err := exec.CommandContext(sctx, "systemctl", args...).Output()
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s manager: %w", scope, err))
			continue
		}
		read++
		units = append(units, parseRestartCountersAt(scope, string(out), monotonicNow())...)
	}
	if read == 0 {
		return nil, errors.Join(errs...)
	}
	return units, nil
}

// monotonicNow reads CLOCK_MONOTONIC, the clock systemd's *Monotonic
// timestamps use. It returns -1 when the clock cannot be read.
func monotonicNow() time.Duration {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		return -1
	}
	return time.Duration(ts.Nano())
}

// parseRestartCountersAt parses `systemctl show` output: one blank-line
// separated block of Key=Value lines per unit. now is the current
// CLOCK_MONOTONIC reading (-1 if unknown), used to derive ActiveFor.
func parseRestartCountersAt(scope, out string, now time.Duration) []unitRestarts {
	var units []unitRestarts
	fresh := func() unitRestarts { return unitRestarts{Scope: scope, ActiveFor: -1} }
	cur := fresh()
	flush := func() {
		if cur.Unit != "" {
			units = append(units, cur)
		}
		cur = fresh()
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			flush()
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "Id":
			cur.Unit = value
		case "NRestarts":
			cur.Restarts, _ = strconv.Atoi(value)
		case "ActiveState":
			cur.Active = value
		case "SubState":
			cur.Sub = value
		case "ActiveEnterTimestampMonotonic":
			if us, err := strconv.ParseInt(value, 10, 64); err == nil && us > 0 && now >= 0 {
				if d := now - time.Duration(us)*time.Microsecond; d >= 0 {
					cur.ActiveFor = d
				}
			}
		}
	}
	flush()
	return units
}

// restartLoopCheck turns restart counters into a doctor check. It reports
// nothing on platforms without systemd.
func restartLoopCheck(units []unitRestarts, err error) []DoctorCheck {
	if errors.Is(err, errOwnershipProbeUnsupported) {
		return nil
	}
	if err != nil {
		return []DoctorCheck{{
			Name:    "Service restart loops",
			Status:  "warn",
			Message: fmt.Sprintf("could not read restart counters: %v", err),
		}}
	}
	var looping []unitRestarts
	for _, u := range units {
		if u.loopingNow() {
			looping = append(looping, u)
		}
	}
	if len(looping) == 0 {
		return []DoctorCheck{{
			Name:    "Service restart loops",
			Status:  "pass",
			Message: fmt.Sprintf("no service is looping now (more than %d restarts and mid-restart, failed, or active under %s)", restartLoopThreshold, restartLoopRecentWindow),
		}}
	}
	sort.Slice(looping, func(i, j int) bool {
		if looping[i].Restarts != looping[j].Restarts {
			return looping[i].Restarts > looping[j].Restarts
		}
		return looping[i].Scope+looping[i].Unit < looping[j].Scope+looping[j].Unit
	})
	shown := looping
	if len(shown) > restartLoopReportLimit {
		shown = shown[:restartLoopReportLimit]
	}
	parts := make([]string, 0, len(shown))
	for _, u := range shown {
		parts = append(parts, fmt.Sprintf("%s %s: %d restarts (%s/%s)", u.Scope, u.Unit, u.Restarts, u.Active, u.Sub))
	}
	msg := strings.Join(parts, "; ")
	if extra := len(looping) - len(shown); extra > 0 {
		msg += fmt.Sprintf("; and %d more", extra)
	}
	top := shown[0]
	journal := "journalctl -u " + top.Unit
	if top.Scope == "user" {
		journal = "journalctl --user -u " + top.Unit
	}
	return []DoctorCheck{{
		Name:    "Service restart loops",
		Status:  "warn",
		Message: msg,
		Fix:     fmt.Sprintf("see why it exits (%s -n 20), then fix the unit or disable it", journal),
	}}
}

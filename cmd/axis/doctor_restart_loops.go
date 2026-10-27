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
)

// Restart-loop diagnostics.
//
// A unit with Restart= that can never start stays "activating (auto-restart)"
// indefinitely. It is never "failed", so `systemctl --failed` and most
// dashboards stay clean while it burns a restart every few seconds; the
// restart counter is the only durable trace. Doctor reads it for system and
// user services and reports the units past restartLoopThreshold. The probe is
// read-only: it never resets counters or touches a unit.

// restartLoopThreshold is the restart count past which a unit is reported.
// Occasional restarts of a flaky tunnel or a crashed worker stay below it.
const restartLoopThreshold = 20

// restartLoopReportLimit bounds how many units the check message lists.
const restartLoopReportLimit = 5

// unitRestarts is one service's restart counter and current state.
type unitRestarts struct {
	Scope    string // "system" or "user"
	Unit     string
	Restarts int
	Active   string
	Sub      string
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
		args := []string{"show", "--property=Id,NRestarts,ActiveState,SubState", "*.service"}
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
		units = append(units, parseRestartCounters(scope, string(out))...)
	}
	if read == 0 {
		return nil, errors.Join(errs...)
	}
	return units, nil
}

// parseRestartCounters parses `systemctl show` output: one blank-line
// separated block of Key=Value lines per unit.
func parseRestartCounters(scope, out string) []unitRestarts {
	var units []unitRestarts
	cur := unitRestarts{Scope: scope}
	flush := func() {
		if cur.Unit != "" {
			units = append(units, cur)
		}
		cur = unitRestarts{Scope: scope}
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
		if u.Restarts > restartLoopThreshold {
			looping = append(looping, u)
		}
	}
	if len(looping) == 0 {
		return []DoctorCheck{{
			Name:    "Service restart loops",
			Status:  "pass",
			Message: fmt.Sprintf("no service has restarted more than %d times", restartLoopThreshold),
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

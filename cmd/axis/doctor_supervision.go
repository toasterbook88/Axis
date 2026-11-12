package main

import (
	"fmt"
	"path"
	"strings"
)

// Daemon supervision diagnostics.
//
// The boot-start check only looks at the AXIS-managed service file, so a
// daemon started by hand (nohup, tmux, a shell loop) on a host with no service
// installed gets no warning at all. That daemon will not be restarted after a
// crash and will not come back after a reboot. The kernel already records who
// started each process: on a systemd host its cgroup names the unit. These
// checks read that record and nothing else.

// parseSystemdCgroup returns the systemd cgroup path from the contents of
// /proc/<pid>/cgroup: the unified (cgroup v2) "0::" entry, or the cgroup v1
// "name=systemd" hierarchy. It returns "" when neither is present.
func parseSystemdCgroup(content string) string {
	var legacy string
	for _, line := range strings.Split(content, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 3)
		if len(parts) != 3 {
			continue
		}
		switch {
		case parts[0] == "0" && parts[1] == "":
			return parts[2]
		case parts[1] == "name=systemd":
			legacy = parts[2]
		}
	}
	return legacy
}

// daemonServiceUnit returns the systemd service whose cgroup holds a daemon
// process, judged from the leaf of its cgroup path. It returns "" when the
// leaf is not a service: a login session (session-N.scope), a terminal
// multiplexer or any other scope.
func daemonServiceUnit(cgroup string) string {
	leaf := path.Base(cgroup)
	if strings.HasSuffix(leaf, ".service") {
		return leaf
	}
	return ""
}

// cgroupObservable reports whether a cgroup path says anything about who
// started the process. An empty path was not read; "/" is what a process sees
// inside a container's cgroup namespace, where the host's unit is hidden.
func cgroupObservable(cgroup string) bool {
	return cgroup != "" && cgroup != "/"
}

// daemonSupervisionCheck reports daemons that no service manager restarts.
// A daemon counts as supervised only when its own cgroup is a service whose
// name mentions axis; a daemon that merely inherited an unrelated service's
// cgroup (for example one started from an SSH login on a host without
// per-session scopes) stops with that service and is reported. It returns
// false when no owner's cgroup could be observed.
func daemonSupervisionCheck(owners []doctorDaemonProc) (DoctorCheck, bool) {
	var unsupervised, supervised []string
	for _, p := range owners {
		if !cgroupObservable(p.Cgroup) {
			continue
		}
		unit := daemonServiceUnit(p.Cgroup)
		switch {
		case unit != "" && strings.Contains(strings.ToLower(unit), "axis"):
			supervised = append(supervised, fmt.Sprintf("pid %d (%s)", p.PID, unit))
		case unit != "":
			unsupervised = append(unsupervised, fmt.Sprintf("pid %d (inside %s)", p.PID, unit))
		default:
			unsupervised = append(unsupervised, fmt.Sprintf("pid %d (%s)", p.PID, path.Base(p.Cgroup)))
		}
	}
	if len(unsupervised) > 0 {
		return DoctorCheck{
			Name:   "Daemon supervision",
			Status: "warn",
			Message: fmt.Sprintf("daemon not started by its own service: %s; it will not restart after a crash or start after a reboot",
				strings.Join(unsupervised, ", ")),
			Fix: "stop that daemon, then run axis daemon service install",
		}, true
	}
	if len(supervised) > 0 {
		return DoctorCheck{
			Name:    "Daemon supervision",
			Status:  "pass",
			Message: "supervised by systemd: " + strings.Join(supervised, ", "),
		}, true
	}
	return DoctorCheck{}, false
}

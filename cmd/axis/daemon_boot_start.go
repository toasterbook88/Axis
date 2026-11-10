package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"
)

// bootStartProbeTimeout bounds each read-only command the boot-start check runs.
var bootStartProbeTimeout = 5 * time.Second

// daemonBootStartCheck reports whether the AXIS-managed user service starts
// when the machine boots with nobody logged in. It only reads state: it never
// enables lingering, changes automatic login or installs a system service.
// It returns nil when no AXIS-managed service is installed.
func daemonBootStartCheck(ctx context.Context, deps daemonServiceDependencies) *DoctorCheck {
	if deps.goos != "linux" && deps.goos != "darwin" {
		return nil
	}
	home, err := deps.homeDir()
	if err != nil {
		return nil
	}
	path, err := daemonServicePath(deps.goos, home)
	if err != nil {
		return nil
	}
	content, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(content, []byte(daemonServiceMarker)) {
		return nil
	}
	probe := func(name string, args ...string) (string, error) {
		probeCtx, cancel := context.WithTimeout(ctx, bootStartProbeTimeout)
		defer cancel()
		out, err := deps.run(probeCtx, name, args...)
		if err == nil && probeCtx.Err() != nil {
			err = probeCtx.Err()
		}
		return strings.TrimSpace(string(out)), err
	}
	const name = "Daemon boot start"
	if deps.goos == "linux" {
		// is-enabled exits non-zero for "disabled", so read the output.
		if state, _ := probe("systemctl", "--user", "is-enabled", daemonSystemdUnit); state != "enabled" {
			return nil
		}
		linger, err := probe("loginctl", "show-user", strconv.Itoa(deps.uid()), "--property=Linger", "--value")
		switch {
		case err == nil && linger == "yes":
			return &DoctorCheck{Name: name, Status: "pass", Message: "user service starts at boot (lingering on)"}
		case err == nil && linger == "no":
			return &DoctorCheck{
				Name:    name,
				Status:  "warn",
				Message: "user service starts at login only (lingering off); after a reboot the daemon stays down until this user logs in",
				Fix:     "loginctl enable-linger $USER",
			}
		default:
			return &DoctorCheck{Name: name, Status: "warn", Message: "could not read lingering state: " + probeDetail(linger, err)}
		}
	}

	autoLogin, err := probe("defaults", "read", "/Library/Preferences/com.apple.loginwindow", "autoLoginUser")
	switch {
	case err != nil && strings.Contains(autoLogin, "does not exist"):
		return &DoctorCheck{
			Name:    name,
			Status:  "warn",
			Message: "LaunchAgent starts at login only (no automatic login); after a reboot the daemon stays down until this user logs in",
			Fix:     "enable automatic login for this user, or run the daemon as a LaunchDaemon (both need an administrator)",
		}
	case err != nil:
		return &DoctorCheck{Name: name, Status: "warn", Message: "could not read automatic-login state: " + probeDetail(autoLogin, err)}
	}
	me := ""
	if deps.userName != nil {
		me, _ = deps.userName()
	}
	if me == "" || autoLogin != me {
		return &DoctorCheck{
			Name:    name,
			Status:  "warn",
			Message: fmt.Sprintf("automatic login is set for %q, not this user; the LaunchAgent starts only when this user logs in", autoLogin),
			Fix:     "enable automatic login for this user, or run the daemon as a LaunchDaemon (both need an administrator)",
		}
	}
	return &DoctorCheck{Name: name, Status: "pass", Message: "automatic login is set for this user; the LaunchAgent starts when it logs in at boot"}
}

func probeDetail(out string, err error) string {
	switch {
	case err != nil && out != "":
		return fmt.Sprintf("%v: %s", err, out)
	case err != nil:
		return err.Error()
	case out == "":
		return "empty output"
	default:
		return fmt.Sprintf("unexpected output %q", out)
	}
}

func currentUserName() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

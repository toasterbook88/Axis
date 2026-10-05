package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/daemon"
)

// supervisorType identifies which service manager supervises the daemon.
type supervisorType int

const (
	supervisorNone supervisorType = iota
	supervisorSystemd
	supervisorLaunchd
)

// detectSupervisor determines which supervisor (if any) is managing the
// daemon. It returns the supervisor type and the service exec path (the
// binary path recorded in the unit file or plist).
// It is a var so tests can override it.
var detectSupervisor = func(deps daemonServiceDependencies) (supervisorType, string, error) {
	home, err := deps.homeDir()
	if err != nil {
		return supervisorNone, "", fmt.Errorf("resolve home directory: %w", err)
	}
	path, err := daemonServicePath(deps.goos, home)
	if err != nil {
		return supervisorNone, "", err
	}

	switch deps.goos {
	case "linux":
		// Check that the unit file exists and is AXIS-managed.
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return supervisorNone, "", nil
		}
		if !bytes.Contains(data, []byte(daemonServiceMarker)) {
			return supervisorNone, "", nil
		}
		// Check that the unit is active.
		if _, runErr := deps.run(context.Background(), "systemctl", "--user", "is-active", "--quiet", daemonSystemdUnit); runErr != nil {
			return supervisorNone, "", nil
		}
		execPath := parseSystemdExecStart(string(data))
		return supervisorSystemd, execPath, nil
	case "darwin":
		// Check that the plist exists and is AXIS-managed.
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return supervisorNone, "", nil
		}
		if !bytes.Contains(data, []byte(daemonServiceMarker)) {
			return supervisorNone, "", nil
		}
		// Check that the agent is loaded.
		domain := fmt.Sprintf("gui/%d", deps.uid())
		if _, runErr := deps.run(context.Background(), "launchctl", "print", domain+"/"+daemonLaunchdLabel); runErr != nil {
			return supervisorNone, "", nil
		}
		execPath := parseLaunchdProgramArguments(string(data))
		return supervisorLaunchd, execPath, nil
	default:
		return supervisorNone, "", nil
	}
}

// parseSystemdExecStart extracts the ExecStart binary path from a systemd
// unit file. The ExecStart line looks like:
//
//	ExecStart=/path/to/axis daemon start --addr ... --refresh ...
//
// We return the first field (the binary path).
func parseSystemdExecStart(unit string) string {
	for _, line := range strings.Split(unit, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ExecStart=") {
			value := strings.TrimPrefix(line, "ExecStart=")
			// systemd may quote the path; strip surrounding quotes.
			value = strings.Trim(value, `"'`)
			// The first field is the binary path.
			fields := strings.Fields(value)
			if len(fields) > 0 {
				return fields[0]
			}
		}
	}
	return ""
}

// parseLaunchdProgramArguments extracts the binary path from a launchd plist.
// The ProgramArguments array contains the binary path as the first string.
func parseLaunchdProgramArguments(plist string) string {
	// Find the ProgramArguments array and extract the first <string> value.
	idx := strings.Index(plist, "<key>ProgramArguments</key>")
	if idx < 0 {
		return ""
	}
	rest := plist[idx:]
	arrIdx := strings.Index(rest, "<array>")
	if arrIdx < 0 {
		return ""
	}
	rest = rest[arrIdx:]
	endIdx := strings.Index(rest, "</array>")
	if endIdx < 0 {
		return ""
	}
	arr := rest[:endIdx]
	// Find the first <string>...</string> inside the array.
	strIdx := strings.Index(arr, "<string>")
	if strIdx < 0 {
		return ""
	}
	arr = arr[strIdx+len("<string>"):]
	endStr := strings.Index(arr, "</string>")
	if endStr < 0 {
		return ""
	}
	return arr[:endStr]
}

// restartViaSupervisor restarts the daemon through the given supervisor.
func restartViaSupervisor(ctx context.Context, deps daemonServiceDependencies, sup supervisorType, out io.Writer) error {
	var name string
	var args []string
	switch sup {
	case supervisorSystemd:
		name = "systemctl"
		args = []string{"--user", "restart", daemonSystemdUnit}
	case supervisorLaunchd:
		domain := fmt.Sprintf("gui/%d", deps.uid())
		name = "launchctl"
		args = []string{"kickstart", "-k", domain + "/" + daemonLaunchdLabel}
	default:
		return fmt.Errorf("no supervisor available")
	}

	managerOut, err := deps.run(ctx, name, args...)
	if err != nil {
		msg := strings.TrimSpace(string(managerOut))
		if msg != "" {
			return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, msg)
		}
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	if len(managerOut) > 0 {
		fmt.Fprint(out, string(managerOut))
	}
	return nil
}

// restartSupervisedDaemon is the shared supervisor-aware restart entry point.
// It detects the active supervisor and restarts through it. If no supervisor
// is active, it falls back to the standalone restart path.
//
// replacedPaths are the paths just updated (for update); if non-empty, the
// service ExecStart must be among them or the restart is skipped with a
// warning. This prevents restarting a daemon that would bring back an old
// binary.
func restartSupervisedDaemon(ctx context.Context, deps daemonServiceDependencies, addr string, replacedPaths []string, out io.Writer) error {
	sup, execPath, err := detectSupervisor(deps)
	if err != nil {
		return err
	}

	switch sup {
	case supervisorSystemd, supervisorLaunchd:
		// For update: verify the service exec path was among the replaced paths.
		if len(replacedPaths) > 0 && execPath != "" {
			found := false
			for _, p := range replacedPaths {
				if sameFile(p, execPath) {
					found = true
					break
				}
			}
			if !found {
				fmt.Fprintf(out, "Warning: service exec path %s was not updated; skipping daemon restart\n", execPath)
				return nil
			}
		}
		if err := restartViaSupervisor(ctx, deps, sup, out); err != nil {
			return err
		}
		// Poll until the daemon is serving the expected version.
		return pollDaemonVersion(ctx, addr, out)
	default:
		// Standalone: use the existing restart logic.
		return restartDaemon(ctx, addr, out)
	}
}

// pollDaemonVersion polls daemon.FetchMeta until the daemon reports the
// expected version, the deadline expires, or ctx is cancelled.
func pollDaemonVersion(ctx context.Context, addr string, out io.Writer) error {
	deadline := time.Now().Add(restartReadyDeadline)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(restartPollInterval):
			pollCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			meta, err := restartFetchMeta(pollCtx, addr)
			cancel()
			if err == nil && meta.Version == daemon.Version {
				fmt.Fprintln(out, "AXIS daemon restarted and serving current version")
				return nil
			}
		}
	}
	return fmt.Errorf("daemon did not report expected version after restart on %s", addr)
}

// restartAfterUpdate restarts the daemon after a successful update.
// It checks that the service exec path was among the replaced paths,
// then delegates to restartSupervisedDaemon.
// It is a var so tests can override it.
var restartAfterUpdate = func(ctx context.Context, addr string, replacedPaths []string, out io.Writer) error {
	deps := defaultDaemonServiceDependencies()
	return restartSupervisedDaemon(ctx, deps, addr, replacedPaths, out)
}

// versionQueryDaemon queries the daemon's /health endpoint and returns the
// version string. It returns an error if the daemon is not responding.
func versionQueryDaemon(ctx context.Context, addr string) (string, error) {
	client, baseURLAddr := daemon.HttpClientForAddr(addr)
	baseURL := daemon.NormalizeAddr(baseURLAddr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("health check returned %s", resp.Status)
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	return payload.Version, nil
}

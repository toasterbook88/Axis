package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"strconv"
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

// supervisedService is what the AXIS-managed unit file or plist records about
// the daemon it launches.
type supervisedService struct {
	// execPath is the binary the service manager starts.
	execPath string
	// addr is the --addr the service passes to "daemon start" ("" if absent).
	addr string
}

// restartServiceDeps supplies the service-manager seams used by the restart
// paths. It is a var so tests can stub systemctl/launchctl.
var restartServiceDeps = defaultDaemonServiceDependencies

// detectSupervisor determines which supervisor (if any) is managing the
// daemon and what its unit file or plist records. The service-manager probe
// runs under ctx so a stalled systemctl/launchctl cannot outlive the caller.
// It is a var so tests can override it.
var detectSupervisor = func(ctx context.Context, deps daemonServiceDependencies) (supervisorType, supervisedService, error) {
	home, err := deps.homeDir()
	if err != nil {
		return supervisorNone, supervisedService{}, fmt.Errorf("resolve home directory: %w", err)
	}
	path, err := daemonServicePath(deps.goos, home)
	if err != nil {
		return supervisorNone, supervisedService{}, err
	}

	switch deps.goos {
	case "linux":
		// Check that the unit file exists and is AXIS-managed.
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return supervisorNone, supervisedService{}, nil
		}
		if !bytes.Contains(data, []byte(daemonServiceMarker)) {
			return supervisorNone, supervisedService{}, nil
		}
		// Check that the unit is active.
		if _, runErr := deps.run(ctx, "systemctl", "--user", "is-active", "--quiet", daemonSystemdUnit); runErr != nil {
			return supervisorNone, supervisedService{}, nil
		}
		return supervisorSystemd, serviceFromArgs(systemdExecStartArgs(string(data))), nil
	case "darwin":
		// Check that the plist exists and is AXIS-managed.
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return supervisorNone, supervisedService{}, nil
		}
		if !bytes.Contains(data, []byte(daemonServiceMarker)) {
			return supervisorNone, supervisedService{}, nil
		}
		// Check that the agent is loaded.
		domain := fmt.Sprintf("gui/%d", deps.uid())
		if _, runErr := deps.run(ctx, "launchctl", "print", domain+"/"+daemonLaunchdLabel); runErr != nil {
			return supervisorNone, supervisedService{}, nil
		}
		return supervisorLaunchd, serviceFromArgs(launchdProgramArguments(string(data))), nil
	default:
		return supervisorNone, supervisedService{}, nil
	}
}

// serviceFromArgs reads the binary and the --addr value from a service's
// argv as written by renderDaemonService.
func serviceFromArgs(args []string) supervisedService {
	var svc supervisedService
	if len(args) > 0 {
		svc.execPath = args[0]
	}
	for i := 1; i+1 < len(args); i++ {
		if args[i] == "--addr" {
			svc.addr = args[i+1]
			break
		}
	}
	return svc
}

// systemdExecStartArgs splits the ExecStart line written by renderDaemonService
// into argv, undoing systemdArgument's quoting and %-escaping.
func systemdExecStartArgs(unit string) []string {
	for _, line := range strings.Split(unit, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ExecStart=") {
			continue
		}
		rest := strings.TrimPrefix(line, "ExecStart=")
		var args []string
		for {
			rest = strings.TrimLeft(rest, " \t")
			if rest == "" {
				return args
			}
			var arg string
			if rest[0] == '"' {
				quoted, err := strconv.QuotedPrefix(rest)
				if err != nil {
					return args
				}
				arg, _ = strconv.Unquote(quoted)
				rest = rest[len(quoted):]
			} else if i := strings.IndexAny(rest, " \t"); i >= 0 {
				arg, rest = rest[:i], rest[i:]
			} else {
				arg, rest = rest, ""
			}
			args = append(args, strings.ReplaceAll(arg, "%%", "%"))
		}
	}
	return nil
}

// launchdProgramArguments returns the unescaped <string> values of the
// plist's ProgramArguments array.
func launchdProgramArguments(plist string) []string {
	idx := strings.Index(plist, "<key>ProgramArguments</key>")
	if idx < 0 {
		return nil
	}
	rest := plist[idx:]
	start := strings.Index(rest, "<array>")
	end := strings.Index(rest, "</array>")
	if start < 0 || end < start {
		return nil
	}
	arr := rest[start:end]
	var args []string
	for {
		i := strings.Index(arr, "<string>")
		if i < 0 {
			return args
		}
		arr = arr[i+len("<string>"):]
		j := strings.Index(arr, "</string>")
		if j < 0 {
			return args
		}
		args = append(args, html.UnescapeString(arr[:j]))
		arr = arr[j+len("</string>"):]
	}
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
	sup, svc, err := detectSupervisor(ctx, deps)
	if err != nil {
		return err
	}
	execPath := svc.execPath

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
		// Poll where the service actually listens: a unit installed with
		// "daemon service install --addr" serves there, not on the default.
		if svc.addr != "" {
			addr = svc.addr
		}
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
	return restartSupervisedDaemon(ctx, restartServiceDeps(), addr, replacedPaths, out)
}

// daemonIdentity is the build identity a running daemon reports on /health.
type daemonIdentity struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// versionQueryDaemon queries the daemon's /health endpoint and returns its
// version and commit. It returns an error if the daemon is not responding.
func versionQueryDaemon(ctx context.Context, addr string) (daemonIdentity, error) {
	client, baseURLAddr := daemon.HttpClientForAddr(addr)
	baseURL := daemon.NormalizeAddr(baseURLAddr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health", nil)
	if err != nil {
		return daemonIdentity{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return daemonIdentity{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return daemonIdentity{}, fmt.Errorf("health check returned %s", resp.Status)
	}
	var id daemonIdentity
	if err := json.NewDecoder(resp.Body).Decode(&id); err != nil {
		return daemonIdentity{}, err
	}
	return id, nil
}

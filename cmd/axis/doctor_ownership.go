package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/config"
)

// Daemon ownership diagnostics.
//
// api.ServeWithContext refuses to become a second owner of the daemon address,
// but a refusal at startup is invisible afterwards: the operator still has to
// read `ps` to learn that two daemons are alive, that the running one executes
// an inode that no longer exists on disk, or that a socket file is a leftover
// rather than a listener. These checks report that state and nothing else.
//
// Every probe here is observational. Doctor must never kill a process, unlink
// a socket, or take the advisory lock — taking the lock would make a daemon
// that is starting concurrently abort.

// errOwnershipProbeUnsupported is returned by probes that need a facility this
// platform does not provide (process enumeration needs procfs).
var errOwnershipProbeUnsupported = errors.New("not supported on this platform")

// doctorDaemonProc is one observed axis daemon process.
type doctorDaemonProc struct {
	PID int
	// Exe is the resolved executable path, with any "(deleted)" marker
	// stripped. Empty when the link could not be read (typically another
	// user's process).
	Exe string
	// Deleted reports that the executable inode has been unlinked or
	// replaced — the common case is a rebuild while the daemon kept running.
	Deleted bool
	// Addr is the API address the process was asked to serve.
	Addr string
}

// doctorSocketState describes who, if anyone, owns a unix daemon address.
type doctorSocketState struct {
	Path       string
	Exists     bool
	IsSocket   bool
	Live       bool
	LockPath   string
	LockExists bool
	LockHeld   bool
	LockErr    error
	// Unix is false for TCP daemon addresses, where none of the file-level
	// fields apply.
	Unix bool
}

// doctorMeshPortState describes the configured mesh gossip port.
type doctorMeshPortState struct {
	Port    int
	Enabled bool
	// Bound reports that something already holds the UDP port. Attribution
	// is inferred from the observed daemon processes, not from the socket
	// table: a full inode-to-pid scan needs privileges doctor should not
	// require.
	Bound bool
	Err   error
}

var (
	doctorProbeDaemonProcs = listDaemonProcs
	doctorProbeSocket      = probeDaemonSocket
	doctorProbeMeshPort    = probeMeshUDPPort
)

// listDaemonProcs enumerates axis processes serving an HTTP API address.
func listDaemonProcs() ([]doctorDaemonProc, error) {
	if runtime.GOOS != "linux" {
		return nil, errOwnershipProbeUnsupported
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("reading /proc: %w", err)
	}

	var procs []doctorDaemonProc
	self := os.Getpid()
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			// The process exited between ReadDir and here, or it belongs
			// to another user. Either way it is not observable.
			continue
		}
		argv := splitCmdline(raw)
		addr, ok := daemonServeAddr(argv)
		if !ok {
			continue
		}
		proc := doctorDaemonProc{PID: pid, Addr: addr}
		if target, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe")); err == nil {
			proc.Exe, proc.Deleted = splitDeletedExe(target)
		}
		procs = append(procs, proc)
	}
	sort.Slice(procs, func(i, j int) bool { return procs[i].PID < procs[j].PID })
	return procs, nil
}

// splitCmdline turns a NUL-separated /proc cmdline into argv.
func splitCmdline(raw []byte) []string {
	parts := strings.Split(string(raw), "\x00")
	argv := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			argv = append(argv, p)
		}
	}
	return argv
}

// splitDeletedExe strips Linux's "(deleted)" marker from an exe link target.
//
// The marker is how a daemon running a replaced binary is identified: a
// rebuild writes a new inode at the same path, so the path still resolves
// while the running image is orphaned.
func splitDeletedExe(target string) (path string, deleted bool) {
	const marker = " (deleted)"
	if strings.HasSuffix(target, marker) {
		return strings.TrimSuffix(target, marker), true
	}
	return target, false
}

// daemonServeAddr reports whether argv describes an axis process serving the
// HTTP API, and which address it serves.
//
// `axis mcp serve` also contains "serve" but binds nothing, so the subcommand
// is matched positionally rather than by substring.
func daemonServeAddr(argv []string) (string, bool) {
	if len(argv) < 2 {
		return "", false
	}
	if base := filepath.Base(argv[0]); base != "axis" && !strings.HasSuffix(base, "/axis") {
		return "", false
	}

	args := argv[1:]
	// Skip global flags to find the subcommand path.
	var verbs []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		verbs = append(verbs, a)
		if len(verbs) == 2 {
			break
		}
	}
	if len(verbs) == 0 {
		return "", false
	}
	switch {
	case verbs[0] == "serve":
	case verbs[0] == "daemon" && len(verbs) > 1 && verbs[1] == "start":
	default:
		return "", false
	}

	addr := api.DefaultAddr()
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, "--addr="); ok {
			addr = v
		} else if a == "--addr" && i+1 < len(args) {
			addr = args[i+1]
		}
	}
	return addr, true
}

// probeDaemonSocket inspects the daemon address without taking it over.
func probeDaemonSocket(addr string) doctorSocketState {
	st := doctorSocketState{Path: addr, Unix: strings.HasPrefix(addr, "/")}
	if !st.Unix {
		conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			st.Live = true
		}
		return st
	}

	st.LockPath = api.LockPathFor(addr)
	if _, err := os.Stat(st.LockPath); err == nil {
		st.LockExists = true
		st.LockHeld, st.LockErr = probeAdvisoryLockHeld(st.LockPath)
	} else if !os.IsNotExist(err) {
		st.LockErr = fmt.Errorf("stat lock file: %w", err)
	}
	fi, err := os.Lstat(addr)
	if err != nil {
		return st
	}
	st.Exists = true
	st.IsSocket = fi.Mode()&os.ModeSocket != 0
	if st.IsSocket {
		st.Live = api.SocketIsLive(addr)
	}
	return st
}

// meshUDPPort returns the gossip port the daemon would bind for this config.
// It mirrors the derivation in internal/daemon so doctor reports the port the
// daemon actually uses rather than the package default.
func meshUDPPort(cfg *config.Config) int {
	if cfg != nil && cfg.Discovery != nil && cfg.Discovery.UDPPort > 0 {
		return cfg.Discovery.UDPPort
	}
	return 42426
}

// probeMeshUDPPort reports whether the gossip port is already bound.
//
// Attribution is deliberately shallow: a bind attempt tells us the port is
// taken, and the daemon-process list tells us whether an axis daemon is the
// plausible holder. Mapping the socket to an exact pid needs a privileged
// inode scan, which is more than a read-only check should demand.
func probeMeshUDPPort(cfg *config.Config) doctorMeshPortState {
	st := doctorMeshPortState{Port: meshUDPPort(cfg), Enabled: cfg == nil || cfg.IsMeshEnabled()}
	if !st.Enabled {
		return st
	}
	pc, err := net.ListenPacket("udp", fmt.Sprintf(":%d", st.Port))
	if err != nil {
		st.Bound = true
		return st
	}
	_ = pc.Close()
	return st
}

// daemonOwnershipChecks turns observed ownership state into operator checks.
// Kept pure so the reporting rules are testable without a real daemon.
func daemonOwnershipChecks(procs []doctorDaemonProc, procErr error, sock doctorSocketState, meshState doctorMeshPortState) []DoctorCheck {
	var checks []DoctorCheck
	procsKnown := procErr == nil

	// Only daemons sharing this address contend for ownership.
	var owners []doctorDaemonProc
	for _, p := range procs {
		if p.Addr == sock.Path {
			owners = append(owners, p)
		}
	}

	switch {
	case errors.Is(procErr, errOwnershipProbeUnsupported):
		checks = append(checks, DoctorCheck{
			Name:    "Daemon processes",
			Status:  "warn",
			Message: fmt.Sprintf("cannot enumerate daemon processes on %s; duplicate daemons would go unreported", runtime.GOOS),
		})
	case procErr != nil:
		checks = append(checks, DoctorCheck{
			Name:    "Daemon processes",
			Status:  "warn",
			Message: fmt.Sprintf("process probe failed: %v", procErr),
		})
	case len(owners) == 0:
		checks = append(checks, DoctorCheck{
			Name:    "Daemon processes",
			Status:  "pass",
			Message: fmt.Sprintf("no axis daemon process is serving %s", sock.Path),
		})
	case len(owners) == 1:
		checks = append(checks, DoctorCheck{
			Name:    "Daemon processes",
			Status:  "pass",
			Message: fmt.Sprintf("one daemon serving %s (pid %d)", sock.Path, owners[0].PID),
		})
	default:
		checks = append(checks, DoctorCheck{
			Name:   "Daemon processes",
			Status: "fail",
			Message: fmt.Sprintf("%d axis daemons are serving %s (pids %s); only one may own the address",
				len(owners), sock.Path, formatPIDs(owners)),
			Fix: "stop the extra daemons; the survivor should be the one holding " + sock.LockPath,
		})
	}

	if procsKnown && len(owners) > 0 {
		checks = append(checks, daemonExecutableCheck(owners))
	}

	checks = append(checks, daemonSocketCheck(sock, owners, procsKnown))
	checks = append(checks, meshPortCheck(meshState, owners, procsKnown))
	return checks
}

// daemonExecutableCheck reports daemons running an executable that is no
// longer the file at its own path.
func daemonExecutableCheck(owners []doctorDaemonProc) DoctorCheck {
	var deleted, unresolved []doctorDaemonProc
	for _, p := range owners {
		switch {
		case p.Deleted:
			deleted = append(deleted, p)
		case p.Exe == "":
			unresolved = append(unresolved, p)
		}
	}

	if len(deleted) > 0 {
		var parts []string
		for _, p := range deleted {
			parts = append(parts, fmt.Sprintf("pid %d (%s)", p.PID, p.Exe))
		}
		return DoctorCheck{
			Name:    "Daemon executable",
			Status:  "fail",
			Message: fmt.Sprintf("running a deleted executable: %s; the on-disk binary is a different build", strings.Join(parts, ", ")),
			Fix:     "axis daemon restart, then confirm axis daemon status reports the current commit",
		}
	}
	if len(unresolved) > 0 {
		return DoctorCheck{
			Name:    "Daemon executable",
			Status:  "warn",
			Message: fmt.Sprintf("could not resolve the executable for %s (likely another user's process)", formatPIDs(unresolved)),
		}
	}
	return DoctorCheck{
		Name:    "Daemon executable",
		Status:  "pass",
		Message: fmt.Sprintf("on disk: %s", owners[0].Exe),
	}
}

// daemonSocketCheck reports who owns the daemon address.
func daemonSocketCheck(sock doctorSocketState, owners []doctorDaemonProc, procsKnown bool) DoctorCheck {
	if !sock.Unix {
		if sock.Live {
			return DoctorCheck{
				Name:    "Daemon socket",
				Status:  "pass",
				Message: fmt.Sprintf("%s is accepting connections", sock.Path),
			}
		}
		return DoctorCheck{
			Name:    "Daemon socket",
			Status:  "warn",
			Message: fmt.Sprintf("nothing is listening on %s", sock.Path),
			Fix:     "start with: axis serve",
		}
	}

	switch {
	case !sock.Exists:
		return DoctorCheck{
			Name:    "Daemon socket",
			Status:  "warn",
			Message: fmt.Sprintf("no socket at %s", sock.Path),
			Fix:     "start with: axis serve",
		}
	case !sock.IsSocket:
		return DoctorCheck{
			Name:    "Daemon socket",
			Status:  "fail",
			Message: fmt.Sprintf("%s exists but is not a socket; the daemon will refuse to start rather than remove it", sock.Path),
			Fix:     "inspect and remove the file by hand once you know what wrote it",
		}
	case !sock.Live:
		return DoctorCheck{
			Name:    "Daemon socket",
			Status:  "warn",
			Message: fmt.Sprintf("stale socket file at %s; nothing is accepting on it", sock.Path),
			Fix:     "axis serve removes a stale socket after taking the lock — do not unlink it by hand",
		}
	}

	if procsKnown && len(owners) == 0 {
		return DoctorCheck{
			Name:    "Daemon socket",
			Status:  "warn",
			Message: fmt.Sprintf("%s is live but no matching axis daemon process was found (the owner may run as another user)", sock.Path),
		}
	}
	if !sock.LockExists {
		return DoctorCheck{
			Name:    "Daemon socket",
			Status:  "warn",
			Message: fmt.Sprintf("%s is live but %s is missing; the owner predates advisory locking and cannot exclude a second daemon", sock.Path, sock.LockPath),
			Fix:     "restart the daemon from a current binary: axis daemon restart",
		}
	}
	if sock.LockErr != nil {
		return DoctorCheck{
			Name:    "Daemon socket",
			Status:  "warn",
			Message: fmt.Sprintf("%s is live and %s exists, but doctor could not verify that the advisory lock is held: %v", sock.Path, sock.LockPath, sock.LockErr),
		}
	}
	if !sock.LockHeld {
		return DoctorCheck{
			Name:    "Daemon socket",
			Status:  "warn",
			Message: fmt.Sprintf("%s is live and %s exists, but no process holds the advisory lock; the owner cannot exclude a second daemon", sock.Path, sock.LockPath),
			Fix:     "restart the daemon from a current binary: axis daemon restart",
		}
	}
	return DoctorCheck{
		Name:    "Daemon socket",
		Status:  "pass",
		Message: fmt.Sprintf("live, owned via %s", sock.LockPath),
	}
}

// meshPortCheck reports contention on the gossip UDP port.
func meshPortCheck(st doctorMeshPortState, owners []doctorDaemonProc, procsKnown bool) DoctorCheck {
	switch {
	case !st.Enabled:
		return DoctorCheck{
			Name:    "Mesh UDP port",
			Status:  "pass",
			Message: "mesh disabled by discovery config",
		}
	case st.Err != nil:
		return DoctorCheck{
			Name:    "Mesh UDP port",
			Status:  "warn",
			Message: fmt.Sprintf("probe failed for :%d: %v", st.Port, st.Err),
		}
	case !st.Bound:
		return DoctorCheck{
			Name:    "Mesh UDP port",
			Status:  "pass",
			Message: fmt.Sprintf(":%d is free", st.Port),
		}
	case procsKnown && len(owners) == 0:
		return DoctorCheck{
			Name:    "Mesh UDP port",
			Status:  "warn",
			Message: fmt.Sprintf(":%d is bound but no axis daemon is running; another process holds the gossip port", st.Port),
			Fix:     "identify the holder (ss -lunp) or move the mesh with discovery.udp_port in nodes.yaml",
		}
	case len(owners) > 1:
		return DoctorCheck{
			Name:   "Mesh UDP port",
			Status: "warn",
			Message: fmt.Sprintf(":%d is bound and %d axis daemons are running; gossip attribution is ambiguous",
				st.Port, len(owners)),
			Fix: "stop the extra daemons first, then re-run axis doctor",
		}
	default:
		return DoctorCheck{
			Name:    "Mesh UDP port",
			Status:  "pass",
			Message: fmt.Sprintf(":%d is bound, consistent with the running daemon", st.Port),
		}
	}
}

func formatPIDs(procs []doctorDaemonProc) string {
	parts := make([]string, 0, len(procs))
	for _, p := range procs {
		parts = append(parts, strconv.Itoa(p.PID))
	}
	return strings.Join(parts, ", ")
}

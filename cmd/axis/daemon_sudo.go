package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"

	"github.com/toasterbook88/axis/internal/persist"
)

// sudoInvoker is the non-root account behind a `sudo axis ...` invocation.
type sudoInvoker struct {
	name string
	uid  int
	home string
}

// lookupSudoInvoker reports the account that ran sudo, when this process is
// root because of sudo. ok is false for a non-root process and for a real
// root login. It is a var so tests can simulate running under sudo.
var lookupSudoInvoker = func() (inv sudoInvoker, ok bool, err error) {
	if os.Geteuid() != 0 {
		return sudoInvoker{}, false, nil
	}
	name, rawUID := os.Getenv("SUDO_USER"), os.Getenv("SUDO_UID")
	if name == "" || name == "root" || rawUID == "" || rawUID == "0" {
		return sudoInvoker{}, false, nil
	}
	uid, err := strconv.Atoi(rawUID)
	if err != nil {
		return sudoInvoker{}, true, fmt.Errorf("invalid SUDO_UID %q", rawUID)
	}
	u, err := user.LookupId(rawUID)
	if err != nil {
		return sudoInvoker{}, true, fmt.Errorf("look up sudo user %s: %w", name, err)
	}
	return sudoInvoker{name: name, uid: uid, home: u.HomeDir}, true, nil
}

// forSudoInvoker retargets the service-manager seams from root to inv: the
// unit or plist is read from inv's home, launchd is addressed in inv's GUI
// domain, and `systemctl --user` talks to inv's user manager.
func (d daemonServiceDependencies) forSudoInvoker(inv sudoInvoker) daemonServiceDependencies {
	d.homeDir = func() (string, error) { return inv.home, nil }
	d.uid = func() int { return inv.uid }
	run := d.run
	d.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "--user" {
			args = append([]string{"--user", "--machine=" + inv.name + "@"}, args[1:]...)
		}
		return run(ctx, name, args...)
	}
	return d
}

// restartSudoInvokerDaemon restarts the invoking user's supervised daemon
// after `sudo axis update`. It never falls back to the standalone path: that
// would spawn `axis serve` as root on root's socket, leaving a second,
// unsupervised daemon contending with the user's for the mesh port.
func restartSudoInvokerDaemon(ctx context.Context, deps daemonServiceDependencies, inv sudoInvoker, replacedPaths []string, out io.Writer) error {
	deps = deps.forSudoInvoker(inv)
	sup, svc, err := detectSupervisor(ctx, deps)
	if err != nil {
		return err
	}
	if sup == supervisorNone {
		fmt.Fprintf(out, "Running under sudo and %s has no active AXIS user service; not starting a daemon as root.\nRestart it as %s: axis daemon restart\n", inv.name, inv.name)
		return nil
	}
	// The poll runs as root, which holds root's API token rather than the
	// owner's, so readiness is read from the unauthenticated /health.
	addr := filepath.Join(persist.AxisDirIn(inv.home), "axis.sock")
	return restartThroughSupervisor(ctx, deps, sup, svc, addr, replacedPaths, pollDaemonHealthVersion, out)
}

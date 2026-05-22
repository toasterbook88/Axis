package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/ui"
)

func daemonCmd() *cobra.Command {
	var cacheAddr string

	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the local AXIS daemon lifecycle and cache",
	}
	cmd.PersistentFlags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS API daemon cache (Unix socket or TCP host:port)")
	cmd.AddCommand(daemonStartCmd())
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show local AXIS daemon health and staleness",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			meta, err := daemon.FetchMeta(ctx, cacheAddr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "daemon not responding on %s: %v\n", cacheAddr, err)
				return err
			}

			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(meta); err != nil {
				return err
			}

			switch {
			case meta.Version == "":
				fmt.Fprintln(cmd.OutOrStdout(), "warning: daemon metadata is missing version information; restart axis serve from current main")
			case meta.Stale:
				fmt.Fprintln(cmd.OutOrStdout(), "warning: daemon cache is stale; restart axis serve or run axis daemon refresh")
			default:
				fmt.Fprintln(cmd.OutOrStdout(), "daemon cache is fresh")
			}
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "mesh",
		Short: "Show gossip mesh peers from the local daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			peers, err := fetchDaemonMesh(ctx, cacheAddr)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "mesh query failed: %v\n", err)
				return err
			}
			printMeshPeers(cmd, peers)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "invalidate",
		Short: "Invalidate the local AXIS daemon snapshot cache",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			if err := invalidateDaemonCache(ctx, cacheAddr); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "AXIS daemon cache invalidated")
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "refresh",
		Short: "Refresh the local AXIS daemon snapshot cache now",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
			defer cancel()

			if err := refreshDaemonCache(ctx, cacheAddr); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "AXIS daemon cache refreshed")
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "restart",
		Short: "Restart the AXIS daemon on the target address from the current binary",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			return restartDaemon(ctx, cacheAddr, cmd.OutOrStdout())
		},
	})

	return cmd
}

func daemonStartCmd() *cobra.Command {
	var addr string
	var refreshInterval time.Duration
	var pprof bool

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the local AXIS daemon HTTP API with background snapshot refresh",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServeCommand(cmd.OutOrStdout(), addr, refreshInterval, pprof)
		},
	}

	cmd.Flags().StringVar(&addr, "addr", api.DefaultAddr(), "Listen address for the local AXIS API (Unix socket or TCP host:port)")
	cmd.Flags().DurationVar(&refreshInterval, "refresh", time.Minute, "Background snapshot refresh interval")
	cmd.Flags().BoolVar(&pprof, "pprof", false, "Expose /debug/pprof profiling endpoints")
	return cmd
}

func fetchDaemonMesh(ctx context.Context, addr string) ([]meshPeer, error) {
	client, baseURLAddr := daemon.HttpClientForAddr(addr)
	baseURL := daemon.NormalizeAddr(baseURLAddr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v2/mesh", nil)
	if err != nil {
		return nil, err
	}

	token, err := auth.LoadOrGenerateToken()
	if err != nil {
		return nil, fmt.Errorf("loading api token: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("mesh query failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var payload struct {
		Peers []meshPeer `json:"peers"`
		Count int        `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decoding mesh response: %w", err)
	}
	return payload.Peers, nil
}

type meshPeer struct {
	Name        string    `json:"name"`
	Hostname    string    `json:"hostname"`
	Port        int       `json:"port"`
	StableID    string    `json:"stable_id"`
	State       string    `json:"state"`
	Source      string    `json:"source"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	MissedPings int       `json:"missed_pings"`
	Generation  uint64    `json:"generation"`
}

func printMeshPeers(cmd *cobra.Command, peers []meshPeer) {
	out := cmd.OutOrStdout()
	if len(peers) == 0 {
		fmt.Fprintln(out, "No active mesh peers.")
		return
	}

	tbl := ui.NewTable("NAME", "HOSTNAME", "STATE", "SOURCE", "LAST SEEN")
	for _, p := range peers {
		stateColor := ui.Dim
		switch p.State {
		case "trusted":
			stateColor = ui.Green
		case "verified":
			stateColor = ui.Cyan
		case "discovered":
			stateColor = ui.Yellow
		case "suspect":
			stateColor = ui.Red
		}
		lastSeen := humanizeTime(p.LastSeen)
		tbl.AddRow(ui.Cyan(p.Name), p.Hostname, stateColor(p.State), p.Source, lastSeen)
	}
	fmt.Fprintf(out, "%s (%d peers)\n\n", ui.Bold("MESH PEERS"), len(peers))
	tbl.Render(out)
}

func humanizeTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func invalidateDaemonCache(ctx context.Context, addr string) error {
	client, baseURLAddr := daemon.HttpClientForAddr(addr)
	baseURL := daemon.NormalizeAddr(baseURLAddr)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/invalidate", nil)
	if err != nil {
		return err
	}

	return doDaemonActionWithClient(client, req, "daemon invalidate failed")
}

func refreshDaemonCache(ctx context.Context, addr string) error {
	return refreshDaemonCacheWithTrigger(ctx, addr, "")
}

func refreshDaemonCacheWithTrigger(ctx context.Context, addr, trigger string) error {
	client, baseURLAddr := daemon.HttpClientForAddr(addr)
	baseURL := daemon.NormalizeAddr(baseURLAddr)
	endpoint := baseURL + "/refresh"
	if trigger != "" {
		normalized, err := daemon.NormalizeRefreshTrigger(trigger)
		if err != nil {
			return err
		}
		values := url.Values{}
		values.Set("trigger", normalized)
		endpoint += "?" + values.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}

	return doDaemonActionWithClient(client, req, "daemon refresh failed")
}

func doDaemonActionWithClient(client *http.Client, req *http.Request, prefix string) error {
	token, err := auth.LoadOrGenerateToken()
	if err != nil {
		return fmt.Errorf("%s: loading api token: %w", prefix, err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		return fmt.Errorf("%s: %s", prefix, resp.Status)
	}
	return fmt.Errorf("%s: %s: %s", prefix, resp.Status, msg)
}

func restartDaemon(ctx context.Context, addr string, out io.Writer) error {
	listenAddr, err := daemonListenAddr(addr)
	if err != nil {
		return err
	}

	metaCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	meta, metaErr := daemon.FetchMeta(metaCtx, listenAddr)
	cancel()
	if metaErr == nil && meta.Version == daemon.Version && !meta.Stale {
		fmt.Fprintf(out, "AXIS daemon already fresh on %s\n", listenAddr)
		return nil
	}

	pid, err := findDaemonPID(listenAddr)
	if err != nil {
		return err
	}

	switch {
	case metaErr == nil && pid > 0:
		fmt.Fprintf(out, "Sending SIGTERM to AXIS daemon PID %d on %s\n", pid, listenAddr)
		if err := terminatePID(pid, out); err != nil {
			return err
		}
	case metaErr == nil && pid == 0:
		return fmt.Errorf("daemon metadata found on %s but no listener PID could be identified", listenAddr)
	case metaErr != nil && pid > 0:
		return fmt.Errorf("address %s is already in use but daemon metadata could not be read; refusing automatic restart", listenAddr)
	default:
		fmt.Fprintf(out, "No daemon responding on %s; starting fresh\n", listenAddr)
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot resolve current binary: %w", err)
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", os.DevNull, err)
	}
	defer devNull.Close()

	serveCmd := exec.Command(exe, "serve", "--addr", listenAddr)
	serveCmd.Stdin = devNull
	serveCmd.Stdout = devNull
	serveCmd.Stderr = devNull
	serveCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := serveCmd.Start(); err != nil {
		return fmt.Errorf("failed to start fresh daemon: %w", err)
	}
	fmt.Fprintf(out, "Fresh daemon started (PID %d) on %s\n", serveCmd.Process.Pid, listenAddr)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
			pollCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			meta, err := daemon.FetchMeta(pollCtx, listenAddr)
			cancel()
			if err == nil && meta.Version == daemon.Version && !meta.Stale {
				fmt.Fprintln(out, "AXIS daemon is fresh and serving current snapshot")
				return nil
			}
		}
	}

	return fmt.Errorf("daemon did not become ready in time on %s", listenAddr)
}

func daemonListenAddr(addr string) (string, error) {
	normalized := daemon.NormalizeAddr(addr)
	u, err := url.Parse(normalized)
	if err != nil {
		return "", fmt.Errorf("invalid daemon address %q: %w", addr, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("invalid daemon address %q", addr)
	}
	if _, _, err := net.SplitHostPort(u.Host); err != nil {
		return "", fmt.Errorf("invalid daemon address %q: %w", addr, err)
	}
	return u.Host, nil
}

func findDaemonPID(addr string) (int, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("split host/port: %w", err)
	}

	out, err := exec.Command("lsof", "-nP", "-iTCP:"+port, "-sTCP:LISTEN", "-Fp").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return 0, nil
		}
		return 0, fmt.Errorf("lsof lookup failed: %w", err)
	}

	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "p") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimPrefix(line, "p"))
		if err == nil {
			return pid, nil
		}
	}
	return 0, nil
}

func terminatePID(pid int, out io.Writer) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("sigterm pid %d: %w", pid, err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !pidAlive(pid) {
		return nil
	}

	fmt.Fprintf(out, "Daemon PID %d did not exit after SIGTERM; sending SIGKILL\n", pid)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("sigkill pid %d: %w", pid, err)
	}

	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}

	if pidAlive(pid) {
		return fmt.Errorf("pid %d is still alive after SIGKILL", pid)
	}
	return nil
}

func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

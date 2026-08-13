package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/persist"
)

const (
	daemonLaunchdLabel  = "com.toasterbook88.axis.daemon"
	daemonSystemdUnit   = "axis.service"
	daemonServiceMarker = "Managed by AXIS"
)

type daemonServiceDependencies struct {
	goos       string
	homeDir    func() (string, error)
	executable func() (string, error)
	uid        func() int
	run        func(context.Context, string, ...string) ([]byte, error)
}

func defaultDaemonServiceDependencies() daemonServiceDependencies {
	return daemonServiceDependencies{
		goos:       runtime.GOOS,
		homeDir:    os.UserHomeDir,
		executable: os.Executable,
		uid:        os.Getuid,
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
	}
}

func daemonServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Manage the supervised user daemon",
	}
	cmd.AddCommand(daemonServiceInstallCmd())
	cmd.AddCommand(daemonServiceStatusCmd())
	cmd.AddCommand(daemonServiceUninstallCmd())
	return cmd
}

func daemonServiceInstallCmd() *cobra.Command {
	var addr, refresh string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install and start a launchd or systemd user service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := installDaemonService(cmd.Context(), addr, refresh, defaultDaemonServiceDependencies())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "AXIS daemon user service installed: %s\n", path)
			fmt.Fprintln(cmd.OutOrStdout(), "Verify: axis daemon status")
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "addr", api.DefaultAddr(), "Daemon listen address (Unix socket or TCP host:port)")
	cmd.Flags().StringVar(&refresh, "refresh", "1m", "Snapshot refresh interval")
	return cmd
}

func daemonServiceStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show native user-service manager status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			deps := defaultDaemonServiceDependencies()
			var name string
			var managerArgs []string
			switch deps.goos {
			case "darwin":
				name = "launchctl"
				managerArgs = []string{"print", fmt.Sprintf("gui/%d/%s", deps.uid(), daemonLaunchdLabel)}
			case "linux":
				name = "systemctl"
				managerArgs = []string{"--user", "status", "--no-pager", daemonSystemdUnit}
			default:
				return unsupportedDaemonServicePlatform(deps.goos)
			}
			out, err := deps.run(cmd.Context(), name, managerArgs...)
			_, _ = cmd.OutOrStdout().Write(out)
			return err
		},
	}
}

func daemonServiceUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the native user service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := uninstallDaemonService(cmd.Context(), defaultDaemonServiceDependencies())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "AXIS daemon user service removed: %s\n", path)
			return nil
		},
	}
}

func daemonServicePath(goos, home string) (string, error) {
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", daemonLaunchdLabel+".plist"), nil
	case "linux":
		return filepath.Join(home, ".config", "systemd", "user", daemonSystemdUnit), nil
	default:
		return "", unsupportedDaemonServicePlatform(goos)
	}
}

func installDaemonService(ctx context.Context, addr, refresh string, deps daemonServiceDependencies) (string, error) {
	validatedAddr, err := daemonListenAddr(addr)
	if err != nil {
		return "", err
	}
	refreshInterval, err := time.ParseDuration(refresh)
	if err != nil || refreshInterval <= 0 {
		return "", fmt.Errorf("invalid refresh interval %q", refresh)
	}
	home, err := deps.homeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	exe, err := deps.executable()
	if err != nil {
		return "", fmt.Errorf("resolve current AXIS binary: %w", err)
	}
	if !filepath.IsAbs(exe) {
		return "", fmt.Errorf("AXIS executable path must be absolute: %q", exe)
	}
	path, err := daemonServicePath(deps.goos, home)
	if err != nil {
		return "", err
	}
	if existing, readErr := os.ReadFile(path); readErr == nil {
		if !bytes.Contains(existing, []byte(daemonServiceMarker)) {
			return "", fmt.Errorf("refusing to replace %s because it is not managed by AXIS", path)
		}
	} else if !os.IsNotExist(readErr) {
		return "", fmt.Errorf("inspect daemon service %s: %w", path, readErr)
	}
	content, err := renderDaemonService(deps.goos, exe, validatedAddr, refreshInterval.String(), home)
	if err != nil {
		return "", err
	}
	if deps.goos == "darwin" {
		if err := persist.EnsurePrivateDir(filepath.Join(home, "Library", "Logs", "Axis")); err != nil {
			return "", fmt.Errorf("create private daemon log directory: %w", err)
		}
	}
	if err := persist.WriteFileAtomic(path, content, 0o644); err != nil {
		return "", fmt.Errorf("write daemon service %s: %w", path, err)
	}

	switch deps.goos {
	case "darwin":
		domain := fmt.Sprintf("gui/%d", deps.uid())
		_, _ = deps.run(ctx, "launchctl", "bootout", domain+"/"+daemonLaunchdLabel)
		if err := runServiceCommand(ctx, deps, "launchctl", "bootstrap", domain, path); err != nil {
			return "", err
		}
		if err := runServiceCommand(ctx, deps, "launchctl", "enable", domain+"/"+daemonLaunchdLabel); err != nil {
			return "", err
		}
		if err := runServiceCommand(ctx, deps, "launchctl", "kickstart", "-k", domain+"/"+daemonLaunchdLabel); err != nil {
			return "", err
		}
	case "linux":
		if err := runServiceCommand(ctx, deps, "systemctl", "--user", "daemon-reload"); err != nil {
			return "", err
		}
		if err := runServiceCommand(ctx, deps, "systemctl", "--user", "enable", daemonSystemdUnit); err != nil {
			return "", err
		}
		if err := runServiceCommand(ctx, deps, "systemctl", "--user", "restart", daemonSystemdUnit); err != nil {
			return "", err
		}
	}
	return path, nil
}

func uninstallDaemonService(ctx context.Context, deps daemonServiceDependencies) (string, error) {
	home, err := deps.homeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	path, err := daemonServicePath(deps.goos, home)
	if err != nil {
		return "", err
	}
	if existing, readErr := os.ReadFile(path); readErr == nil {
		if !bytes.Contains(existing, []byte(daemonServiceMarker)) {
			return "", fmt.Errorf("refusing to remove %s because it is not managed by AXIS", path)
		}
	} else if !os.IsNotExist(readErr) {
		return "", fmt.Errorf("inspect daemon service %s: %w", path, readErr)
	}
	switch deps.goos {
	case "darwin":
		domain := fmt.Sprintf("gui/%d", deps.uid())
		stopErr := runServiceCommand(ctx, deps, "launchctl", "bootout", domain+"/"+daemonLaunchdLabel)
		if stopErr != nil {
			if _, checkErr := deps.run(ctx, "launchctl", "print", domain+"/"+daemonLaunchdLabel); checkErr == nil {
				return "", stopErr
			}
		}
	case "linux":
		stopErr := runServiceCommand(ctx, deps, "systemctl", "--user", "disable", "--now", daemonSystemdUnit)
		if stopErr != nil {
			if _, checkErr := deps.run(ctx, "systemctl", "--user", "is-active", "--quiet", daemonSystemdUnit); checkErr == nil {
				return "", stopErr
			}
		}
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("remove daemon service %s: %w", path, err)
	}
	if deps.goos == "linux" {
		if err := runServiceCommand(ctx, deps, "systemctl", "--user", "daemon-reload"); err != nil {
			return "", err
		}
	}
	return path, nil
}

func runServiceCommand(ctx context.Context, deps daemonServiceDependencies, name string, args ...string) error {
	out, err := deps.run(ctx, name, args...)
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, msg)
}

func renderDaemonService(goos, exe, addr, refresh, home string) ([]byte, error) {
	for label, value := range map[string]string{"executable": exe, "address": addr, "refresh": refresh, "home": home} {
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("%s contains an invalid control character", label)
		}
	}
	switch goos {
	case "darwin":
		var escaped bytes.Buffer
		writeXML := func(value string) string {
			escaped.Reset()
			_ = xml.EscapeText(&escaped, []byte(value))
			return escaped.String()
		}
		return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- Managed by AXIS. -->
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string><string>daemon</string><string>start</string>
    <string>--addr</string><string>%s</string>
    <string>--refresh</string><string>%s</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>30</integer>
  <key>ProcessType</key><string>Background</string>
  <key>Umask</key><integer>63</integer>
  <key>EnvironmentVariables</key>
  <dict>
    <key>HOME</key><string>%s</string>
    <key>PATH</key><string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
  </dict>
  <key>WorkingDirectory</key><string>%s</string>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, daemonLaunchdLabel, writeXML(exe), writeXML(addr), writeXML(refresh), writeXML(home), writeXML(home), writeXML(filepath.Join(home, "Library", "Logs", "Axis", "daemon.stdout.log")), writeXML(filepath.Join(home, "Library", "Logs", "Axis", "daemon.stderr.log")))), nil
	case "linux":
		return []byte(fmt.Sprintf(`# Managed by AXIS.
[Unit]
Description=AXIS snapshot daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment=PATH=/usr/local/bin:/run/current-system/sw/bin:/usr/bin:/bin:/usr/sbin:/sbin
ExecStart=%s daemon start --addr %s --refresh %s
Restart=on-failure
RestartSec=5
TimeoutStopSec=20
UMask=0077

[Install]
WantedBy=default.target
`, systemdArgument(exe), systemdArgument(addr), systemdArgument(refresh))), nil
	default:
		return nil, unsupportedDaemonServicePlatform(goos)
	}
}

func systemdArgument(value string) string {
	value = strings.ReplaceAll(value, "%", "%%")
	if strings.ContainsAny(value, " \t\"\\") {
		return strconv.Quote(value)
	}
	return value
}

func unsupportedDaemonServicePlatform(goos string) error {
	return fmt.Errorf("daemon user services are supported on darwin and linux, not %s", goos)
}

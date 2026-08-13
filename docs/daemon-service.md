# Daemon User Service

`axis daemon service install` installs and starts a supervised service for the
current user. It uses an absolute path to the currently running AXIS binary,
the private default Unix socket, and a one-minute refresh interval unless the
operator supplies `--addr` or `--refresh`.

| Platform | Service file | Manager |
| --- | --- | --- |
| macOS | `~/Library/LaunchAgents/com.toasterbook88.axis.daemon.plist` | `launchctl` GUI user domain |
| Linux | `~/.config/systemd/user/axis.service` | `systemctl --user` |

The generated service invokes `axis daemon start` directly without a shell and
applies an owner-only umask. The macOS service sets deterministic PATH/HOME
values and writes stdout/stderr beneath private `~/Library/Logs/Axis/`. The
Linux service writes to the user journal and includes `/usr/local/bin` plus the
NixOS system profile in PATH.

Install is idempotent for files carrying the `Managed by AXIS` marker and
refuses to overwrite an unrecognized service file. Uninstall has the same
ownership check, stops the job, removes its service file, and reloads the user
manager. It does not remove configuration, state, logs, tokens, or the AXIS
binary.

Install does not terminate a listener owned by an unknown process. If service
startup reports that the address is already in use, verify and stop that
process explicitly, then rerun install.

```bash
axis daemon service install
axis daemon service status
axis daemon status
axis daemon service uninstall
```

`axis daemon restart` directly replaces a listener process and is useful for
development or recovery. It does not install boot/login persistence. Prefer
the native service command for normal operation.

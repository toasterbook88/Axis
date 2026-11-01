# ssh -G reads the system-wide ssh_config when $HOME is the passwd home

## Problem

Transport resolves every node through `ssh -G` (`runSSHConfigCommand`,
`internal/transport/ssh.go:28-50` at `cee6b354`). Lines 36-45 add `-F $HOME/.ssh/config` whenever
that file exists:

```go
home, _ := os.UserHomeDir()
if home != "" {
    configPath := filepath.Join(home, ".ssh", "config")
    if _, err := os.Stat(configPath); err == nil {
        args = append(args, "-F", configPath)
    }
}
args = append(args, host)

cmd := exec.CommandContext(ctx, "ssh", args...)
```

With `-F`, OpenSSH ignores the system-wide `/etc/ssh/ssh_config` and its `Include`d files. So a
`ProxyJump` or `ProxyCommand` set only there never reaches the refusal added in #599
(`unsupportedDirective`), and the host is dialed directly: the silent bypass #599 removed for
per-user config. The #599 spec lists this as a non-goal, and the CHANGELOG states it: "A directive
set only in the system-wide `ssh_config` is not seen while `~/.ssh/config` exists."

The `-F` has a purpose. OpenSSH finds `~/.ssh/config` through the passwd entry's home directory and
ignores `$HOME`: on the author's workstation, `HOME=$(mktemp -d) ssh -G <alias>` returned the same
`hostname` and `user` as a plain `ssh -G <alias>`. Transport loads keys and known-hosts files from
`$HOME` (`signerPaths`, `existingKnownHostsPaths`, `normalizedSSHPath`), so `-F $HOME/.ssh/config`
keeps the config and the keys from the same home when `$HOME` is overridden (hermetic tests,
containers, `sudo -E`, a unit with its own `HOME`). Commit `120d84ad`, which added `-F`, gives no
reason; this one is inferred from the probe.

When `$HOME` is the passwd home, `-F` buys nothing: `ssh` would read the same user file, and the
only thing `-F` changes is that it hides the system file.

## Non-goals

- Changing the refusal policy, the error type or its message, or `axis doctor`. The doctor fix
  string stays exactly as merged in `cmd/axis/doctor.go:347-348`:
  `"Axis does not use %[1]s: point hostname or an endpoint at an address Axis can reach directly, "+`
  `"or add \"%[1]s none\" in a more specific Host block before the wildcard that sets it"`, which
  renders for ProxyJump as: Axis does not use ProxyJump: point hostname or an endpoint at an address
  Axis can reach directly, or add "ProxyJump none" in a more specific Host block before the wildcard
  that sets it. It names no literal `Host *`. The advice stays correct for a system-file wildcard,
  because OpenSSH reads the user file first and the first value obtained wins.
- Hop dialing (`ProxyJump` support).
- `nodes.yaml` generation. `axis init --generate` calls the same `runSSHConfigCommand` through
  `ResolveSSHTarget`, so its redirect check sees system-file values too; its logic is unchanged.
- Reading the system file when `$HOME` differs from the passwd home. That case keeps `-F` and the
  limit, and the CHANGELOG says so.

## Design

1. A seam in `internal/transport`, default the passwd entry's home directory:

   ```go
   var sshPasswdHome = func() (string, error) {
       u, err := user.LookupId(strconv.Itoa(os.Getuid()))
       if err != nil {
           return "", err
       }
       return u.HomeDir, nil
   }
   ```

   It must not use `user.Current()`. In a `CGO_ENABLED=0` build (how Axis is built), `Current`
   falls back to `HomeDir = $HOME` with no error when `/etc/passwd` has no entry for the uid
   (`os/user/lookup_stubs.go`, Go 1.27), which would make every comparison equal and drop `-F`
   exactly where it must stay. `LookupId` reads `/etc/passwd` without cgo and has no environment
   fallback, so a user known only through NSS (LDAP, systemd-homed) gets an error and keeps `-F`.
   The default caches its result with `sync.Once`; the passwd home does not change during a
   process.

2. `runSSHConfigCommand` omits `-F` when the passwd lookup succeeds and `$HOME` equals the passwd
   home. Both sides are compared after `filepath.Clean`, and after `filepath.EvalSymlinks` when it
   succeeds for both, so a symlinked home counts as equal. In every other case (`$HOME` differs,
   either lookup fails, `$HOME` is empty) it behaves as today: `-F $HOME/.ssh/config` if that file
   exists. So the only case that changes is the one where `ssh` would already read the same user
   file.

3. The CHANGELOG limit sentence becomes: a directive set only in the system-wide `ssh_config` is not
   seen when `$HOME` differs from the passwd home directory (or the passwd entry cannot be read).
   The `UnsupportedSSHDirectiveError` and `SSHTarget` comments and `docs/current-state.md` say the
   same.

### Parse surface

`parseSSHConfigDump` (`internal/transport/ssh.go` at `cee6b354`) extracts 11 keys from one `ssh -G`
run. Dropping `-F` changes the input to all of them, not only the two the refusal reads:

| Key | Read by |
| --- | --- |
| `hostname` | `Connect` dial address (`resolvedDialHost`), `ResolveSSHTarget` |
| `port` | `Connect` (`resolvedPort`), `ResolveSSHTarget` |
| `user` | `sshConfig` login user, `ResolveSSHTarget` |
| `hostkeyalias` | `Connect` host-key name (`resolvedHostKeyName`), `ResolveSSHTarget` |
| `proxyjump` | refusal (`unsupportedDirective`), `ResolveSSHTarget` |
| `proxycommand` | refusal (`unsupportedDirective`), `ResolveSSHTarget` |
| `hostkeyalgorithms` | `sshConfig` (`preferredHostKeyAlgorithms`) |
| `identityfile` | `sshConfig` (`signerPaths`) |
| `identitiesonly` | `sshConfig` (`signerPaths`) |
| `userknownhostsfile` | `sshConfig` (`existingKnownHostsPaths`) |
| `globalknownhostsfile` | `sshConfig` (`existingKnownHostsPaths`) |

### Measurement

On the author's workstation (OpenSSH 9.6p1; the system file sets only `SendEnv`, `HashKnownHosts`,
`GSSAPIAuthentication` and one `Include` of a directory with one file), `ssh -G -p <port> -l <user>`
was run with and without `-F ~/.ssh/config` for every `hostname` and endpoint in `~/.axis/nodes.yaml`
(18 targets), comparing those 11 keys: 0 keys differed on any target. Only key names and counts were
recorded.

Before the implementation PR merges, repeat it on one other node that runs `axis serve`, read-only,
and record the node's OpenSSH version and the counts (no hostnames) in that PR:

```bash
python3 - <<'EOF'
import collections, os, subprocess, yaml
KEYS = {"hostname", "user", "port", "hostkeyalias", "proxyjump", "proxycommand",
        "hostkeyalgorithms", "identityfile", "identitiesonly", "userknownhostsfile",
        "globalknownhostsfile"}
cfg = yaml.safe_load(open(os.path.expanduser("~/.axis/nodes.yaml")))
uc = os.path.expanduser("~/.ssh/config")
def dump(host, port, user, with_f):
    args = ["ssh", "-G", "-p", str(port), "-l", user] + (["-F", uc] if with_f else []) + [host]
    out = subprocess.run(args, capture_output=True, text=True, timeout=10).stdout
    d = collections.defaultdict(list)
    for line in out.splitlines():
        f = line.split()
        if len(f) >= 2 and f[0].lower() in KEYS:
            d[f[0].lower()].append(" ".join(f[1:]))
    return d
diff, n = collections.Counter(), 0
for node in cfg.get("nodes", []):
    hosts = [node["hostname"]] + [e["hostname"] for e in node.get("endpoints") or [] if e.get("hostname")]
    for h in hosts:
        n += 1
        a, b = dump(h, node.get("ssh_port") or 22, node["ssh_user"], True), dump(h, node.get("ssh_port") or 22, node["ssh_user"], False)
        diff.update(k for k in KEYS if a.get(k) != b.get(k))
print("targets:", n, "keys differing:", dict(diff) or "none")
EOF
```

If any key differs there, the implementation PR lists which, and whether the system-file value is
what the operator's own `ssh` uses (it is, by construction), before merging.

## Tests

The red test, written before the code. It runs the real `runSSHConfigCommand` against a fake `ssh`
on `PATH` that records its arguments, with `HOME` a temp directory holding `.ssh/config`:

```go
func TestSSHConfigCommandOmitsFOnlyWhenHomeIsThePasswdHome(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host *\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	argvFile := filepath.Join(bin, "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argvFile + "'\necho 'hostname node.example.com'\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", home)

	for _, tc := range []struct {
		name   string
		passwd func() (string, error)
		wantF  bool
	}{
		{"home is the passwd home", func() (string, error) { return home, nil }, false},
		{"home differs", func() (string, error) { return t.TempDir(), nil }, true},
		{"passwd lookup fails", func() (string, error) { return "", errors.New("no passwd entry") }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prev := sshPasswdHome
			sshPasswdHome = tc.passwd
			t.Cleanup(func() { sshPasswdHome = prev })
			if _, err := runSSHConfigCommand(context.Background(), "node.example.com", 22, "axis"); err != nil {
				t.Fatalf("runSSHConfigCommand: %v", err)
			}
			raw, err := os.ReadFile(argvFile)
			if err != nil {
				t.Fatal(err)
			}
			argv := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if got := slices.Contains(argv, "-F"); got != tc.wantF {
				t.Fatalf("ssh %q: -F present = %v, want %v", argv, got, tc.wantF)
			}
		})
	}
}
```

With only the seam added and `runSSHConfigCommand` unchanged, "home is the passwd home" fails
(`-F` is passed) and the other two pass. Also:

- A symlinked `HOME` pointing at the passwd home omits `-F`.
- `TestConnectReportsTheFirstRefusal` and the rest of `ssh_proxy_directive_test.go` stay green; they
  stub `runSSHConfigCommand`, so the rule cannot affect them.

A real system-only `ProxyJump` cannot be tested offline without writing `/etc/ssh`. The argument
test plus the existing refusal tests on a stubbed dump cover the chain: no `-F`, so `ssh -G` reports
the system directive, so `Connect` refuses.

## Alternatives

- Keep `-F` and record the limit as permanent: leaves the #599 bypass for system-file directives on
  every node with a user config.
- Drop `-F` unconditionally: with `$HOME` overridden, Axis would read one user's SSH config and
  another home's keys. Rejected by the probe above.
- Pass `-F` to a generated file that `Include`s `$HOME/.ssh/config` then `/etc/ssh/ssh_config`:
  covers the overridden-`HOME` case too, but writes a temp file per resolution and relies on
  `Include` path rules; more moving parts than the case needs today.

## Research

- `ssh(1)` `-F`: "If a configuration file is given on the command line, the system-wide
  configuration file (/etc/ssh/ssh_config) will be ignored" (<https://man.openbsd.org/ssh#F>).
- `ssh_config(5)`: files are read user file first, then system file, and the first obtained value
  of each parameter wins (<https://man.openbsd.org/ssh_config>).
- Go `os/user` (<https://pkg.go.dev/os/user>): without cgo, `Current` tries `/etc/passwd` and, if
  the uid is missing, returns `$USER` and `$HOME` with no error (`lookup_stubs.go` `current()`, read
  in the Go 1.27.3 toolchain); `LookupId` reads `/etc/passwd` and errors when the uid is missing.
  Design 1 uses `LookupId` and treats any error as "unknown".
- Optimization: the passwd lookup is cached once per process; no extra `ssh` run.

## Review

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
containers, `sudo -E`, a unit with its own `HOME`). Commit `120d84ad`, which added `-F`, states
only "`~/.ssh/config`: skip agent and default key names; pass `-F` to `ssh -G`" (its IdentitiesOnly
item); the `$HOME` mechanism above is inferred from the probe.

When `$HOME` is the passwd home, `-F` mostly buys nothing: `ssh` would read the same user file.
It changes two things: it hides the system file, and it skips OpenSSH's permission check on the
config files it reads implicitly. A user config that is world-writable or foreign-owned, or a
badly owned system `Include`, makes `ssh -G` without `-F` exit 255 ("Bad owner or permissions"),
while the same run with `-F` succeeds (reviewer probe, OpenSSH 9.6p1 in a sandbox). Design 3
handles that.

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
- Reading the system file when `$HOME` differs from the passwd home, or when the passwd entry
  cannot be read from `/etc/passwd` (NSS-only users). Those cases keep `-F` and the limit, and the
  CHANGELOG says so.
- An NSS module that overrides the home of a user who is also in `/etc/passwd` (for example sssd
  `override_homedir`): `ssh` would read the NSS home while Design 1 reads `/etc/passwd`. The
  measurement step checks for it; if seen, the implementation PR stops and the spec is revised.

## Design

1. A seam in `internal/transport` for the passwd file, and a reader that parses it directly:

   ```go
   var sshPasswdFile = "/etc/passwd"

   // passwdHome returns the home field of the /etc/passwd entry whose uid
   // field is uid. A missing file, a missing entry or an empty home is an error.
   func passwdHome(uid int) (string, error)
   ```

   It does not use `os/user`. In a `CGO_ENABLED=0` build (how Axis is built), `user.Current`
   falls back to `HomeDir = $HOME` with no error when `/etc/passwd` has no entry for the uid
   (`os/user/lookup_stubs.go` `current()`), and `user.LookupId` returns `Current()` whenever the
   uid matches (`os/user/lookup.go`), so it inherits the same fallback; the reviewer reproduced
   `LookupId` returning `$HOME` for a uid missing from `/etc/passwd`. Either would make the
   comparison equal for an NSS-only user with `$HOME` overridden and drop `-F` exactly where it
   must stay. Reading the file directly has no environment fallback. The result is computed once
   per process (`sync.Once` over the default path); tests that change `sshPasswdFile` bypass the
   cache.

2. `runSSHConfigCommand` omits `-F` when `passwdHome(os.Getuid())` succeeds and `$HOME` equals it.
   Both sides are compared after `filepath.Clean`, and after `filepath.EvalSymlinks` when it
   succeeds for both, so a symlinked home counts as equal. In every other case (`$HOME` differs,
   the passwd read fails, `$HOME` is empty) it behaves as today: `-F $HOME/.ssh/config` if that file
   exists.

3. If the run without `-F` fails (any error, including exit 255 from the permission check), it is
   rerun once with `-F $HOME/.ssh/config` when that file exists, and that result is used. So no
   config that resolves today stops resolving: a permission-rejected user config still yields its
   `ProxyJump` and is refused, as today, instead of falling back to defaults and dialing directly.
   The retry logs at debug level. `ResolveSSHTarget` (`axis init --generate`) gets the same
   behaviour because it calls the same function.

4. The CHANGELOG limit sentence becomes: a directive set only in the system-wide `ssh_config` is not
   seen when `$HOME` differs from the `/etc/passwd` home, when there is no `/etc/passwd` entry, or
   when OpenSSH rejects a config file's permissions. The `UnsupportedSSHDirectiveError` and
   `SSHTarget` comments and `docs/current-state.md` say the same.

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
preferring one whose OS differs from the workstation's (system includes such as crypto-policies
can set `HostKeyAlgorithms`, which transport uses verbatim). Run it as the user the `axis serve`
unit runs as, since that user's home and includes are the ones that apply. Record the node's OS
family, OpenSSH version and the counts (no hostnames) in that PR. Also compare `getent passwd
$(id -u)` with the `/etc/passwd` line for that uid (home field only; equal or not). The script
needs PyYAML; if it is missing, list the targets from `nodes.yaml` by hand and run the same `ssh -G`
pair per target:

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
	t.Setenv("USER", "axis")

	uid := os.Getuid()
	for _, tc := range []struct {
		name   string
		passwd string
		wantF  bool
	}{
		{"home is the passwd home", fmt.Sprintf("axis:x:%d:%d::%s:/bin/sh\n", uid, uid, home), false},
		{"home differs", fmt.Sprintf("axis:x:%d:%d::%s:/bin/sh\n", uid, uid, t.TempDir()), true},
		{"uid not in passwd", fmt.Sprintf("other:x:%d:%d::%s:/bin/sh\n", uid+1, uid+1, home), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			passwd := filepath.Join(t.TempDir(), "passwd")
			if err := os.WriteFile(passwd, []byte(tc.passwd), 0o644); err != nil {
				t.Fatal(err)
			}
			prev := sshPasswdFile
			sshPasswdFile = passwd
			t.Cleanup(func() { sshPasswdFile = prev })
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

With only the `sshPasswdFile` variable added and `runSSHConfigCommand` unchanged, "home is the
passwd home" fails (`-F` is passed) and the other two pass. "uid not in passwd" sets `HOME` and
`USER`, the conditions under which `os/user` would have reported `$HOME` as the passwd home.

Also:

- `passwdHome` directly: the uid's entry returns its home; a file without the uid returns an
  error; a missing file returns an error; an entry with an empty home field returns an error.
- Retry: a fake `ssh` that exits 255 when `-F` is absent and prints `proxyjump jump.example.com`
  when it is present, with `HOME` equal to the fake passwd home: `runSSHConfigCommand` returns the
  `-F` output, and `Connect` returns `*UnsupportedSSHDirectiveError` without dialing.
- A symlinked `HOME` pointing at the passwd home omits `-F`.
- `TestConnectReportsTheFirstRefusal` and the rest of `ssh_proxy_directive_test.go` stay green; they
  stub `runSSHConfigCommand`, so the rule cannot affect them.
- Optional integration test, skipped when `bwrap` or unprivileged user namespaces are unavailable
  (so CI may skip it): real `ssh -G` through the real `runSSHConfigCommand`, with a fake
  `/etc/passwd` and `/etc/ssh/ssh_config` bound in a sandbox. A system-only `ProxyJump` is refused
  by `Connect`; a 0666 user config with `ProxyJump` is refused, not dialed directly.

## Alternatives

- Keep `-F` and record the limit as permanent: leaves the #599 bypass for system-file directives on
  every node with a user config.
- Use `os/user` for the passwd home: falls back to `$HOME` without cgo (Design 1); rejected.
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
  the uid is missing, returns `$USER` and `$HOME` with no error (`lookup_stubs.go` `current()`);
  `LookupId` returns `Current()` when the uid matches (`lookup.go`), so it has the same fallback
  (read in the Go 1.27.3 toolchain; the reviewer found the same in 1.26.10). Design 1 reads
  `/etc/passwd` itself for that reason.
- OpenSSH checks ownership and mode of config files it reads implicitly and not of a file given
  with `-F` (readconf `SSHCONF_CHECKPERM`; observed in the reviewer's sandbox). Design 3's retry
  keeps today's results when that check rejects a file.
- Optimization: the passwd read is cached once per process. A second `ssh -G` runs only when the
  first fails.

## Review

Kind: spec
Reviewer: fresh-context subagent (Cursor)
Model: same vendor family as the author (the designated second-family reviewer was unavailable:
usage limit)
Base: cee6b354
Head: 6a21e677
Date: 2026-11-01
Verdict: revise

1. Major: `user.LookupId` returns `Current()` when the uid matches, and without cgo `Current`
   falls back to `$HOME`, so an NSS-only user with `$HOME` overridden would drop `-F`. Fixed:
   Design 1 reads `/etc/passwd` directly behind a path seam; the red test uses a fake passwd file,
   including a missing-uid case with `HOME` and `USER` set; `passwdHome` has its own tests.
2. Major: without `-F`, OpenSSH permission-checks the config files, so a world-writable user config
   or a badly owned system include makes `ssh -G` fail, and transport would fall back to defaults
   and dial directly, bypassing a user-file `ProxyJump` that is refused today. Fixed: Design 3
   reruns with `-F` when the run without it fails; a retry test asserts `Connect` still refuses.
3. Minor: the spec said system-file behaviour cannot be tested offline. Fixed: an optional sandboxed
   integration test (skipped without `bwrap` or user namespaces) covers a system-only `ProxyJump`
   and a 0666 user config.
4. Minor: commit `120d84ad` does state a reason. Fixed: quoted in Problem; the `$HOME` mechanism is
   marked inferred.

Questions:
- `axis serve` may run as another user. Fixed: the measurement runs as the unit's user.
- Non-Debian system includes may set `HostKeyAlgorithms`. Fixed: the measurement prefers a node
  with a different OS and records the OS family.
- An NSS module may override the home of a user in `/etc/passwd`. Fixed as a non-goal with a stop
  condition; the measurement compares `getent passwd` with `/etc/passwd`.
- PyYAML may be missing on the node. Fixed: the step says how to run it by hand.

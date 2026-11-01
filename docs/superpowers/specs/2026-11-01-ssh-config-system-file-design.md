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
   The retry logs at Warn, like the existing `ssh -G` failure warning (`ssh.go:776`), because it
   is the remaining path where a system-file directive goes unseen. `ResolveSSHTarget` (`axis init --generate`) gets the same
   behaviour because it calls the same function.

4. The CHANGELOG limit sentence becomes: a directive set only in the system-wide `ssh_config` is not
   seen when `$HOME` differs from the `/etc/passwd` home, when there is no `/etc/passwd` entry, or
   when OpenSSH rejects a config file's permissions. The `UnsupportedSSHDirectiveError` and
   `SSHTarget` comments and `docs/current-state.md` say the same. A second CHANGELOG sentence, marked
   as a behaviour change in the style of the #599 entry: when `$HOME` is the passwd home, hosts
   matched by a `ProxyJump` or `ProxyCommand` in the system-wide `ssh_config` (which the operator's
   own `ssh` already honours) are now refused; they worked by accident and now fail until a more
   specific `Host` block in `~/.ssh/config` sets `ProxyJump none` or `ProxyCommand none`.

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

Without `-F`, `ssh -G` also evaluates the system file's `Match exec` commands on every resolution
(reviewer probe: a `Match exec "touch <file>"` ran). The operator's own `ssh` does the same; the
measurement step greps for it so the cost is known before merge.

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
$(id -u)` with the `/etc/passwd` line for that uid (home field only; equal or not), and count
`Match exec` lines in `/etc/ssh/ssh_config` and its includes. On a node without `~/.ssh/config` the
change has no effect (Base already omits `-F`), and the script says so instead of comparing. The script
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
if not os.path.exists(uc):
    raise SystemExit("n/a: no ~/.ssh/config; ssh -G already runs without -F here")
def dump(host, port, user, with_f):
    args = ["ssh", "-G", "-p", str(port), "-l", user] + (["-F", uc] if with_f else []) + [host]
    r = subprocess.run(args, capture_output=True, text=True, timeout=10)
    if r.returncode != 0:
        return None
    d = collections.defaultdict(list)
    for line in r.stdout.splitlines():
        f = line.split()
        if len(f) >= 2 and f[0].lower() in KEYS:
            d[f[0].lower()].append(" ".join(f[1:]))
    return d
diff, failed, n = collections.Counter(), collections.Counter(), 0
for node in cfg.get("nodes", []):
    hosts = [node["hostname"]] + [e["hostname"] for e in node.get("endpoints") or [] if e.get("hostname")]
    for h in hosts:
        n += 1
        port = node.get("ssh_port") or 22
        a, b = dump(h, port, node["ssh_user"], True), dump(h, port, node["ssh_user"], False)
        if a is None or b is None:
            failed["with -F" if a is None else "without -F"] += 1
            continue
        diff.update(k for k in KEYS if a.get(k) != b.get(k))
print("targets:", n, "ssh -G failed:", dict(failed) or "none", "keys differing:", dict(diff) or "none")
EOF
```

A run that fails only without `-F` is a permission rejection, which Design 3's retry covers; the PR
records the count. If any key differs there, the implementation PR lists which, and whether the system-file value is
what the operator's own `ssh` uses (it is, by construction), before merging.

## Tests

The red test, written before the code. It runs the real `runSSHConfigCommand` against a fake `ssh`
on `PATH` that records each invocation and prints `proxyjump sys.example.com` only when `-F` is
absent, standing in for a system-file directive that `-F` hides. `HOME` is a temp directory holding
`.ssh/config`. The test asserts on the returned output, not only on the arguments, so an
implementation that runs without `-F` but returns the `-F` output fails:

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
	runsFile := filepath.Join(bin, "runs")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + runsFile + "'\n" +
		"echo 'hostname node.example.com'\n" +
		"case \" $* \" in *\" -F \"*) ;; *) echo 'proxyjump sys.example.com' ;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", home)
	t.Setenv("USER", "axis")

	uid := os.Getuid()
	for _, tc := range []struct {
		name          string
		passwd        string
		wantProxyJump string
	}{
		{"home is the passwd home", fmt.Sprintf("axis:x:%d:%d::%s:/bin/sh\n", uid, uid, home), "sys.example.com"},
		{"home differs", fmt.Sprintf("axis:x:%d:%d::%s:/bin/sh\n", uid, uid, t.TempDir()), ""},
		{"uid not in passwd", fmt.Sprintf("other:x:%d:%d::%s:/bin/sh\n", uid+1, uid+1, home), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			passwd := filepath.Join(t.TempDir(), "passwd")
			if err := os.WriteFile(passwd, []byte(tc.passwd), 0o644); err != nil {
				t.Fatal(err)
			}
			prev := sshPasswdFile
			sshPasswdFile = passwd
			t.Cleanup(func() { sshPasswdFile = prev })
			if err := os.WriteFile(runsFile, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			out, err := runSSHConfigCommand(context.Background(), "node.example.com", 22, "axis")
			if err != nil {
				t.Fatalf("runSSHConfigCommand: %v", err)
			}
			if got := parseSSHConfigDump(out).ProxyJump; got != tc.wantProxyJump {
				t.Fatalf("ProxyJump = %q, want %q", got, tc.wantProxyJump)
			}
			raw, err := os.ReadFile(runsFile)
			if err != nil {
				t.Fatal(err)
			}
			if runs := strings.Count(string(raw), "\n"); runs != 1 {
				t.Fatalf("ssh -G ran %d times, want 1:\n%s", runs, raw)
			}
		})
	}
}
```

With only the `sshPasswdFile` variable added and `runSSHConfigCommand` unchanged, "home is the
passwd home" fails (`-F` is passed, so the system value is hidden) and the other two pass. "uid not in passwd" sets `HOME` and
`USER`, the conditions under which `os/user` would have reported `$HOME` as the passwd home.

Also:

- `passwdHome` directly: the uid's entry returns its home; a file without the uid returns an
  error; a missing file returns an error; an entry with an empty home field returns an error.
- Retry: a fake `ssh` that exits 255 when `-F` is absent and prints `proxyjump jump.example.com`
  when it is present, with `HOME` equal to the fake passwd home: `runSSHConfigCommand` returns the
  `-F` output after exactly two runs (the second with `-F`), and `Connect` returns
  `*UnsupportedSSHDirectiveError` without dialing.
- Both runs fail: `runSSHConfigCommand` returns the second error, and transport falls back to
  defaults as it does today when `ssh -G` fails (`TestConnectDialsWithDefaultsWhenSSHConfigFails`).
- A symlinked `HOME` pointing at the passwd home omits `-F`.
- `TestConnectReportsTheFirstRefusal` and the rest of `ssh_proxy_directive_test.go` stay green; they
  stub `runSSHConfigCommand`, so the rule cannot affect them.
- Optional integration test, skipped when `bwrap` or unprivileged user namespaces are unavailable
  (so CI may skip it): real `ssh -G` through the real `runSSHConfigCommand`, with a fake
  `/etc/passwd` and a self-owned `/etc/ssh` tree bound in a sandbox. The whole tree is bound, not
  only `ssh_config`: inside an unprivileged user namespace root-owned files appear owned by the
  overflow uid, so a distro `Include` would fail OpenSSH's permission check. A system-only `ProxyJump` is refused
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
  first fails. Without `-F`, system-file `Match exec` commands run on each resolution, as they do
  for the operator's `ssh`; the measurement counts them.

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
- `axis serve` may run as another user. Became a measurement rule: run as the unit's user.
- Non-Debian system includes may set `HostKeyAlgorithms`. Became a measurement rule: prefer a
  node with a different OS and record the OS family.
- An NSS module may override the home of a user in `/etc/passwd`. Became a non-goal with a
  stop condition; the measurement compares `getent passwd` with `/etc/passwd`.
- PyYAML may be missing on the node. Became a note in the step on running it by hand.

Kind: spec
Reviewer: fresh-context subagent (Cursor)
Model: same vendor family as the author (the designated second-family reviewer was unavailable:
usage limit)
Base: cee6b354
Head: 23fb3992
Date: 2026-11-01
Verdict: approve-with-changes

1. Minor: the red test checked only the arguments of the last `ssh` run, so an implementation that
   returned the `-F` output after a discarded run without `-F` passed. Fixed: the fake `ssh` prints a
   `proxyjump` only without `-F`; the test asserts on the parsed output and on exactly one run; the
   retry test asserts exactly two.
2. Minor: the measurement script reported every key as differing on a node without
   `~/.ssh/config` and ignored exit codes. Fixed: it stops with "n/a" when the file is missing and
   counts failed runs separately.
3. Minor: no CHANGELOG entry for hosts that start being refused. Fixed: Design 4 adds a
   behaviour-change sentence with the `ProxyJump none` remedy.

Questions:
- Should the retry warn? Became Design 3: it logs at Warn.
- `Match exec` runs without `-F`. Became a note under Parse surface and Optimization, and the
  measurement counts `Match exec` lines.
- The bwrap test needs a self-owned `/etc/ssh` tree. Became part of the Tests entry.
- NSS home overrides and crypto-policies `HostKeyAlgorithms` on a served node. Open: they need the
  second-node measurement, which runs before the implementation PR merges.

#!/usr/bin/env bash
# E2E: the cmd/axis shell fixtures must work when the test process has an
# empty PATH. Compiles the cmd/axis test binary, then runs the fixture tests
# under `env -i PATH=` so neither the parent nor the child inherits a PATH.
# The fixtures resolve sh/sleep and build the child PATH from the same
# stub-first list (stubs, inherited PATH, /run/current-system/sw/bin,
# /usr/bin, /bin).
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

(cd "$root" && go test -c -o "$work/axis.test" ./cmd/axis)
cd "$root/cmd/axis"
env -i HOME="$work" PATH= "$work/axis.test" -test.count=1 -test.v \
	-test.run '^(TestShellStopMLXGuardMatchesServerNotPythonOrConsole|TestLlamaServerSampleScriptMaxRSSAndDeviceRow)$'

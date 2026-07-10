#!/usr/bin/env bash
# Fixture tests for make lint fail-closed behavior (no network).
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# Isolate: copy only Makefile recipe under test into a tiny module dir
mkdir -p "$tmp/fakebin" "$tmp/src"
cat > "$tmp/src/Makefile" <<'MAKE'
lint:
	@unformatted=$$(gofmt -l .) || exit $$?; \
	if [ -n "$$unformatted" ]; then \
		echo "Files need gofmt:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	@echo vet-ok
MAKE

# 1) crashing gofmt must fail
printf '%s\n' '#!/bin/sh' 'echo crash >&2' 'exit 127' > "$tmp/fakebin/gofmt"
chmod +x "$tmp/fakebin/gofmt"
if PATH="$tmp/fakebin:/usr/bin:/bin" make -C "$tmp/src" lint >/tmp/lint-out 2>&1; then
  echo "FAIL: expected lint to fail when gofmt crashes" >&2
  cat /tmp/lint-out >&2
  exit 1
fi
echo "ok: gofmt crash fails lint"

# 2) unformatted file must fail (use real gofmt)
printf '%s\n' 'package main' 'func main(){println(1)}' > "$tmp/src/a.go"
if (cd "$tmp/src" && make lint >/tmp/lint-out2 2>&1); then
  echo "FAIL: expected lint to fail on unformatted go" >&2
  cat /tmp/lint-out2 >&2
  exit 1
fi
echo "ok: unformatted file fails lint"

# 3) formatted file passes
gofmt -w "$tmp/src/a.go"
if ! (cd "$tmp/src" && make lint >/tmp/lint-out3 2>&1); then
  echo "FAIL: expected lint to pass on formatted go" >&2
  cat /tmp/lint-out3 >&2
  exit 1
fi
echo "ok: formatted file passes lint"
echo "all lint fail-closed fixtures passed"

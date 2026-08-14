#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail() {
  printf 'repo-truth-tests: %s\n' "$1" >&2
  exit 1
}

test_root="$(mktemp -d "${TMPDIR:-/tmp}/axis-repo-truth-tests.XXXXXX")"
trap 'rm -rf "$test_root"' EXIT

fake_bin="$test_root/bin"
mkdir -p "$fake_bin"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'printf '\''{"tag_name":"%s","published_at":"2026-08-13T00:00:00Z"}'\'' "${AXIS_TEST_RELEASE_TAG}"' \
  >"$fake_bin/curl"
chmod +x "$fake_bin/curl"

first_doc="$test_root/first.md"
second_doc="$test_root/second.md"
cp docs/current-state.md "$first_doc"
cp docs/current-state.md "$second_doc"

PATH="$fake_bin:$PATH" \
  AXIS_TEST_RELEASE_TAG=v0.14.10 \
  AXIS_CURRENT_STATE_DOC_PATH="$first_doc" \
  ./hack/refresh-current-state.sh --facts-only >/dev/null
PATH="$fake_bin:$PATH" \
  AXIS_TEST_RELEASE_TAG=v99.0.0 \
  AXIS_CURRENT_STATE_DOC_PATH="$second_doc" \
  ./hack/refresh-current-state.sh --facts-only >/dev/null

extract_facts() {
  awk '
    /<!-- BEGIN GENERATED CURRENT STATE FACTS -->/ { in_facts = 1 }
    in_facts { print }
    /<!-- END GENERATED CURRENT STATE FACTS -->/ { exit }
  ' "$1"
}

first_facts="$(extract_facts "$first_doc")"
second_facts="$(extract_facts "$second_doc")"

[[ "$first_facts" == "$second_facts" ]] \
  || fail "published release changes must not alter committed current-state facts"
[[ "$first_facts" != *"Latest published GitHub release"* ]] \
  || fail "committed facts must not embed the mutable latest GitHub release"
[[ "$first_facts" != *"Release truth"* ]] \
  || fail "committed facts must not embed a release comparison"

version_file="$test_root/version.go"
printf 'package buildinfo\n\nconst Version = "1.2.3"\n' >"$version_file"

source_tag="$(AXIS_VERSION_FILE="$version_file" ./hack/validate-release-version.sh --source)"
[[ "$source_tag" == "v1.2.3" ]] \
  || fail "source mode must return the normalized source tag"

validated_version="$(AXIS_VERSION_FILE="$version_file" ./hack/validate-release-version.sh v1.2.3)"
[[ "$validated_version" == "v1.2.3" ]] \
  || fail "matching source and tag must return the validated tag"

if AXIS_VERSION_FILE="$version_file" ./hack/validate-release-version.sh v1.2.4 >"$test_root/mismatch.out" 2>"$test_root/mismatch.err"; then
  fail "source/tag mismatch must be rejected"
fi
grep -qF 'tag v1.2.4 does not match source version 1.2.3' "$test_root/mismatch.err" \
  || fail "source/tag mismatch must explain both versions"

printf 'repo truth regression tests passed\n'

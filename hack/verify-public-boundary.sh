#!/usr/bin/env bash
# Enforce docs/decisions/placement-selection-contract.md §12 for IPv4
# literals and private cluster domain names across all tracked files.
#
# Documentation / example addresses are allowlisted. A 192.168.1.x or
# CGNAT address that is not on that list is treated as a live grid host.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail() { printf 'verify-public-boundary: %s\n' "$1" >&2; exit 1; }

allowed_ip() {
  case "$1" in
    # RFC 5737
    192.0.2.*|198.51.100.*|203.0.113.*) return 0 ;;
    # Common RFC 1918 examples (not a specific grid host)
    192.168.0.*|192.168.1.0|192.168.1.1|192.168.1.2|192.168.1.5|192.168.1.10|192.168.1.50|192.168.1.100|192.168.1.200|192.168.1.255|192.168.100.1) return 0 ;;
    10.0.0.*|10.0.1.*|10.1.2.3|10.8.0.*|10.147.17.5|10.254.*) return 0 ;;
    172.16.0.*|172.17.0.*|172.18.0.*|172.31.255.*|172.32.0.*) return 0 ;;
    # CGNAT classification fixtures + dummy 100.1.2.3 (not CGNAT, not a grid host)
    100.1.2.3|100.64.0.*|100.64.1.*|100.100.*|100.63.*|100.127.*|100.128.*) return 0 ;;

    169.254.0.0|169.254.1.1|169.254.1.2|169.254.1.5|169.254.169.254) return 0 ;;
    127.*|0.0.0.0|255.255.255.255|8.8.8.8|1.2.3.4|224.0.0.1) return 0 ;;
    # Semver mistaken for IPv4 (x.y.z.w version strings)
    0.*) return 0 ;;
    *) return 1 ;;
  esac
}

# 1. Check for private cluster domain / hostname patterns across all tracked files
domain_hits=()
forbidden_domain_regex='(\.lan\.axismcp\.org|\.axismcp\.org|\.ts\.net|cranium\.lan|tail9ecf52)'

while IFS= read -r -d '' f; do
  [[ -f "$f" ]] || continue
  [[ "$f" == "hack/verify-public-boundary.sh" ]] && continue
  while IFS= read -r line; do
    domain_hits+=("$f: $line")
  done < <(rg -n -e "$forbidden_domain_regex" "$f" || true)
done < <(git ls-files -z)

if (( ${#domain_hits[@]} > 0 )); then
  printf '%s\n' "${domain_hits[@]}" >&2
  fail "${#domain_hits[@]} private cluster domain/hostname occurrence(s); use RFC 2606 example.com domains"
fi

# 2. Check for unauthorized IPv4 literals across all tracked files
ip_hits=()
while IFS= read -r -d '' f; do
  [[ -f "$f" ]] || continue
  while IFS= read -r ip; do
    allowed_ip "$ip" && continue
    ip_hits+=("$f: address $ip")
  done < <(rg -oN --no-filename '[0-9]{1,3}(\.[0-9]{1,3}){3}' "$f" | sort -u)
done < <(git ls-files -z)

if (( ${#ip_hits[@]} > 0 )); then
  printf '%s\n' "${ip_hits[@]}" >&2
  fail "${#ip_hits[@]} non-documentation IPv4 literal(s); use RFC 5737 / example addresses (placement-selection-contract.md §12)"
fi

printf 'public boundary guardrails passed\n'

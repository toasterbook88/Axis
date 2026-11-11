#!/usr/bin/env python3
"""Block high-confidence credentials in Claude Code tool input.

Two tiers keep this usable on a repository full of documentation and test
fixtures:

* provider-shaped tokens (GitHub, AWS, Google, Slack, OpenAI, Anthropic,
  GitLab, Vault, JWTs, private keys, Authorization headers) always block;
* a generic ``key: value`` rule blocks only when the value looks like key
  material (20+ characters, at least one letter and one digit, no dots) and
  is not a documented placeholder. Prefixed names such as ``OPENAI_API_KEY``
  and bare ``token`` assignments are covered.

Every occurrence is checked, not just the first one in a string. A value that
already exists verbatim in a tracked file is allowed: it is already in the
repository, so re-using it is not a new leak, and blocking it would make the
repository's own fixtures uneditable. The matched value is never echoed.
"""

import json
import os
import re
import subprocess
import sys


SECRET_PATTERNS = (
    re.compile(r"-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----"),
    re.compile(r"\bgh[pousr]_[A-Za-z0-9_]{30,}\b"),
    re.compile(r"\bgithub_pat_[A-Za-z0-9_]{50,}\b"),
    re.compile(r"\bsgp_[A-Za-z0-9_-]{20,}\b"),
    re.compile(r"\bAKIA[0-9A-Z]{16}\b"),
    re.compile(r"\bAIza[0-9A-Za-z_-]{35}\b"),
    re.compile(r"\bya29\.[0-9A-Za-z_-]{20,}\b"),
    re.compile(r"\bxox[baprs]-\d{10,}-[A-Za-z0-9-]{10,}\b"),
    re.compile(r"\bsk-ant-[A-Za-z0-9_-]{20,}\b"),
    re.compile(r"\bsk-proj-[A-Za-z0-9_-]{20,}\b"),
    re.compile(r"\bsk-[A-Za-z0-9]{32,}\b"),
    re.compile(r"\bglpat-[A-Za-z0-9_-]{20,}\b"),
    re.compile(r"\bhvs\.[A-Za-z0-9_-]{20,}\b"),
    re.compile(r"\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b"),
    re.compile(r"https://hooks\.slack\.com/services/[A-Za-z0-9/]{20,}"),
    re.compile(
        r"(?i)\b(?:proxy-)?authorization\s*:\s*"
        r"(?:bearer|basic)\s+[A-Za-z0-9+/=_-]{20,}"
    ),
)

# A key may carry a prefix (OPENAI_API_KEY, MY_SECRET), be quoted, and be
# followed by a quote before the separator, so JSON config is covered as well
# as YAML, shell and environment assignments.
GENERIC_KEY = re.compile(
    r"(?i)\b(?:[a-z0-9]+[_-])*(?:aws_secret_access_key|client_secret|"
    r"api[_-]?key|access[_-]?token|auth[_-]?token|token|password|passwd|secret)\b"
    r"['\"]?\s*[:=]\s*['\"]?"
    r"([A-Za-z0-9/+=_-]{20,})"
)

PLACEHOLDER = re.compile(
    r"(?i)(example|placeholder|changeme|change-me|your[_-]|replace[-_]with|"
    r"shared-cluster-secret|<[^>]+>|not[-_]a[-_]real|dummy|fake|sample)"
)


def strings(value):
    if isinstance(value, str):
        yield value
    elif isinstance(value, dict):
        for child in value.values():
            yield from strings(child)
    elif isinstance(value, list):
        for child in value:
            yield from strings(child)


def looks_like_secret(value):
    """True for a value shaped like key material, not an identifier."""
    if PLACEHOLDER.search(value):
        return False
    return any(char.isdigit() for char in value) and any(
        char.isalpha() for char in value
    )


def already_tracked(value):
    """True when the exact string already exists in a tracked file."""
    project = os.environ.get("CLAUDE_PROJECT_DIR") or None
    try:
        result = subprocess.run(
            ["git", "grep", "-F", "-q", "--", value],
            cwd=project,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
    except OSError:
        return False
    return result.returncode == 0


def scan(value, allow_tracked):
    for pattern in SECRET_PATTERNS:
        for match in pattern.finditer(value):
            candidate = match.group(0)
            if PLACEHOLDER.search(candidate) is not None:
                continue
            if allow_tracked and already_tracked(candidate):
                continue
            return True
    for match in GENERIC_KEY.finditer(value):
        candidate = match.group(1)
        if not looks_like_secret(candidate):
            continue
        if allow_tracked and already_tracked(candidate):
            continue
        return True
    return False


def main():
    try:
        payload = json.load(sys.stdin)
    except (json.JSONDecodeError, UnicodeDecodeError):
        print("secret guard: could not parse tool input; blocked", file=sys.stderr)
        return 2

    tool = payload.get("tool_name") if isinstance(payload, dict) else None
    # Editing a file that already contains a value (the repository's own test
    # fixtures) is not a new leak. A shell command gets no such allowance: a
    # committed token echoed in a command still crosses a boundary the hook is
    # meant to hold.
    allow_tracked = tool in {"Edit", "Write", "MultiEdit", "NotebookEdit"}

    for value in strings(payload):
        if scan(value, allow_tracked):
            print(
                "secret guard: tool input appears to contain a credential; "
                "remove it or use a placeholder",
                file=sys.stderr,
            )
            return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())

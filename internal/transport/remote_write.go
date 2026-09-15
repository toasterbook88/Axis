package transport

import (
	"encoding/base64"
	"fmt"
	"strings"

	"al.essio.dev/pkg/shellescape"
)

func shellescapeQuote(s string) string { return shellescape.Quote(s) }

func base64StdEncode(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// MaxRemoteArgChars is the Linux MAX_ARG_STRLEN limit (131072) minus a safety
// margin for the surrounding command, path quoting, and login-shell expansion.
// A single SSH argument longer than this fails execve with E2BIG ("Argument
// list too long") regardless of how much memory the remote host has.
const MaxRemoteArgChars = 120_000

// BuildRemoteWriteCommand returns a shell-neutral command that writes content
// to path on a remote node. Base64 keeps payload bytes out of the login shell's
// parser, avoiding POSIX heredoc incompatibilities under shells such as fish.
//
// Content is delivered in fixed-size base64 chunks appended with >> so the
// per-argument length stays far below the remote kernel's MAX_ARG_STRLEN no
// matter how large the payload grows (execution context snapshots cross the
// limit at roughly 90KB of JSON). Chunked appends are byte-identical to a
// single write: base64 chunks are cut at fixed 4-char groups on the encoded
// side, so concatenation then decode == decode of the whole.
func BuildRemoteWriteCommand(path string, content []byte) string {
	return BuildRemoteWriteCommandChunked(path, content, MaxRemoteArgChars)
}

// BuildRemoteWriteCommandChunked is BuildRemoteWriteCommand with an explicit
// per-command character budget. Kept exported for tests that pin the chunking
// contract (single command when under budget; ordered appends when over).
func BuildRemoteWriteCommandChunked(path string, content []byte, budget int) string {
	quotedPath := shellescapeQuote(path)
	encoded := base64StdEncode(content)
	if len(encoded)+len(quotedPath)+64 <= budget {
		return fmt.Sprintf("mkdir -p $(dirname %s) && printf '%%s' %s | base64 -d > %s",
			quotedPath, shellescapeQuote(encoded), quotedPath)
	}

	// Cut the encoded payload into fixed base64 groups (multiples of 4 chars)
	// so each `printf '...' >> path` command stays within budget regardless of
	// shell overhead. base64 of arbitrary bytes is always a multiple of 4 chars
	// with padding, so fixed 4-char group splitting is lossless.
	group := budget - len(quotedPath) - 80
	group -= group % 4
	if group < 4 {
		group = 4
	}
	var b strings.Builder
	b.WriteString("mkdir -p $(dirname ")
	b.WriteString(quotedPath)
	b.WriteString(") && : > ")
	b.WriteString(quotedPath)
	for i := 0; i < len(encoded); i += group {
		end := i + group
		if end > len(encoded) {
			end = len(encoded)
		}
		b.WriteString(" && printf '%s' ")
		b.WriteString(shellescapeQuote(encoded[i:end]))
		b.WriteString(" >> ")
		b.WriteString(quotedPath)
	}
	b.WriteString(" && base64 -d ")
	b.WriteString(quotedPath)
	b.WriteString(" > ")
	b.WriteString(quotedPath)
	b.WriteString(" && rm -f ")
	b.WriteString(quotedPath)
	b.WriteString(".b64 2>/dev/null || true")
	return b.String()
}

package chat

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Tool results are capped when they enter the conversation, so one oversized
// result cannot sit whole in the protected tail and fill every later request.
const (
	toolResultCapNoLimit = 32 << 10 // bytes, for a conversation without a budget
	toolResultCapFloor   = 2048     // bytes
)

var toolCapMarkerRe = regexp.MustCompile(`\n… \[truncated: tool output was (\d+) bytes; the first \d+ are kept\]$`)

func toolCapNote(total, kept int) string {
	return fmt.Sprintf("… [truncated: tool output was %d bytes; the first %d are kept]", total, kept)
}

// toolResultCap is maxTokens bytes (about a quarter of the budget at chars/4),
// never below 2,048. A conversation without a budget uses 32 KiB.
func toolResultCap(maxChars int) int {
	maxTokens := maxChars / 4
	if maxTokens <= 0 {
		return toolResultCapNoLimit
	}
	return max(toolResultCapFloor, maxTokens)
}

// asSent replaces each invalid UTF-8 byte with U+FFFD, as encoding/json does
// when the request is sent, so the cap measures what the model receives.
func asSent(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.Map(func(r rune) rune { return r }, s)
}

// capToolResult returns content cut to at most capBytes, marker included,
// on a UTF-8 rune boundary. A result it already capped keeps its original
// size in the marker, and a capped result passes through unchanged.
func capToolResult(content string, capBytes int) string {
	content = asSent(content)
	if len(content) <= capBytes {
		return content
	}
	total, body := len(content), content
	if m := toolCapMarkerRe.FindStringSubmatchIndex(content); m != nil {
		if n, err := strconv.Atoi(content[m[2]:m[3]]); err == nil && n > total {
			total = n
		}
		body = content[:m[0]]
	}
	// The note's kept count has at most as many digits as capBytes.
	keep := max(0, min(len(body), capBytes-len("\n"+toolCapNote(total, capBytes))))
	for keep > 0 && keep < len(body) && !utf8.RuneStart(body[keep]) {
		keep--
	}
	return body[:keep] + "\n" + toolCapNote(total, keep)
}

// capToolMessage caps m's content when m is a tool result.
func (c *Conversation) capToolMessage(m Message) Message {
	if m.Role == RoleTool {
		m.Content = capToolResult(m.Content, toolResultCap(c.maxChars))
	}
	return m
}

package agent

import (
	"regexp"
	"strconv"
	"strings"
)

// contextOverflow is what a server's "request too long" error states.
// A zero field means the error did not state it.
type contextOverflow struct {
	window int // the served context in tokens
	total  int // the request size in tokens, as the server counted it
}

// overflowMarkers are the error texts servers use for a request longer than
// the context window: llama.cpp, LiteLLM, OpenAI-compatible and Anthropic.
var overflowMarkers = []string{
	"exceeds the available context size",
	"context_length_exceeded",
	"maximum context length",
	"ContextWindowExceededError",
	"prompt is too long",
}

var (
	overflowWindowRe = regexp.MustCompile(`context size \((\d+) tokens\)|maximum context length is (\d+)|tokens > (\d+) maximum`)
	overflowTotalRe  = regexp.MustCompile(`request \((\d+) tokens\)|resulted in (\d+) tokens|too long: (\d+) tokens`)
)

// parseContextOverflow reports whether err is an HTTP 400 context-overflow
// error, and the window and request size it states. Backends report errors
// as "http status N: body" before any output is streamed.
func parseContextOverflow(err error) (contextOverflow, bool) {
	if err == nil {
		return contextOverflow{}, false
	}
	msg := err.Error()
	if !strings.Contains(msg, "http status 400") {
		return contextOverflow{}, false
	}
	for _, m := range overflowMarkers {
		if strings.Contains(msg, m) {
			return contextOverflow{window: firstNumber(overflowWindowRe, msg), total: firstNumber(overflowTotalRe, msg)}, true
		}
	}
	return contextOverflow{}, false
}

// firstNumber returns the first captured number re finds in s, or 0.
func firstNumber(re *regexp.Regexp, s string) int {
	m := re.FindStringSubmatch(s)
	for _, g := range m[min(1, len(m)):] {
		if n, err := strconv.Atoi(g); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

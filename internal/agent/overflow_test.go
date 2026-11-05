package agent

import (
	"errors"
	"fmt"
	"testing"
)

// loggedOverflow is the shape of the logged proxy error (model names removed).
const loggedOverflow = `http status 400: {"error":{"message":"ContextWindowExceededError: request (34870 tokens) exceeds the available context size (32768 tokens), try increasing it"}}`

func TestParseContextOverflow(t *testing.T) {
	for _, tc := range []struct {
		name          string
		err           error
		ok            bool
		window, total int
	}{
		{"logged proxy error", errors.New(loggedOverflow), true, 32768, 34870},
		{"wrapped", fmt.Errorf("chat stream (turn 0): %w", errors.New(loggedOverflow)), true, 32768, 34870},
		{"openai wording", errors.New(`http status 400: {"error":{"message":"This model's maximum context length is 8192 tokens. However, your messages resulted in 9000 tokens.","code":"context_length_exceeded"}}`), true, 8192, 9000},
		{"anthropic wording, no numbers", errors.New(`http status 400: {"error":{"message":"prompt is too long"}}`), true, 0, 0},
		{"anthropic wording with sizes", errors.New(`http status 400: {"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`), true, 200000, 210000},
		{"other 400", errors.New(`http status 400: {"error":{"message":"invalid tool schema"}}`), false, 0, 0},
		{"marker without 400", errors.New(`http status 500: maximum context length is 8192`), false, 0, 0},
		{"nil", nil, false, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, ok := parseContextOverflow(tc.err)
			if ok != tc.ok || o.window != tc.window || o.total != tc.total {
				t.Fatalf("got (%+v, %v), want window %d total %d ok %v", o, ok, tc.window, tc.total, tc.ok)
			}
		})
	}
}

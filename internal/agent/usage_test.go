package agent

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// UsageStats totals real per-turn usage reported by the backend. The mock
// daemon reports prompt_eval_count/eval_count on the final chunk.
func TestAgentUsageStatsAccumulatesAcrossTurns(t *testing.T) {
	server := mockOllamaChat(t, [][]mockStreamChunk{
		usageResponse(10, 5, "first answer"),
		usageResponse(20, 8, "second answer"),
	})
	defer server.Close()

	var out bytes.Buffer
	a := New(Config{
		Endpoint: server.URL,
		Model:    "test-model",
		Output:   &out,
		Confirm:  alwaysConfirm(),
	})

	if err := a.Run(context.Background(), "turn one"); err != nil {
		t.Fatalf("turn one: %v", err)
	}
	if err := a.Run(context.Background(), "turn two"); err != nil {
		t.Fatalf("turn two: %v", err)
	}

	in, outTok, turns := a.UsageStats()
	if in != 30 {
		t.Errorf("tokens in = %d, want 30", in)
	}
	if outTok != 13 {
		t.Errorf("tokens out = %d, want 13", outTok)
	}
	if turns != 2 {
		t.Errorf("turns = %d, want 2", turns)
	}
}

func TestAgentUsageStatsZeroWhenBackendReportsNone(t *testing.T) {
	// A daemon that does not report usage leaves the accumulator untouched;
	// turns stays 0 so callers distinguish "no data" from "zero tokens".
	server := mockOllamaChat(t, [][]mockStreamChunk{
		textResponse("no usage reported"),
	})
	defer server.Close()

	var out bytes.Buffer
	a := New(Config{
		Endpoint: server.URL,
		Model:    "test-model",
		Output:   &out,
		Confirm:  alwaysConfirm(),
	})

	if err := a.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run: %v", err)
	}

	in, outTok, turns := a.UsageStats()
	if in != 0 || outTok != 0 || turns != 0 {
		t.Errorf("usage = (%d, %d, %d), want (0, 0, 0) when backend reports none", in, outTok, turns)
	}
}

func TestAgentUsageStatsSkipsFailedTurns(t *testing.T) {
	// A turn that errors mid-stream must not contribute usage.
	server := mockOllamaChat(t, [][]mockStreamChunk{
		usageResponse(10, 5, "ok answer"),
		[]mockStreamChunk{{Message: mockChunkMessage{Role: "assistant", Content: "partial"}, Done: false}},
	})
	defer server.Close()

	var out bytes.Buffer
	a := New(Config{
		Endpoint: server.URL,
		Model:    "test-model",
		Output:   &out,
		Confirm:  alwaysConfirm(),
	})

	if err := a.Run(context.Background(), "good turn"); err != nil {
		t.Fatalf("good turn: %v", err)
	}
	// Second turn: server has no final chunk queued; force an error by
	// calling with a cancelled context instead.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = a.Run(ctx, "cancelled turn")

	in, outTok, turns := a.UsageStats()
	if in != 10 || outTok != 5 || turns != 1 {
		t.Errorf("usage = (%d, %d, %d), want (10, 5, 1)", in, outTok, turns)
	}
	_ = strings.TrimSpace
}

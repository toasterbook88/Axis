package console

import (
	"fmt"
	"testing"
	"time"
)

// Regression for the v0.18.0 console panic: stream was a strings.Builder
// VALUE in a value-receiver Model — the second StreamChunkMsg through the
// Elm loop (Update -> route) hit strings.Builder.copyCheck and killed the
// program. With a *strings.Builder the two chunks must concatenate.
func TestModelStreamBuilderSurvivesElmCopies(t *testing.T) {
	m := NewModel(Options{})
	first, _ := m.Update(StreamChunkMsg{Turn: m.Turn(), Text: "chunk-one"})

	out := make(chan Model, 1)
	go func() {
		second, _ := first.(Model).Update(StreamChunkMsg{Turn: m.Turn(), Text: "chunk-two"})
		out <- second.(Model)
	}()
	select {
	case second := <-out:
		if got := fmt.Sprint(second.stream); got != "chunk-onechunk-two" {
			t.Fatalf("streamed text lost across Update copies: %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for second chunk")
	}
}

package agent

import (
	"io"
	"sync"
	"testing"
)

// The console's startup goroutine and /model call SetModel while a turn may
// be building subagents. Run under -race; an unguarded read of the model name
// is reported as a data race.
func TestSetModelConcurrentWithChildBuild(t *testing.T) {
	a := New(Config{
		Backend:     noopChatBackend{},
		MaxTurns:    1,
		MaxTokens:   200,
		Output:      io.Discard,
		ToolContext: NewToolContext(&RuntimeView{}, nil),
	})
	const iterations = 200
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			a.SetModel([]string{"model-a", "model-b"}[i%2])
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			if child := a.buildChildAgent(1, "", ""); child == nil {
				t.Error("nil child")
				return
			}
		}
	}()
	wg.Wait()
}

package agent

import (
	"io"
	"testing"
)

func newBudgetAgent(maxTokens, window int) *Agent {
	return New(Config{
		Backend:       noopChatBackend{},
		MaxTurns:      1,
		MaxTokens:     maxTokens,
		ContextWindow: window,
		Output:        io.Discard,
		ToolContext:   NewToolContext(&RuntimeView{}, nil),
	})
}

func TestBudgetUsesSmallestKnownWindow(t *testing.T) {
	for _, tc := range []struct {
		name      string
		maxTokens int
		window    int
		learned   int
		wantB     int
		wantU     int
	}{
		{"no window", 32768, 0, 0, 32768, 28672},
		{"target window below max", 32768, 16384, 0, 16384, 14336},
		{"max below target window", 8192, 32768, 0, 8192, 7168},
		{"learned below target", 32768, 32768, 20000, 20000, 17500},
		{"learned without target", 65536, 0, 32768, 32768, 28672},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newBudgetAgent(tc.maxTokens, tc.window)
			if tc.learned > 0 {
				a.learnWindow(a.backendGeneration(), tc.learned)
			}
			b, u := a.budget()
			if b != tc.wantB || u != tc.wantU {
				t.Fatalf("budget = (%d, %d), want (%d, %d)", b, u, tc.wantB, tc.wantU)
			}
		})
	}
}

func TestLearnedWindowNeverRises(t *testing.T) {
	a := newBudgetAgent(65536, 0)
	gen := a.backendGeneration()
	a.learnWindow(gen, 20000)
	a.learnWindow(gen, 40000)
	if b, _ := a.budget(); b != 20000 {
		t.Fatalf("B = %d, want 20000", b)
	}
}

// An overflow from a backend that was switched away mid-round teaches the
// new backend nothing.
func TestLearnedWindowIgnoresOldBackend(t *testing.T) {
	a := newBudgetAgent(65536, 0)
	old := a.backendGeneration()
	a.SetBackend(noopChatBackend{}, BackendLocal, 0)
	a.learnWindow(old, 8192)
	if b, _ := a.budget(); b != 65536 {
		t.Fatalf("B = %d, want 65536", b)
	}
}

func TestSetBackendResetsLearnedState(t *testing.T) {
	a := newBudgetAgent(65536, 0)
	gen := a.backendGeneration()
	a.learnWindow(gen, 20000)
	a.learnFactor(gen, 1.5)
	a.SetBackend(noopChatBackend{}, BackendLocal, 16384)
	if b, _ := a.budget(); b != 16384 {
		t.Fatalf("B = %d, want 16384", b)
	}
	if k := a.estimateFactor(); k != 1.0 {
		t.Fatalf("k = %v, want 1.0", k)
	}
}

func TestEstimateFactorRatchetsAndIsCapped(t *testing.T) {
	a := newBudgetAgent(65536, 0)
	gen := a.backendGeneration()
	if !a.learnFactor(gen, 1.3) || !a.learnFactor(gen, 1.4) {
		t.Fatal("ratio within the cap not learned")
	}
	if k := a.estimateFactor(); k != 1.4 {
		t.Fatalf("k = %v, want 1.4", k)
	}
	a.learnFactor(gen, 1.2)
	if k := a.estimateFactor(); k != 1.4 {
		t.Fatalf("k fell to %v", k)
	}
	if a.learnFactor(gen, 7) {
		t.Fatal("absurd ratio learned")
	}
	if k := a.estimateFactor(); k != 1.4 {
		t.Fatalf("k = %v after absurd ratio, want 1.4", k)
	}
}

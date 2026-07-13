package ui

import (
	"bytes"
	"context"
	"testing"
)

type panicInputReader struct{}

func (panicInputReader) Read([]byte) (int, error) {
	panic("input handler panic")
}

func TestSelectRecoversInputPanic(t *testing.T) {
	t.Skip("RED: pending fix #5 — see EXECUTION-PLAN.md")

	terminal := NewStdTerminal(panicInputReader{}, &bytes.Buffer{})
	_, err := Select(context.Background(), terminal, "Choose", []SelectOption{{ID: "one", Label: "One"}})
	if err == nil {
		t.Fatal("expected Select to return an input panic error")
	}
}

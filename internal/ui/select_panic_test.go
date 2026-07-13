package ui

import (
	"context"
	"io"
	"os"
	"testing"

	"golang.org/x/term"
)

type panicInputReader struct{}

func (panicInputReader) Read([]byte) (int, error) {
	panic("input handler panic")
}

func TestSelectRecoversInputPanic(t *testing.T) {
	oldMakeRaw := termMakeRaw
	oldRestore := termRestore
	oldGetSize := termGetSize
	oldReadKey := readKeyFunc
	t.Cleanup(func() {
		termMakeRaw = oldMakeRaw
		termRestore = oldRestore
		termGetSize = oldGetSize
		readKeyFunc = oldReadKey
	})

	termMakeRaw = func(int) (*term.State, error) { return &term.State{}, nil }
	termRestore = func(int, *term.State) error { return nil }
	termGetSize = func(int) (int, int, error) { return 80, 24, nil }
	readKeyFunc = func(io.Reader, int, bool) (KeyAction, error) {
		var reader panicInputReader
		reader.Read(nil)
		return ActionNone, nil
	}

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create input pipe: %v", err)
	}
	defer inR.Close()
	defer inW.Close()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create output pipe: %v", err)
	}
	defer outR.Close()
	defer outW.Close()

	terminal := &mockFileTerminal{in: inR, out: outW, isTTY: true}
	_, selectErr := Select(context.Background(), terminal, "Choose", []SelectOption{{ID: "one", Label: "One"}})
	if selectErr == nil {
		t.Fatal("expected Select to return an input panic error")
	}
}

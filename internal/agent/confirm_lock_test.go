package agent

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

func TestRunShellConfirmDoesNotHoldDispatchMu(t *testing.T) {
	a := &Agent{
		safety: func(string) (bool, string, int) { return true, "", 0 },
		output: io.Discard,
	}
	a.SetRunShell(func(context.Context, string) (string, error) {
		return "ok", nil
	})
	a.SetConfirm(func(string, string, int) ConfirmResult {
		done := make(chan struct{})
		go func() {
			_ = a.Autonomy()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("Autonomy blocked while the shell prompt was open")
		}
		return ConfirmYes
	})

	got, err := a.dispatchShell(context.Background(), json.RawMessage(`{"command":"echo hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != "ok" {
		t.Fatalf("result = %q, want ok", got)
	}
}

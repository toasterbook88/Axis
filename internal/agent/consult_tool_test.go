package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/toasterbook88/axis/internal/chat"
)

func consultToolArgs(seat, prompt string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"seat":%q,"prompt":%q}`, seat, prompt))
}

func TestConsultModelToolLeavesTheDriverInPlace(t *testing.T) {
	driver := &stubBackend{name: "driver", response: chat.Message{Role: chat.RoleAssistant, Content: "driver"}}
	seat := &consultRecorder{response: chat.Message{Role: chat.RoleAssistant, Content: "a short note"}}
	a := New(Config{Backend: driver, Model: "driver-model", Output: io.Discard, Confirm: alwaysConfirm()})
	a.SetConsultResolver(func(context.Context, string) (ChatBackend, string, func(), error) {
		return seat, "tool-note", nil, nil
	})

	got, err := a.dispatchToolCall(context.Background(), chat.ToolCall{
		Function: chat.ToolCallFunction{Name: consultModelTool, Arguments: consultToolArgs("seat-a", "Review this diff and name one risk.")},
	})
	if err != nil {
		t.Fatalf("consult_model: %v", err)
	}
	if got != "a short note" {
		t.Fatalf("tool result = %q", got)
	}
	if a.Model() != "driver-model" {
		t.Fatalf("driver model = %q", a.Model())
	}
	if a.Backend() != ChatBackend(driver) {
		t.Fatal("consult_model replaced the driver backend")
	}
	if driver.callCount() != 0 {
		t.Fatalf("driver calls = %d, want 0", driver.callCount())
	}
	if seat.calls != 1 || len(seat.tools) != 0 {
		t.Fatalf("seat calls=%d tools=%d", seat.calls, len(seat.tools))
	}
	if len(seat.msgs) != 1 || seat.msgs[0].Role != chat.RoleUser || seat.msgs[0].Content != "Review this diff and name one risk." {
		t.Fatalf("seat messages = %+v", seat.msgs)
	}
}

func TestConsultModelToolReportsSeatErrors(t *testing.T) {
	driver := &stubBackend{name: "driver", response: chat.Message{Role: chat.RoleAssistant, Content: "driver"}}
	a := New(Config{Backend: driver, Model: "driver-model", Output: io.Discard, Confirm: alwaysConfirm()})
	a.SetConsultResolver(func(_ context.Context, ref string) (ChatBackend, string, func(), error) {
		switch ref {
		case "tool-down":
			return &consultRecorder{err: errors.New("connection refused")}, "tool-down", nil, nil
		case "tool-hide":
			return &consultRecorder{err: errors.New("ollama pull tool-hide")}, "tool-hide", nil, nil
		case "tool-quiet":
			return &consultRecorder{err: errors.New("could not start ollama serve")}, "tool-quiet", nil, nil
		case "tool-blank":
			return &consultRecorder{response: chat.Message{Role: chat.RoleAssistant, Content: " \n"}}, "tool-blank", nil, nil
		default:
			return nil, "", nil, fmt.Errorf("consult: seat %q is not answering", ref)
		}
	})

	_, err := a.dispatchToolCall(context.Background(), chat.ToolCall{
		Function: chat.ToolCallFunction{Name: consultModelTool, Arguments: consultToolArgs("tool-down", "review")},
	})
	if err == nil || !strings.Contains(err.Error(), "tool-down") || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("seat error = %v", err)
	}
	_, err = a.dispatchToolCall(context.Background(), chat.ToolCall{
		Function: chat.ToolCallFunction{Name: consultModelTool, Arguments: consultToolArgs("tool-hide", "review")},
	})
	if err == nil || !strings.Contains(err.Error(), "tool-hide") || !strings.Contains(err.Error(), "is not answering") || strings.Contains(strings.ToLower(err.Error()), "pull") || strings.Contains(err.Error(), "ollama") {
		t.Fatalf("seat pull error = %v", err)
	}
	_, err = a.dispatchToolCall(context.Background(), chat.ToolCall{
		Function: chat.ToolCallFunction{Name: consultModelTool, Arguments: consultToolArgs("tool-quiet", "review")},
	})
	if err == nil || !strings.Contains(err.Error(), "tool-quiet") || !strings.Contains(err.Error(), "is not answering") || strings.Contains(err.Error(), "ollama") || strings.Contains(err.Error(), "serve") {
		t.Fatalf("seat serve error = %v", err)
	}
	_, err = a.dispatchToolCall(context.Background(), chat.ToolCall{
		Function: chat.ToolCallFunction{Name: consultModelTool, Arguments: consultToolArgs("tool-blank", "review")},
	})
	if err == nil || !strings.Contains(err.Error(), "tool-blank") || !strings.Contains(err.Error(), "returned no visible text") {
		t.Fatalf("blank text error = %v", err)
	}
	if a.Backend() != ChatBackend(driver) || a.Model() != "driver-model" || driver.callCount() != 0 {
		t.Fatal("a seat error changed the driver")
	}

	bare := New(Config{Backend: driver, Model: "driver-model", Output: io.Discard})
	_, err = bare.ExecuteToolDirect(context.Background(), consultModelTool, consultToolArgs("missing-seat", "review"))
	if err == nil || !strings.Contains(err.Error(), "missing-seat") || !strings.Contains(err.Error(), "is not answering") {
		t.Fatalf("nil resolver error = %v", err)
	}
	if driver.callCount() != 0 {
		t.Fatalf("nil resolver called the driver %d times", driver.callCount())
	}
}

func TestConsultModelClosesResolverCleanup(t *testing.T) {
	driver := &stubBackend{name: "driver", response: chat.Message{Role: chat.RoleAssistant, Content: "driver"}}
	seat := &consultRecorder{response: chat.Message{Role: chat.RoleAssistant, Content: "a note"}}
	a := New(Config{Backend: driver, Model: "driver-model", Output: io.Discard, Confirm: alwaysConfirm()})
	closed := 0
	a.SetConsultResolver(func(context.Context, string) (ChatBackend, string, func(), error) {
		return seat, "seat-cleanup", func() { closed++ }, nil
	})
	if _, err := a.dispatchToolCall(context.Background(), chat.ToolCall{
		Function: chat.ToolCallFunction{Name: consultModelTool, Arguments: consultToolArgs("seat-cleanup", "hello")},
	}); err != nil {
		t.Fatal(err)
	}
	if closed != 1 {
		t.Fatalf("resolver cleanup calls = %d, want 1", closed)
	}
}

func TestConsultModelSameResolvedIDIsSingleSlot(t *testing.T) {
	isolateConsultSlots(t)
	synctest.Test(t, func(t *testing.T) {
		seat := &slotSeat{entered: make(chan struct{}, 2), release: make(chan struct{})}
		r := NewToolRegistry(NewToolContext(&RuntimeView{}, nil))
		r.SetConsultResolver(func(_ context.Context, ref string) (ChatBackend, string, func(), error) {
			switch ref {
			case "alias-a", "seat-a":
				return seat, "resolved-shared", nil, nil
			default:
				return nil, "", nil, fmt.Errorf("consult: seat %q is not answering", ref)
			}
		})
		go r.Execute(context.Background(), consultModelTool, consultToolArgs("alias-a", "one"))
		<-seat.entered
		go r.Execute(context.Background(), consultModelTool, consultToolArgs("seat-a", "two"))
		synctest.Wait()
		if seat.maxInFlight() != 1 {
			t.Fatalf("in-flight consults = %d, want 1", seat.maxInFlight())
		}
		close(seat.release)
		synctest.Wait()
	})
}

func TestConsultModelDifferentResolvedIDsMayOverlap(t *testing.T) {
	isolateConsultSlots(t)
	synctest.Test(t, func(t *testing.T) {
		seat := &slotSeat{entered: make(chan struct{}, 2), release: make(chan struct{})}
		r := NewToolRegistry(NewToolContext(&RuntimeView{}, nil))
		r.SetConsultResolver(func(_ context.Context, ref string) (ChatBackend, string, func(), error) {
			switch ref {
			case "seat-a":
				return seat, "resolved-a", nil, nil
			case "seat-b":
				return seat, "resolved-b", nil, nil
			default:
				return nil, "", nil, fmt.Errorf("consult: seat %q is not answering", ref)
			}
		})
		go r.Execute(context.Background(), consultModelTool, consultToolArgs("seat-a", "one"))
		<-seat.entered
		go r.Execute(context.Background(), consultModelTool, consultToolArgs("seat-b", "two"))
		<-seat.entered
		if seat.maxInFlight() != 2 {
			t.Fatalf("in-flight consults = %d, want 2", seat.maxInFlight())
		}
		close(seat.release)
		synctest.Wait()
	})
}

func TestConsultModelClusterDispatchDoesNotRunTheSeat(t *testing.T) {
	driver := &stubBackend{name: "driver", response: chat.Message{Role: chat.RoleAssistant, Content: "driver"}}
	seat := &consultRecorder{response: chat.Message{Role: chat.RoleAssistant, Content: "nope"}}
	resolved := 0
	a := New(Config{
		Backend: driver, Model: "driver-model", Output: io.Discard,
		Confirm: alwaysConfirm(), Autonomy: AutonomyCluster,
	})
	a.SetConsultResolver(func(context.Context, string) (ChatBackend, string, func(), error) {
		resolved++
		return seat, "seat-a", nil, nil
	})
	_, err := a.dispatchToolCall(context.Background(), chat.ToolCall{
		Function: chat.ToolCallFunction{Name: consultModelTool, Arguments: consultToolArgs("seat-a", "hello")},
	})
	if err == nil || !strings.Contains(err.Error(), "not available in this mode") {
		t.Fatalf("cluster dispatch err = %v", err)
	}
	if resolved != 0 || seat.calls != 0 || driver.callCount() != 0 {
		t.Fatalf("cluster dispatch resolved=%d seat=%d driver=%d", resolved, seat.calls, driver.callCount())
	}
}

func TestConsultModelScoreZeroAsks(t *testing.T) {
	if isReadOnlyTool(consultModelTool) {
		t.Fatal("consult_model is read-only")
	}
	rec := &recorderConfirm{}
	for _, mode := range []AutonomyMode{AutonomyDefault, AutonomyEdit, AutonomyFull} {
		rec.called = false
		got := autonomyConfirm(mode, rec.confirm)(consultModelTool, "ask", 0)
		if got != ConfirmNo || !rec.called {
			t.Fatalf("mode %s score 0: result %v fallback %v", mode, got, rec.called)
		}
	}
	rec.called = false
	got := AutoApproveConfirm(70, rec.confirm)(consultModelTool, "ask", 0)
	if got != ConfirmNo || !rec.called {
		t.Fatalf("auto-approve score 0: result %v fallback %v", got, rec.called)
	}
}

func TestConsultModelSessionAlwaysSkipsLaterConfirms(t *testing.T) {
	rc := &recordingConfirm{}
	seat := &consultRecorder{response: chat.Message{Role: chat.RoleAssistant, Content: "noted"}}
	driver := &stubBackend{name: "driver", response: chat.Message{Role: chat.RoleAssistant, Content: "driver"}}
	a := New(Config{
		Backend: driver, Model: "driver-model", Output: io.Discard,
		Confirm: rc.confirm, Autonomy: AutonomyEdit,
	})
	a.SetConsultResolver(func(context.Context, string) (ChatBackend, string, func(), error) {
		return seat, "tool-always", nil, nil
	})
	args := string(consultToolArgs("seat-a", "hello"))
	dispatch(t, a, consultModelTool, args)
	dispatch(t, a, consultModelTool, args)
	if got := rc.count(consultModelTool); got != 1 {
		t.Fatalf("prompts = %d, want 1", got)
	}
	if seat.calls != 2 {
		t.Fatalf("seat calls = %d, want 2", seat.calls)
	}
	if driver.callCount() != 0 || a.Model() != "driver-model" {
		t.Fatal("session approval changed the driver")
	}
}

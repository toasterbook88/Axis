package agent

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/toasterbook88/axis/internal/chat"
)

// consultRecorder is a seat backend for consult tests. It records the tools
// and messages it was given and never stands in for the driver.
type consultRecorder struct {
	calls    int
	tools    []chat.ToolDef
	msgs     []chat.Message
	response chat.Message
	err      error
}

func (c *consultRecorder) ChatStream(ctx context.Context, msgs []chat.Message, tools []chat.ToolDef, w io.Writer) (chat.Message, error) {
	if err := ctx.Err(); err != nil {
		return chat.Message{}, err
	}
	c.calls++
	c.tools = tools
	c.msgs = append([]chat.Message(nil), msgs...)
	if c.err != nil {
		return chat.Message{}, c.err
	}
	return c.response, nil
}

func TestConsultLeavesTheDriverInPlace(t *testing.T) {
	driver := &stubBackend{name: "driver", response: chat.Message{Role: chat.RoleAssistant, Content: "driver"}}
	seat := &consultRecorder{response: chat.Message{Role: chat.RoleAssistant, Content: "a short note"}}
	a := New(Config{Backend: driver, Model: "driver-model", Output: io.Discard})

	got, err := consult(context.Background(), seat, consultRequest{
		SeatID: "seat-a",
		Prompt: "Review this diff and name one risk.",
	})
	if err != nil {
		t.Fatalf("consult: %v", err)
	}
	if got != "a short note" {
		t.Fatalf("consult text = %q", got)
	}
	if a.Model() != "driver-model" {
		t.Fatalf("driver model = %q", a.Model())
	}
	if a.Backend() != ChatBackend(driver) {
		t.Fatal("consult replaced the driver backend")
	}
	if driver.callCount() != 0 {
		t.Fatalf("driver calls = %d, want 0", driver.callCount())
	}
	if seat.calls != 1 {
		t.Fatalf("seat calls = %d, want 1", seat.calls)
	}
	if len(seat.tools) != 0 {
		t.Fatalf("consult passed %d tools", len(seat.tools))
	}
	if len(seat.msgs) != 1 || seat.msgs[0].Role != chat.RoleUser || seat.msgs[0].Content != "Review this diff and name one risk." {
		t.Fatalf("seat messages = %+v", seat.msgs)
	}
}

func TestConsultDownSeatReturnsError(t *testing.T) {
	driver := &stubBackend{name: "driver", response: chat.Message{Role: chat.RoleAssistant, Content: "driver"}}
	a := New(Config{Backend: driver, Model: "driver-model", Output: io.Discard})

	t.Run("nil seat", func(t *testing.T) {
		_, err := consult(context.Background(), nil, consultRequest{SeatID: "seat-a", Prompt: "review"})
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "seat-a") {
			t.Fatalf("error %q does not name the seat", err)
		}
		if a.Backend() != ChatBackend(driver) || a.Model() != "driver-model" || driver.callCount() != 0 {
			t.Fatal("a down seat changed the driver")
		}
	})

	t.Run("seat error", func(t *testing.T) {
		seat := &consultRecorder{err: errors.New("connection refused")}
		_, err := consult(context.Background(), seat, consultRequest{SeatID: "seat-b", Prompt: "review"})
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "seat-b") || !strings.Contains(err.Error(), "connection refused") {
			t.Fatalf("error = %q", err)
		}
		if a.Backend() != ChatBackend(driver) || driver.callCount() != 0 {
			t.Fatal("a seat error changed the driver")
		}
	})
}

// slotSeat counts how many consults are inside ChatStream at once.
type slotSeat struct {
	mu  sync.Mutex
	in  int
	max int

	entered chan struct{}
	release chan struct{}
}

func (s *slotSeat) ChatStream(ctx context.Context, msgs []chat.Message, tools []chat.ToolDef, w io.Writer) (chat.Message, error) {
	s.mu.Lock()
	s.in++
	if s.in > s.max {
		s.max = s.in
	}
	s.mu.Unlock()
	s.entered <- struct{}{}
	<-s.release
	s.mu.Lock()
	s.in--
	s.mu.Unlock()
	return chat.Message{Role: chat.RoleAssistant, Content: "ok"}, nil
}

func (s *slotSeat) maxInFlight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.max
}

func TestConsultSingleSlotSerializesOneSeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		seat := &slotSeat{entered: make(chan struct{}, 2), release: make(chan struct{})}
		req := consultRequest{SeatID: "slot-a", Prompt: "judge this", SingleSlot: true}
		go consult(context.Background(), seat, req)
		<-seat.entered
		go consult(context.Background(), seat, req)
		synctest.Wait()
		if seat.maxInFlight() != 1 {
			t.Fatalf("in-flight consults = %d, want 1", seat.maxInFlight())
		}
		close(seat.release)
		synctest.Wait()
	})
}

func TestConsultEmptyVisibleTextIsAnError(t *testing.T) {
	driver := &stubBackend{name: "driver", response: chat.Message{Role: chat.RoleAssistant, Content: "driver"}}
	a := New(Config{Backend: driver, Model: "driver-model", Output: io.Discard})
	seat := &consultRecorder{response: chat.Message{Role: chat.RoleAssistant, Content: "  \n"}}

	_, err := consult(context.Background(), seat, consultRequest{SeatID: "seat-a", Prompt: "judge this"})
	if err == nil {
		t.Fatal("expected an error for empty visible text")
	}
	if !strings.Contains(err.Error(), "seat-a") {
		t.Fatalf("error %q does not name the seat", err)
	}
	if a.Backend() != ChatBackend(driver) || a.Model() != "driver-model" || driver.callCount() != 0 {
		t.Fatal("an empty seat answer changed the driver")
	}
}

func TestConsultDifferentSingleSlotSeatsMayOverlap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		seat := &slotSeat{entered: make(chan struct{}, 2), release: make(chan struct{})}
		go consult(context.Background(), seat, consultRequest{SeatID: "seat-a", Prompt: "one", SingleSlot: true})
		<-seat.entered
		go consult(context.Background(), seat, consultRequest{SeatID: "seat-b", Prompt: "two", SingleSlot: true})
		<-seat.entered
		if seat.maxInFlight() != 2 {
			t.Fatalf("in-flight consults = %d, want 2", seat.maxInFlight())
		}
		close(seat.release)
		synctest.Wait()
	})
}

func TestConsultCancelledCallDoesNotReachTheSeat(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// The seat ignores cancellation. The test fails unless consult checks
	// the context before the call.
	blind := &blindSeat{}
	_, err := consult(ctx, blind, consultRequest{SeatID: "seat-a", Prompt: "review"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if blind.calls != 0 {
		t.Fatalf("seat calls = %d, want 0", blind.calls)
	}
}

type blindSeat struct {
	calls int
}

func (b *blindSeat) ChatStream(ctx context.Context, msgs []chat.Message, tools []chat.ToolDef, w io.Writer) (chat.Message, error) {
	b.calls++
	return chat.Message{Role: chat.RoleAssistant, Content: "ran"}, nil
}

func TestConsultModelIsVisibleOutsideCluster(t *testing.T) {
	if toolVisible(consultModelTool, ScopeCluster) {
		t.Fatal("consult_model is visible in cluster scope")
	}
	for _, scope := range []ToolScope{ScopeObserve, ScopeEdit, ScopeExec} {
		if !toolVisible(consultModelTool, scope) {
			t.Fatalf("consult_model is hidden in scope %s", scope)
		}
	}
	prompt := VisibleToolPrompt([]string{consultModelTool, "read_file"}, ScopeObserve)
	if !strings.Contains(prompt, "`consult_model` sends the prompt to one other configured model.") {
		t.Fatalf("observe prompt = %q", prompt)
	}
	if !strings.Contains(prompt, "/autonomy edit") || !strings.Contains(prompt, "/autonomy full") {
		t.Fatalf("observe prompt dropped the write or shell limit: %q", prompt)
	}
	plain := VisibleToolPrompt([]string{"read_file"}, ScopeObserve)
	if strings.Contains(plain, "one other configured model") {
		t.Fatalf("observe prompt mentioned consult_model when it is not offered: %q", plain)
	}
}

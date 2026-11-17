package agent

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/toasterbook88/axis/internal/chat"
)

// consultRequest is one question for a seat other than the driver.
// The seat answers text. It does not receive the driver's tools.
// SingleSlot serializes consults that name the same SeatID in this process.
type consultRequest struct {
	SeatID     string
	Prompt     string
	SingleSlot bool
}

// consultSlots hands out one token per seat. The map lock is not held while a
// consult waits or calls the seat. Two processes can still share one server.
var (
	consultSlotsMu sync.Mutex
	consultSlots   = map[string]chan struct{}{}
)

func acquireConsultSlot(ctx context.Context, seat string) (func(), error) {
	consultSlotsMu.Lock()
	token := consultSlots[seat]
	if token == nil {
		token = make(chan struct{}, 1)
		token <- struct{}{}
		consultSlots[seat] = token
	}
	consultSlotsMu.Unlock()
	select {
	case <-token:
		return func() { token <- struct{}{} }, nil
	case <-ctx.Done():
		// The slot stays with its holder. A cancelled waiter returns
		// without touching the token, so the next call can take it.
		return func() {}, ctx.Err()
	}
}

// consult asks seat to answer Prompt. It does not take the agent, so it
// cannot replace the driver backend or the driver model. The caller surfaces
// a returned error as the driver's next tool result.
func consult(ctx context.Context, seat ChatBackend, req consultRequest) (string, error) {
	if seat == nil {
		return "", fmt.Errorf("consult: seat %q is not answering", req.SeatID)
	}
	if req.SingleSlot {
		unlock, err := acquireConsultSlot(ctx, req.SeatID)
		if err != nil {
			return "", err
		}
		defer unlock()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	msg, err := seat.ChatStream(ctx, []chat.Message{{
		Role:    chat.RoleUser,
		Content: req.Prompt,
	}}, nil, io.Discard)
	if err != nil {
		if consultHidesSeat(err) {
			return "", fmt.Errorf("consult: seat %q is not answering", req.SeatID)
		}
		return "", fmt.Errorf("consult: seat %q: %w", req.SeatID, err)
	}
	text := strings.TrimSpace(msg.Content)
	if text == "" {
		return "", fmt.Errorf("consult: seat %q returned no visible text", req.SeatID)
	}
	return text, nil
}

// consultHidesSeat reports a seat error that would tell the driver to pull
// a model or start a server. The driver sees "is not answering" instead.
func consultHidesSeat(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "pull") || strings.Contains(msg, "ollama serve")
}

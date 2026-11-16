package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// consultModelTool asks one configured model for text and leaves the driver
// in place. It is not a read, so confirmation cannot treat a score of 0 as
// low risk.
const consultModelTool = "consult_model"

// ConsultResolver turns a seat reference into a backend and the slot key.
// The slot key is the resolved model choice id. A nil resolver means the
// tool cannot reach a seat.
type ConsultResolver func(ctx context.Context, seatRef string) (seat ChatBackend, slotID string, err error)

// SetConsultResolver installs the seat resolver. The agent command supplies
// the resolver that knows the configured model list. consult itself stays
// free of the agent.
func (r *ToolRegistry) SetConsultResolver(fn ConsultResolver) {
	if r == nil {
		return
	}
	r.consultResolver = fn
}

// SetConsultResolver installs the seat resolver on the agent's tool registry.
func (a *Agent) SetConsultResolver(fn ConsultResolver) {
	if a == nil || a.tools == nil {
		return
	}
	a.tools.SetConsultResolver(fn)
}

// consultAsksOperator reports the tool that must reach the operator even
// when its safety score is 0.
func consultAsksOperator(name string) bool {
	return name == consultModelTool
}

func (r *ToolRegistry) registerConsultModel() {
	r.add(consultModelTool,
		"Ask one configured model, other than the current one, for text. seat is a model id or model name from the same list as /model. prompt is the question. The seat does not receive tools, and the current model stays in place.",
		json.RawMessage(`{"type":"object","properties":{"seat":{"type":"string","description":"Model id or model name from the configured choice list"},"prompt":{"type":"string","description":"The question to send to that model"}},"required":["seat","prompt"]}`),
		r.execConsultModel,
	)
}

type consultModelArgs struct {
	Seat   string `json:"seat"`
	Prompt string `json:"prompt"`
}

func (r *ToolRegistry) execConsultModel(ctx context.Context, args json.RawMessage) (string, error) {
	var in consultModelArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return "", fmt.Errorf("consult: invalid arguments")
		}
	}
	seatRef := strings.TrimSpace(in.Seat)
	if strings.TrimSpace(in.Prompt) == "" {
		return "", fmt.Errorf("consult: prompt is empty")
	}
	if r == nil || r.consultResolver == nil {
		return "", fmt.Errorf("consult: seat %q is not answering", seatRef)
	}
	backend, slotID, err := r.consultResolver(ctx, seatRef)
	if err != nil {
		return "", err
	}
	slotID = strings.TrimSpace(slotID)
	if backend == nil || slotID == "" {
		return "", fmt.Errorf("consult: seat %q is not answering", seatRef)
	}
	return consult(ctx, backend, consultRequest{
		SeatID:     slotID,
		Prompt:     in.Prompt,
		SingleSlot: true,
	})
}

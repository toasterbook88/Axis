package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/toasterbook88/axis/internal/execution"
)

const (
	RunStreamContentType = "application/x-ndjson"
	runStreamQueryKey    = "stream"

	RunStreamEventReady       = "ready"
	RunStreamEventStateChange = "state_change"
	RunStreamEventStdout      = "stdout"
	RunStreamEventStderr      = "stderr"
	RunStreamEventResult      = "result"
)

// RunStreamEvent is one NDJSON event in the streaming /run contract.
type RunStreamEvent struct {
	Type    string                            `json:"type"`
	Trigger string                            `json:"trigger,omitempty"`
	Text    string                            `json:"text,omitempty"`
	Result  *execution.GuardedExecutionResult `json:"result,omitempty"`
}

// NormalizeRunResult folds an execution error into the final guarded result so
// both streamed and buffered HTTP paths report the same terminal payload.
func NormalizeRunResult(resp execution.GuardedExecutionResult, runErr error) execution.GuardedExecutionResult {
	if runErr == nil {
		return resp
	}
	resp.OK = false
	if resp.Error == "" {
		resp.Error = runErr.Error()
	}
	return resp
}

// WantsRunStream reports whether the caller requested the streaming /run
// contract via query string or Accept header.
func WantsRunStream(r *http.Request) bool {
	if r == nil {
		return false
	}
	if v := strings.TrimSpace(r.URL.Query().Get(runStreamQueryKey)); v != "" {
		return v == "1" || strings.EqualFold(v, "true")
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), strings.ToLower(RunStreamContentType))
}

// RunStreamEmitter emits NDJSON run events and exposes stdout/stderr writers
// that preserve event framing.
type RunStreamEmitter struct {
	mu      sync.Mutex
	enc     *json.Encoder
	flusher http.Flusher
}

func NewRunStreamEmitter(w http.ResponseWriter) (*RunStreamEmitter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming /run requires http flusher support")
	}
	w.Header().Set("Content-Type", RunStreamContentType)
	w.Header().Set("Cache-Control", "no-store")
	return &RunStreamEmitter{
		enc:     json.NewEncoder(w),
		flusher: flusher,
	}, nil
}

// WireRunStreamResponse adapts a guarded execution request to the NDJSON /run
// contract when the caller requested streaming. Existing callbacks and writers
// are preserved so HTTP handlers can layer streaming on top of their local
// refresh logic instead of replacing it.
func WireRunStreamResponse(w http.ResponseWriter, r *http.Request, req *execution.GuardedExecutionRequest) (func(execution.GuardedExecutionResult) error, bool, error) {
	if !WantsRunStream(r) {
		return nil, false, nil
	}
	emitter, err := NewRunStreamEmitter(w)
	if err != nil {
		return nil, false, err
	}
	if req != nil {
		baseReady := req.OnReady
		req.OnReady = func(resp execution.GuardedExecutionResult) {
			if baseReady != nil {
				baseReady(resp)
			}
			_ = emitter.EmitReady(resp)
		}

		baseStateChange := req.OnStateChange
		req.OnStateChange = func(ctx context.Context, trigger string, resp execution.GuardedExecutionResult) {
			if baseStateChange != nil {
				baseStateChange(ctx, trigger, resp)
			}
			_ = emitter.EmitStateChange(trigger, resp)
		}

		if req.Stdout == nil {
			req.Stdout = emitter.StdoutWriter()
		} else {
			req.Stdout = io.MultiWriter(req.Stdout, emitter.StdoutWriter())
		}
		if req.Stderr == nil {
			req.Stderr = emitter.StderrWriter()
		} else {
			req.Stderr = io.MultiWriter(req.Stderr, emitter.StderrWriter())
		}
	}
	return emitter.EmitResult, true, nil
}

func (e *RunStreamEmitter) Emit(event RunStreamEvent) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.enc.Encode(event); err != nil {
		return err
	}
	e.flusher.Flush()
	return nil
}

func (e *RunStreamEmitter) EmitReady(resp execution.GuardedExecutionResult) error {
	respCopy := resp
	return e.Emit(RunStreamEvent{
		Type:   RunStreamEventReady,
		Result: &respCopy,
	})
}

func (e *RunStreamEmitter) EmitStateChange(trigger string, resp execution.GuardedExecutionResult) error {
	respCopy := resp
	return e.Emit(RunStreamEvent{
		Type:    RunStreamEventStateChange,
		Trigger: strings.TrimSpace(trigger),
		Result:  &respCopy,
	})
}

func (e *RunStreamEmitter) EmitResult(resp execution.GuardedExecutionResult) error {
	respCopy := resp
	return e.Emit(RunStreamEvent{
		Type:   RunStreamEventResult,
		Result: &respCopy,
	})
}

func (e *RunStreamEmitter) StdoutWriter() io.Writer {
	return runStreamWriter{emitter: e, eventType: RunStreamEventStdout}
}

func (e *RunStreamEmitter) StderrWriter() io.Writer {
	return runStreamWriter{emitter: e, eventType: RunStreamEventStderr}
}

type runStreamWriter struct {
	emitter   *RunStreamEmitter
	eventType string
}

func (w runStreamWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := w.emitter.Emit(RunStreamEvent{
		Type: w.eventType,
		Text: string(p),
	}); err != nil {
		return 0, err
	}
	return len(p), nil
}

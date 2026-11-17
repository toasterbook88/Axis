package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
)

type recordingThoughtSink struct {
	mu       sync.Mutex
	buf      strings.Builder
	thoughts []string
	closed   int
}

func (s *recordingThoughtSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *recordingThoughtSink) WriteThought(thought string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.thoughts = append(s.thoughts, thought)
}

func (s *recordingThoughtSink) CloseThought() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed++
}

func TestCloudBackend_DefaultsUseIdleTimeoutNotClientTimeout(t *testing.T) {
	b1, err := NewCloudBackendWithKey("openai", "openai", "http://127.0.0.1:4000", "key", "m", 0)
	if err != nil {
		t.Fatalf("NewCloudBackendWithKey: %v", err)
	}
	b2, err := NewOpenAICompatibleBackend("http://127.0.0.1:8080", "m", "")
	if err != nil {
		t.Fatalf("NewOpenAICompatibleBackend: %v", err)
	}
	for name, b := range map[string]*CloudBackend{"cloud": b1, "openai-compat": b2} {
		if b.client.Timeout != 0 {
			t.Errorf("%s client.Timeout = %s, want 0 (governed by ctx + idle guard)", name, b.client.Timeout)
		}
		if b.streamIdleTimeout != defaultStreamIdleTimeout {
			t.Errorf("%s streamIdleTimeout = %s, want %s", name, b.streamIdleTimeout, defaultStreamIdleTimeout)
		}
	}
}

func TestCloudBackend_HealthySlowStreamSurvivesBeyondIdleWindow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, tok := range []string{"a", "b", "c", "d", "e"} {
			time.Sleep(20 * time.Millisecond)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", tok)
			if flusher != nil {
				flusher.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	b, err := NewOpenAICompatibleBackend(server.URL, "slow-local", "")
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	// Total stream ~100ms > 2x idle window (45ms), but each gap (20ms) is < 45ms.
	b.streamIdleTimeout = 45 * time.Millisecond

	resp, err := b.ChatStream(context.Background(), []chat.Message{{Role: chat.RoleUser, Content: "hi"}}, nil, io.Discard)
	if err != nil {
		t.Fatalf("healthy slow stream failed: %v", err)
	}
	if resp.Content != "abcde" {
		t.Fatalf("content = %q, want %q", resp.Content, "abcde")
	}
}

func TestCloudBackend_OpenAI_IdleTimeoutAbortsStalledStream(t *testing.T) {
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"part\"}}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-done:
		}
	}))
	defer func() {
		close(done)
		server.Close()
	}()

	b, err := NewOpenAICompatibleBackend(server.URL, "stalled-local", "")
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	b.streamIdleTimeout = 30 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = b.ChatStream(ctx, []chat.Message{{Role: chat.RoleUser, Content: "hi"}}, nil, io.Discard)
	if err == nil {
		t.Fatal("expected idle timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "stream idle timeout") {
		t.Fatalf("unexpected error = %v, want stream idle timeout wrapping DeadlineExceeded", err)
	}
}

func TestCloudBackend_Anthropic_IdleTimeoutAbortsStalledStream(t *testing.T) {
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"part\"}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-done:
		}
	}))
	defer func() {
		close(done)
		server.Close()
	}()

	b, err := NewCloudBackendWithKey("anthropic", "anthropic", server.URL, "k", "test-model", 0)
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	b.streamIdleTimeout = 30 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = b.ChatStream(ctx, []chat.Message{{Role: chat.RoleUser, Content: "hi"}}, nil, io.Discard)
	if err == nil {
		t.Fatal("expected idle timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "stream idle timeout") {
		t.Fatalf("unexpected error = %v, want stream idle timeout wrapping DeadlineExceeded", err)
	}
}

func TestCloudBackend_OpenAI_ReasoningContentStreamsToThoughtWriterOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"check \"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning\":\"nodes\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"All ok.\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	b, err := NewOpenAICompatibleBackend(server.URL, "test-reasoning-model", "")
	if err != nil {
		t.Fatalf("backend: %v", err)
	}

	// Plain writer (e.g. piped -p stdout) receives only visible answer content.
	var plain bytes.Buffer
	resp, err := b.ChatStream(context.Background(), []chat.Message{{Role: chat.RoleUser, Content: "status"}}, nil, &plain)
	if err != nil {
		t.Fatalf("ChatStream plain: %v", err)
	}
	if resp.Content != "All ok." {
		t.Fatalf("resp.Content = %q, want %q", resp.Content, "All ok.")
	}
	if plain.String() != "All ok." {
		t.Fatalf("plain writer = %q, want %q", plain.String(), "All ok.")
	}

	// ColorWriter wrapping ThoughtWriter forwards reasoning separately and keeps content clean.
	sink := &recordingThoughtSink{}
	cw := NewColorWriter(sink)
	resp2, err := b.ChatStream(context.Background(), []chat.Message{{Role: chat.RoleUser, Content: "status"}}, nil, cw)
	if err != nil {
		t.Fatalf("ChatStream thought sink: %v", err)
	}
	_ = cw.Close()
	if resp2.Content != "All ok." {
		t.Fatalf("resp2.Content = %q, want %q", resp2.Content, "All ok.")
	}
	if gotThoughts := strings.Join(sink.thoughts, ""); gotThoughts != "check nodes" {
		t.Fatalf("thoughts = %q, want %q", gotThoughts, "check nodes")
	}
	if sink.closed == 0 {
		t.Fatal("expected CloseThought to be called when reasoning ends")
	}
	if sink.buf.String() != "All ok." {
		t.Fatalf("visible answer = %q, want %q", sink.buf.String(), "All ok.")
	}
}

func TestCloudBackend_WithDisableThinking_SendsAndFallsBackOn400(t *testing.T) {
	var mu sync.Mutex
	var gotEnableThinking []any

	rejectKW := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		kw, hasKW := body["chat_template_kwargs"].(map[string]any)
		mu.Lock()
		if hasKW {
			gotEnableThinking = append(gotEnableThinking, kw["enable_thinking"])
		} else {
			gotEnableThinking = append(gotEnableThinking, nil)
		}
		reject := rejectKW
		mu.Unlock()

		if hasKW && reject {
			http.Error(w, `{"error":{"message":"Unrecognized request argument supplied: chat_template_kwargs"}}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"summary\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	// 1. Server accepts chat_template_kwargs -> receives enable_thinking: false.
	b, err := NewOpenAICompatibleBackend(server.URL, "qwen", "")
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	mu.Lock()
	rejectKW = false
	mu.Unlock()

	ctx := WithDisableThinking(context.Background())
	resp, err := b.ChatStream(ctx, []chat.Message{{Role: chat.RoleUser, Content: "summarize"}}, nil, io.Discard)
	if err != nil {
		t.Fatalf("ChatStream accept: %v", err)
	}
	if resp.Content != "summary" {
		t.Fatalf("content = %q, want %q", resp.Content, "summary")
	}

	// 2. Server rejects chat_template_kwargs -> falls back once and remembers not to resend.
	bFallback, err := NewOpenAICompatibleBackend(server.URL, "strict-openai", "")
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	mu.Lock()
	rejectKW = true
	gotEnableThinking = nil
	mu.Unlock()

	for i := 0; i < 2; i++ {
		r, err := bFallback.ChatStream(ctx, []chat.Message{{Role: chat.RoleUser, Content: "summarize"}}, nil, io.Discard)
		if err != nil {
			t.Fatalf("ChatStream fallback %d: %v", i, err)
		}
		if r.Content != "summary" {
			t.Fatalf("fallback content = %q, want %q", r.Content, "summary")
		}
	}

	want := []any{false, nil, nil}
	if fmt.Sprint(gotEnableThinking) != fmt.Sprint(want) {
		t.Fatalf("chat_template_kwargs.enable_thinking history = %v, want %v", gotEnableThinking, want)
	}
}

func TestCompactManually_DisablesThinkingOnCloudBackend(t *testing.T) {
	var seenDisableThinking bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if kw, ok := body["chat_template_kwargs"].(map[string]any); ok && kw["enable_thinking"] == false {
			seenDisableThinking = true
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"compacted\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	b, err := NewOpenAICompatibleBackend(server.URL, "test-reasoning-model", "")
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	a := New(Config{Backend: b, MaxTurns: 4, MaxTokens: 100000, Output: io.Discard})
	for i := 0; i < protectLastTurns+4; i++ {
		a.conv.Append(chat.Message{Role: chat.RoleUser, Content: fmt.Sprintf("msg %d", i)})
	}

	if _, _, n, err := a.CompactManually(context.Background()); err != nil || n == 0 {
		t.Fatalf("CompactManually: n=%d err=%v", n, err)
	}
	if !seenDisableThinking {
		t.Fatal("expected CompactManually summarization request to send chat_template_kwargs.enable_thinking=false")
	}
}

func TestBuildBackend_AppliesStreamIdleTimeoutAndThinkingOptions(t *testing.T) {
	f := false
	backend, err := BuildBackend(ModelTarget{
		Model:    "test-reasoning-model",
		Protocol: ProtocolOpenAI,
		Endpoint: "http://127.0.0.1:8080",
	}, CloudBackendOptions{
		StreamIdleTimeout: 5 * time.Second,
		Thinking:          &f,
	})
	if err != nil {
		t.Fatalf("BuildBackend: %v", err)
	}
	cb, ok := backend.(*CloudBackend)
	if !ok {
		t.Fatalf("backend type = %T, want *CloudBackend", backend)
	}
	if cb.streamIdleTimeout != 5*time.Second {
		t.Errorf("streamIdleTimeout = %s, want 5s", cb.streamIdleTimeout)
	}
	if cb.thinking == nil || *cb.thinking != false {
		t.Errorf("thinking = %v, want pointer to false", cb.thinking)
	}

	bNoIdle, err := BuildBackend(ModelTarget{
		Model:    "test-reasoning-model",
		Protocol: ProtocolOpenAI,
		Endpoint: "http://127.0.0.1:8080",
	}, CloudBackendOptions{
		StreamIdleTimeout: -1,
	})
	if err != nil {
		t.Fatalf("BuildBackend disable idle: %v", err)
	}
	if bNoIdle.(*CloudBackend).streamIdleTimeout != 0 {
		t.Errorf("negative StreamIdleTimeout should disable idle guard (0), got %s", bNoIdle.(*CloudBackend).streamIdleTimeout)
	}

	bDefault, err := BuildBackend(ModelTarget{
		Model:    "test-reasoning-model",
		Protocol: ProtocolOpenAI,
		Endpoint: "http://127.0.0.1:8080",
	}, CloudBackendOptions{})
	if err != nil {
		t.Fatalf("BuildBackend zero idle: %v", err)
	}
	if bDefault.(*CloudBackend).streamIdleTimeout != defaultStreamIdleTimeout {
		t.Errorf("zero StreamIdleTimeout = %s, want %s", bDefault.(*CloudBackend).streamIdleTimeout, defaultStreamIdleTimeout)
	}

	tVal := true
	bCloud, err := BuildBackend(ModelTarget{
		Model:        "cloud-model",
		Protocol:     ProtocolCloud,
		ProviderName: "openrouter",
		Endpoint:     "https://example.invalid/api/v1",
	}, CloudBackendOptions{
		ProviderKind:      "openai",
		APIKey:            "k",
		StreamIdleTimeout: 9 * time.Second,
		Thinking:          &tVal,
	})
	if err != nil {
		t.Fatalf("BuildBackend ProtocolCloud: %v", err)
	}
	cbCloud := bCloud.(*CloudBackend)
	if cbCloud.streamIdleTimeout != 9*time.Second {
		t.Errorf("ProtocolCloud streamIdleTimeout = %s, want 9s", cbCloud.streamIdleTimeout)
	}
	if cbCloud.thinking == nil || !*cbCloud.thinking {
		t.Errorf("ProtocolCloud thinking = %v, want *true", cbCloud.thinking)
	}
}

func TestCloudBackend_DisabledIdleTimeoutAllowsLongPause(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi \"}}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(50 * time.Millisecond)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"there\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	b, err := BuildBackend(ModelTarget{
		Model:    "slow",
		Protocol: ProtocolOpenAI,
		Endpoint: server.URL,
	}, CloudBackendOptions{StreamIdleTimeout: -1})
	if err != nil {
		t.Fatalf("BuildBackend: %v", err)
	}
	resp, err := b.ChatStream(context.Background(), []chat.Message{{Role: chat.RoleUser, Content: "hi"}}, nil, io.Discard)
	if err != nil {
		t.Fatalf("ChatStream with disabled idle timeout: %v", err)
	}
	if resp.Content != "hi there" {
		t.Fatalf("content = %q, want %q", resp.Content, "hi there")
	}
}

func TestCloudBackend_PreHeaderIdleTimeout(t *testing.T) {
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-done:
		}
	}))
	defer func() {
		close(done)
		server.Close()
	}()

	b, err := NewOpenAICompatibleBackend(server.URL, "slow-prefill", "")
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	b.streamIdleTimeout = 25 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = b.ChatStream(ctx, []chat.Message{{Role: chat.RoleUser, Content: "hi"}}, nil, io.Discard)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "stream idle timeout") {
		t.Fatalf("pre-header idle error = %v, want stream idle timeout wrapping DeadlineExceeded", err)
	}
}

func TestCloudBackend_ParentDeadlineDoesNotReportIdleTimeout(t *testing.T) {
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-done:
		}
	}))
	defer func() {
		close(done)
		server.Close()
	}()

	b, err := NewOpenAICompatibleBackend(server.URL, "slow-local", "")
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	b.streamIdleTimeout = 500 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = b.ChatStream(ctx, []chat.Message{{Role: chat.RoleUser, Content: "hi"}}, nil, io.Discard)
	if err == nil || strings.Contains(err.Error(), "stream idle timeout") || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("parent deadline error = %v, want plain DeadlineExceeded without idle message", err)
	}
}

func TestCloudBackend_ThinkingOptionAndContextOverride(t *testing.T) {
	var mu sync.Mutex
	var got []any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if kw, ok := body["chat_template_kwargs"].(map[string]any); ok {
			got = append(got, kw["enable_thinking"])
		} else {
			got = append(got, nil)
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	tVal := true
	b, err := BuildBackend(ModelTarget{
		Model:    "qwen",
		Protocol: ProtocolOpenAI,
		Endpoint: server.URL,
	}, CloudBackendOptions{Thinking: &tVal})
	if err != nil {
		t.Fatalf("BuildBackend: %v", err)
	}
	if _, err := b.ChatStream(context.Background(), []chat.Message{{Role: chat.RoleUser, Content: "reason"}}, nil, io.Discard); err != nil {
		t.Fatalf("ChatStream(default true): %v", err)
	}
	if _, err := b.ChatStream(WithDisableThinking(context.Background()), []chat.Message{{Role: chat.RoleUser, Content: "compact"}}, nil, io.Discard); err != nil {
		t.Fatalf("ChatStream(WithDisableThinking): %v", err)
	}
	want := []any{true, false}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("enable_thinking history = %v, want %v", got, want)
	}
}

func TestCloudBackend_EnableThinking4xxFallback(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, has := body["chat_template_kwargs"]; has {
			http.Error(w, `{"error":{"message":"Unknown field: enable_thinking"}}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	b, err := NewOpenAICompatibleBackend(server.URL, "strict-proxy", "")
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	resp, err := b.ChatStream(WithDisableThinking(context.Background()), []chat.Message{{Role: chat.RoleUser, Content: "hi"}}, nil, io.Discard)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if resp.Content != "ok" || calls != 2 {
		t.Fatalf("content=%q calls=%d, want %q and 2", resp.Content, calls, "ok")
	}
}

func TestCloudBackend_ThinkingThenToolCallsClosesThought(t *testing.T) {
	// OpenAI: reasoning_content followed directly by delta.tool_calls (empty content)
	openAISrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"plan call\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"axis_status\",\"arguments\":\"{}\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer openAISrv.Close()

	bOAI, err := NewOpenAICompatibleBackend(openAISrv.URL, "m", "")
	if err != nil {
		t.Fatalf("NewOpenAICompatibleBackend: %v", err)
	}
	sinkOAI := &recordingThoughtSink{}
	respOAI, err := bOAI.ChatStream(context.Background(), []chat.Message{{Role: chat.RoleUser, Content: "go"}}, nil, sinkOAI)
	if err != nil {
		t.Fatalf("OpenAI ChatStream: %v", err)
	}
	if len(respOAI.ToolCalls) != 1 || sinkOAI.closed != 1 || strings.Join(sinkOAI.thoughts, "") != "plan call" {
		t.Fatalf("OpenAI tool-boundary thought close mismatch: tools=%d closed=%d thoughts=%v", len(respOAI.ToolCalls), sinkOAI.closed, sinkOAI.thoughts)
	}

	// Anthropic: thinking_delta followed directly by tool_use block (empty text_delta)
	anthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"plan call\"}}\n\n")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"axis_status\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n")
	}))
	defer anthSrv.Close()

	bAnth, err := NewCloudBackendWithKey("anthropic", "anthropic", anthSrv.URL, "k", "test-model", 0)
	if err != nil {
		t.Fatalf("NewCloudBackendWithKey: %v", err)
	}
	sinkAnth := &recordingThoughtSink{}
	respAnth, err := bAnth.ChatStream(context.Background(), []chat.Message{{Role: chat.RoleUser, Content: "go"}}, nil, sinkAnth)
	if err != nil {
		t.Fatalf("Anthropic ChatStream: %v", err)
	}
	if len(respAnth.ToolCalls) != 1 || sinkAnth.closed != 1 || strings.Join(sinkAnth.thoughts, "") != "plan call" {
		t.Fatalf("Anthropic tool-boundary thought close mismatch: tools=%d closed=%d thoughts=%v", len(respAnth.ToolCalls), sinkAnth.closed, sinkAnth.thoughts)
	}
}

func TestCloudBackend_HeaderResetExtendsFirstChunkBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(28 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(28 * time.Millisecond)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	b, err := NewOpenAICompatibleBackend(server.URL, "header-slow", "")
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	// Header arrives at 28ms (<40ms) and resets idle timer; first chunk arrives 28ms
	// later (total 56ms > 40ms idle window). Succeeds only if header arrival resets.
	b.streamIdleTimeout = 40 * time.Millisecond

	resp, err := b.ChatStream(context.Background(), []chat.Message{{Role: chat.RoleUser, Content: "hi"}}, nil, io.Discard)
	if err != nil {
		t.Fatalf("ChatStream with header reset failed: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("content = %q, want %q", resp.Content, "ok")
	}
}

func TestCloudBackend_Anthropic_IgnoresThinkingOverrides(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n")
	}))
	defer server.Close()

	tVal := true
	b, err := BuildBackend(ModelTarget{
		Model:        "test-model",
		Protocol:     ProtocolCloud,
		ProviderName: "anthropic",
		Endpoint:     server.URL,
	}, CloudBackendOptions{APIKey: "k", Thinking: &tVal})
	if err != nil {
		t.Fatalf("BuildBackend: %v", err)
	}
	if _, err := b.ChatStream(WithDisableThinking(context.Background()), []chat.Message{{Role: chat.RoleUser, Content: "hi"}}, nil, io.Discard); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if _, has := body["chat_template_kwargs"]; has {
		t.Fatalf("Anthropic request unexpectedly contained chat_template_kwargs: %v", body)
	}
}

func TestIdleGuardTouchDoesNotExpireRightAfterReset(t *testing.T) {
	// Part 1: Deterministic check that an already-dispatched timer callback
	// arriving right after touch() sees the extended deadline and does not cancel.
	for i := 0; i < 20; i++ {
		g := newStreamIdleGuard(context.Background(), 50*time.Millisecond)
		g.touch()
		g.onTimer()
		if g.ctx.Err() != nil {
			g.Close()
			t.Fatalf("iter %d: stale onTimer canceled active guard right after touch(): %v", i, g.ctx.Err())
		}
		g.Close()
	}

	// Part 2: Concurrent -race stress test where touch() keeps a 30ms guard alive past 90ms.
	const (
		idle  = 30 * time.Millisecond
		total = 90 * time.Millisecond
	)
	g := newStreamIdleGuard(context.Background(), idle)
	stopTouch := make(chan struct{})
	doneTouch := make(chan struct{})
	go func() {
		defer close(doneTouch)
		ticker := time.NewTicker(3 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopTouch:
				return
			case <-ticker.C:
				g.touch()
			}
		}
	}()
	timer := time.NewTimer(total)
	select {
	case <-g.ctx.Done():
		close(stopTouch)
		<-doneTouch
		timer.Stop()
		g.Close()
		t.Fatalf("guard expired while touch() was actively keeping it alive")
	case <-timer.C:
	}
	close(stopTouch)
	<-doneTouch
	g.Close()

	// Part 3: Post-expiry touch() must not re-arm timer or clear expired state.
	gExp := newStreamIdleGuard(context.Background(), 5*time.Millisecond)
	select {
	case <-gExp.ctx.Done():
	case <-time.After(time.Second):
		gExp.Close()
		t.Fatalf("timed out waiting for guard expiry")
	}
	gExp.mu.Lock()
	expiredDeadline := gExp.deadline
	gExp.mu.Unlock()
	gExp.touch()
	gExp.mu.Lock()
	afterTouchDeadline := gExp.deadline
	gExp.mu.Unlock()
	if !afterTouchDeadline.Equal(expiredDeadline) {
		gExp.Close()
		t.Fatalf("post-expiry touch() modified deadline from %v to %v", expiredDeadline, afterTouchDeadline)
	}
	if !errors.Is(gExp.wrapErr("read", context.Background(), gExp.ctx.Err()), context.DeadlineExceeded) {
		gExp.Close()
		t.Fatalf("expected DeadlineExceeded from expired wrapErr after post-expiry touch()")
	}
	gExp.Close()
}

func TestCloudBackend_ParentCancelDoesNotReportIdleTimeout(t *testing.T) {
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-done:
		}
	}))
	defer func() {
		close(done)
		server.Close()
	}()

	b, err := NewOpenAICompatibleBackend(server.URL, "slow-local", "")
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	b.streamIdleTimeout = 500 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(15*time.Millisecond, cancel)

	_, err = b.ChatStream(ctx, []chat.Message{{Role: chat.RoleUser, Content: "hi"}}, nil, io.Discard)
	if err == nil {
		t.Fatal("expected cancel error, got nil")
	}
	if strings.Contains(err.Error(), "stream idle timeout") {
		t.Fatalf("parent cancel misreported as idle timeout: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestCloudBackend_RejectsBothStreamOptionsAndChatTemplateKwargs(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, has := body["stream_options"]; has {
			http.Error(w, `{"error":{"message":"Unrecognized request argument supplied: stream_options"}}`, http.StatusBadRequest)
			return
		}
		if _, has := body["chat_template_kwargs"]; has {
			http.Error(w, `{"error":{"message":"Unrecognized request argument supplied: chat_template_kwargs"}}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	b, err := NewOpenAICompatibleBackend(server.URL, "strict", "")
	if err != nil {
		t.Fatalf("backend: %v", err)
	}

	resp, err := b.ChatStream(WithDisableThinking(context.Background()), []chat.Message{{Role: chat.RoleUser, Content: "hi"}}, nil, io.Discard)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if resp.Content != "ok" || calls != 3 {
		t.Fatalf("content=%q calls=%d, want %q and 3", resp.Content, calls, "ok")
	}
}

func TestCloudBackend_Anthropic_ThinkingDeltaStreamsToThoughtWriterOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"plan\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n")
	}))
	defer server.Close()

	b, err := NewCloudBackendWithKey("anthropic", "anthropic", server.URL, "k", "test-model", 0)
	if err != nil {
		t.Fatalf("backend: %v", err)
	}

	sink := &recordingThoughtSink{}
	cw := NewColorWriter(sink)
	resp, err := b.ChatStream(context.Background(), []chat.Message{{Role: chat.RoleUser, Content: "hi"}}, nil, cw)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	_ = cw.Close()
	if resp.Content != "done" || sink.buf.String() != "done" {
		t.Fatalf("resp=%q visible=%q, want %q", resp.Content, sink.buf.String(), "done")
	}
	if got := strings.Join(sink.thoughts, ""); got != "plan" {
		t.Fatalf("thoughts = %q, want %q", got, "plan")
	}
	if sink.closed == 0 {
		t.Fatal("expected CloseThought to be called")
	}
}

func TestCloudBackend_OpenAI_UnrecognizedNonField4xxDoesNotRetry(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, `{"error":{"message":"invalid api key or model quota exceeded"}}`, http.StatusBadRequest)
	}))
	defer server.Close()

	b, err := NewOpenAICompatibleBackend(server.URL, "strict", "")
	if err != nil {
		t.Fatalf("backend: %v", err)
	}

	_, err = b.ChatStream(WithDisableThinking(context.Background()), []chat.Message{{Role: chat.RoleUser, Content: "hi"}}, nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "http status 400") {
		t.Fatalf("err = %v, want http status 400", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (unrecognized 400 must not retry)", calls)
	}
	if b.noStreamUsage.Load() || b.noChatTemplateKwargs.Load() {
		t.Fatalf("sticky flags mutated unexpectedly: noStreamUsage=%v noChatTemplateKwargs=%v",
			b.noStreamUsage.Load(), b.noChatTemplateKwargs.Load())
	}
}

func TestCloudBackend_OpenAI_StickyFallbacksPersistAcrossCalls(t *testing.T) {
	var callBodies []map[string]any
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		callBodies = append(callBodies, body)
		mu.Unlock()

		if _, has := body["stream_options"]; has {
			http.Error(w, `{"error":{"message":"unknown parameter stream_options"}}`, http.StatusBadRequest)
			return
		}
		if _, has := body["chat_template_kwargs"]; has {
			http.Error(w, `{"error":{"message":"unsupported enable_thinking"}}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	b, err := NewOpenAICompatibleBackend(server.URL, "strict", "")
	if err != nil {
		t.Fatalf("backend: %v", err)
	}

	ctx := WithDisableThinking(context.Background())
	if _, err := b.ChatStream(ctx, []chat.Message{{Role: chat.RoleUser, Content: "first"}}, nil, io.Discard); err != nil {
		t.Fatalf("first ChatStream: %v", err)
	}
	if _, err := b.ChatStream(ctx, []chat.Message{{Role: chat.RoleUser, Content: "second"}}, nil, io.Discard); err != nil {
		t.Fatalf("second ChatStream: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(callBodies) != 4 {
		t.Fatalf("total HTTP calls = %d, want 4 (3 on first call, 1 on second)", len(callBodies))
	}
	secondCallBody := callBodies[3]
	if _, has := secondCallBody["stream_options"]; has {
		t.Fatalf("second ChatStream unexpectedly re-sent stream_options: %v", secondCallBody)
	}
	if _, has := secondCallBody["chat_template_kwargs"]; has {
		t.Fatalf("second ChatStream unexpectedly re-sent chat_template_kwargs: %v", secondCallBody)
	}
}

func TestCloudBackend_OpenAI_EmptyReasoningContentFallsBackToReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// ReasoningContent takes precedence when non-empty; when empty string "", fall back to Reasoning.
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"primary \",\"reasoning\":\"ignored\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"\",\"reasoning\":\"secondary\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ans\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	b, err := NewOpenAICompatibleBackend(server.URL, "test-reasoning-model", "")
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	sink := &recordingThoughtSink{}
	resp, err := b.ChatStream(context.Background(), []chat.Message{{Role: chat.RoleUser, Content: "hi"}}, nil, sink)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if resp.Content != "ans" || sink.buf.String() != "ans" {
		t.Fatalf("resp=%q visible=%q, want ans", resp.Content, sink.buf.String())
	}
	if got := strings.Join(sink.thoughts, ""); got != "primary secondary" {
		t.Fatalf("thoughts = %q, want %q", got, "primary secondary")
	}
}

func TestIdleGuardEdgeCases_DisabledNilParentAndIdempotentClose(t *testing.T) {
	// Zero and negative timeouts leave timer nil and never trip an idle deadline.
	var nilCtx context.Context
	if !thinkingDisabledFromContext(WithDisableThinking(nilCtx)) {
		t.Fatal("WithDisableThinking(nil) should return a disabled context")
	}
	for _, d := range []time.Duration{0, -10 * time.Millisecond} {
		g := newStreamIdleGuard(nilCtx, d)
		if g.timer != nil {
			g.Close()
			t.Fatalf("timeout=%s unexpectedly armed timer", d)
		}
		g.touch()
		time.Sleep(10 * time.Millisecond)
		if g.ctx.Err() != nil {
			g.Close()
			t.Fatalf("disabled guard timeout=%s canceled: %v", d, g.ctx.Err())
		}
		g.Close()
		g.Close() // idempotent
	}

	// Nil guard helpers must not panic.
	var nilGuard *streamIdleGuard
	nilGuard.touch()
	nilGuard.Close()
	if err := nilGuard.wrapErr("prefix", context.Background(), errors.New("boom")); err == nil || !strings.Contains(err.Error(), "prefix: boom") {
		t.Fatalf("nilGuard wrapErr = %v, want prefix: boom", err)
	}

	// Even if expired is set, if parent context was canceled, wrapErr must preserve caller cancel rather than misreporting idle timeout.
	parent, cancelParent := context.WithCancel(context.Background())
	g := newStreamIdleGuard(parent, 5*time.Millisecond)
	select {
	case <-g.ctx.Done():
	case <-time.After(time.Second):
		g.Close()
		cancelParent()
		t.Fatal("timed out waiting for idle expiry")
	}
	cancelParent()
	wrapped := g.wrapErr("stream read", parent, context.Canceled)
	g.Close()
	if strings.Contains(wrapped.Error(), "stream idle timeout") || !errors.Is(wrapped, context.Canceled) {
		t.Fatalf("wrapErr with canceled parent = %v, want context.Canceled without idle timeout", wrapped)
	}
}

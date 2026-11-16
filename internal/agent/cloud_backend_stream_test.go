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

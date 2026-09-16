package agent

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSymbolSearchNoMatch(t *testing.T) {
	chdirToTempDir(t)
	writeFile(t, "sample.go", "package main\n\nfunc Foo() {}\n")
	r := newTestToolRegistry(t)
	out, _ := execTool(t, r, "symbol_search", mustJSON(t, map[string]any{"query": "Nonexistent", "path": "."}))
	if !strings.Contains(out, "No symbols matching") {
		t.Fatalf("expected no-match message, got: %s", out)
	}
}

func TestWebFetchStripsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><title>Ignored</title><style>body{}</style></head><body><h1>Title</h1><p>Hello world<script>alert(1)</script></p></body></html>`))
	}))
	defer srv.Close()
	r := newTestToolRegistry(t)
	out, err := execTool(t, r, "web_fetch", mustJSON(t, map[string]any{"url": srv.URL}))
	if err != nil {
		t.Fatalf("web_fetch: %v", err)
	}
	if !strings.Contains(out, "Title") || !strings.Contains(out, "Hello world") {
		t.Fatalf("expected readable text, got: %s", out)
	}
	if strings.Contains(out, "<script>") || strings.Contains(out, "alert(1)") || strings.Contains(out, "Ignored") {
		t.Fatalf("html/script/title not stripped, got: %s", out)
	}
}

func TestWebFetchRejectsNonHTTP(t *testing.T) {
	r := newTestToolRegistry(t)
	_, err := execTool(t, r, "web_fetch", mustJSON(t, map[string]any{"url": "file:///etc/passwd"}))
	if err == nil || !strings.Contains(err.Error(), "http or https") {
		t.Fatalf("expected scheme rejection, got %v", err)
	}
}

func TestWebFetchJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"key":"value","n":42}`))
	}))
	defer srv.Close()
	r := newTestToolRegistry(t)
	out, err := execTool(t, r, "web_fetch", mustJSON(t, map[string]any{"url": srv.URL}))
	if err != nil {
		t.Fatalf("web_fetch json: %v", err)
	}
	if !strings.Contains(out, "value") || !strings.Contains(out, "42") {
		t.Fatalf("expected json body, got: %s", out)
	}
}

func TestUndoLastEmpty(t *testing.T) {
	r := newTestToolRegistry(t)
	out, err := execTool(t, r, "undo_last", mustJSON(t, map[string]any{}))
	if err != nil || !strings.Contains(out, "Nothing to undo") {
		t.Fatalf("expected empty message, got: %q err %v", out, err)
	}
}

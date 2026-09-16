package llmrouter_test

import (
	"testing"

	"github.com/toasterbook88/axis/internal/llmrouter"
)

func TestResolveEndpointSecurity(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		endpoint string
		wantErr  bool
		wantURL  string
	}{
		{"https allowed", "openrouter", "https://api.openrouter.ai/v1", false, "https://api.openrouter.ai/v1"},
		{"http localhost allowed", "openrouter", "http://localhost:11434", false, "http://localhost:11434"},
		{"http 127.0.0.1 allowed", "openrouter", "http://127.0.0.1:8080", false, "http://127.0.0.1:8080"},
		{"http [::1] allowed", "openrouter", "http://[::1]:8080", false, "http://[::1]:8080"},
		{"http external blocked", "openrouter", "http://api.evil.com", true, ""},
		{"http localhost.evil.com blocked", "openrouter", "http://localhost.evil.com", true, ""},
		{"empty openrouter default", "openrouter", "", false, "https://openrouter.ai/api/v1"},
		{"empty groq default", "groq", "", false, "https://api.groq.com/openai/v1"},
		{"empty anthropic default", "anthropic", "", false, "https://api.anthropic.com"},
		{"unsupported kind empty", "unknown", "", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := llmrouter.ResolveEndpoint(tc.kind, tc.endpoint)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for endpoint %q", tc.endpoint)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantURL {
				t.Fatalf("got %q want %q", got, tc.wantURL)
			}
		})
	}
}

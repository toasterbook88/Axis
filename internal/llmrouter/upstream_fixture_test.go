package llmrouter_test

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
)

func TestDeadUpstreamStaysUnavailableAcrossAddressReuse(t *testing.T) {
	dead := deadUpstream(t)
	u, err := url.Parse(dead)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce another test server claiming an address after the fixture frees it.
	if listener, err := net.Listen("tcp", u.Host); err == nil {
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
		})}
		go server.Serve(listener)
		t.Cleanup(func() { _ = server.Close() })
	}
	hub := fakeHubInfo(t, deployment(dead), deployment(""))
	cfg := hubConfig([]string{"hub"}, config.AIBackendConfig{Name: "hub", Kind: config.AIBackendOpenAICompatible, BaseURL: hub.URL + "/v1"})
	decision, err := resolveFast(t, cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(decision.Reasoning, " "), "upstream not checked") {
		t.Fatalf("released dead-upstream address became a healthy upstream: %v", decision.Reasoning)
	}
}

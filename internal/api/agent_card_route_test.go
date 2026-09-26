package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/a2a"
)

// Verifies the well-known card route mounts on the real ServeWithContext mux
// and — unlike /snapshot — stays unauthenticated when an API token is set.
func TestServeWithContextMountsAgentCardRoute(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "axis.sock")
	token := "test-token-464"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = ServeWithContext(ctx, socketPath, nil, token, false, func() a2a.AgentCard {
			return a2a.Card(a2a.CardOptions{Name: "card-probe", Version: "test", Scope: a2a.ScopeObserve})
		})
	}()

	// Wait for the socket to accept.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", socketPath, time.Second)
		if err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.DialTimeout("unix", socketPath, 2*time.Second)
			},
		},
	}
	resp, err := httpClient.Get("http://axis/.well-known/agent-card.json")
	if err != nil {
		t.Fatalf("GET card: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("card status = %d, want 200", resp.StatusCode)
	}
	var card a2a.AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatalf("decode card: %v", err)
	}
	if card.Name != "card-probe" {
		t.Fatalf("card name = %q", card.Name)
	}

	// Contrast: /snapshot (same mux) must require the token.
	noAuth, err := httpClient.Get("http://axis/snapshot")
	if err != nil {
		t.Fatalf("GET snapshot: %v", err)
	}
	noAuth.Body.Close()
	if noAuth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("snapshot without token = %d, want 401", noAuth.StatusCode)
	}
}

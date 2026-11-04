package api

import (
	"context"
	"strings"
	"testing"
	"time"
)

// F9: withAuth is a no-op for an empty token, so a TCP listener without one
// would expose /run and every other route to the network unauthenticated.
// ServeWithContext must refuse before it listens.
func TestServeWithContextRefusesTCPWithoutToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := ServeWithContext(ctx, "127.0.0.1:0", nil, "", false, nil)
	if err == nil || !strings.Contains(err.Error(), "api token") {
		t.Fatalf("expected refusal for unauthenticated TCP listener, got %v", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("refusal must be immediate, not after the context ends")
	}
}

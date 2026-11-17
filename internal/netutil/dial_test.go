package netutil

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestDialOutboundContextAllowlist(t *testing.T) {
	ResetInternalAllowlist()
	t.Cleanup(ResetInternalAllowlist)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if conn, err := DialOutboundContext(ctx, "tcp", listener.Addr().String(), nil); err == nil {
		conn.Close()
		t.Fatal("unallowlisted loopback was dialed")
	} else if !strings.Contains(err.Error(), "disallowed internal address") {
		t.Fatalf("expected address-policy rejection: %v", err)
	}
	conn, err := DialOutboundContext(ctx, "tcp", listener.Addr().String(), []string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("explicit allowlist: %v", err)
	}
	conn.Close()
	AllowInternalHost("127.0.0.1")
	conn, err = DialOutboundContext(ctx, "tcp", listener.Addr().String(), nil)
	if err != nil {
		t.Fatalf("registered allowlist: %v", err)
	}
	conn.Close()
}

func TestDialOutboundContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := DialOutboundContext(ctx, "tcp", "192.0.2.10:80", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected parent cancellation, got %v", err)
	}
}

package netutil

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestOutboundDialerAllowlist(t *testing.T) {
	ResetInternalAllowlist()
	t.Cleanup(ResetInternalAllowlist)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	dial := NewOutboundDialer(nil)
	if conn, err := dial(ctx, "tcp", listener.Addr().String()); err == nil {
		conn.Close()
		t.Fatal("unallowlisted loopback was dialed")
	} else if !strings.Contains(err.Error(), "disallowed internal address") {
		t.Fatalf("expected address-policy rejection: %v", err)
	}
	allowedDial := NewOutboundDialer([]string{"127.0.0.1"})
	conn, err := allowedDial(ctx, "tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("explicit allowlist: %v", err)
	}
	conn.Close()
	AllowInternalHost("127.0.0.1")
	conn, err = dial(ctx, "tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("registered allowlist: %v", err)
	}
	conn.Close()
}

func TestOutboundDialerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewOutboundDialer(nil)(ctx, "tcp", "192.0.2.10:80")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected parent cancellation, got %v", err)
	}
}

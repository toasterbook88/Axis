package auth

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func TestForwardedExecutionOriginHeadersRoundTrip(t *testing.T) {
	req := httptest.NewRequest("POST", "/run", nil)
	now := time.Date(2026, time.April, 5, 12, 0, 0, 0, time.UTC)
	want := models.NewExecutionOrigin("upstream-node", "upstream.local", "abc-123")

	if err := SetForwardedExecutionOriginHeaders(req.Header, want, "tok", now); err != nil {
		t.Fatalf("SetForwardedExecutionOriginHeaders: %v", err)
	}

	got, ok, err := ForwardedExecutionOriginFromRequest(req, "tok", now)
	if err != nil {
		t.Fatalf("ForwardedExecutionOriginFromRequest: %v", err)
	}
	if !ok {
		t.Fatal("expected forwarded origin to be detected")
	}
	if got != want {
		t.Fatalf("forwarded origin = %+v, want %+v", got, want)
	}
}

func TestForwardedExecutionOriginRejectsInvalidSignature(t *testing.T) {
	req := httptest.NewRequest("POST", "/run", nil)
	now := time.Date(2026, time.April, 5, 12, 0, 0, 0, time.UTC)

	if err := SetForwardedExecutionOriginHeaders(req.Header, models.NewExecutionOrigin("upstream-node", "upstream.local", "abc-123"), "tok", now); err != nil {
		t.Fatalf("SetForwardedExecutionOriginHeaders: %v", err)
	}
	req.Header.Set(ForwardedOriginSignatureHeader, "deadbeef")

	_, ok, err := ForwardedExecutionOriginFromRequest(req, "tok", now)
	if !ok {
		t.Fatal("expected forwarded origin attempt to be detected")
	}
	if err == nil {
		t.Fatal("expected invalid signature error")
	}
}

func TestForwardedExecutionOriginRejectsExpiredTimestamp(t *testing.T) {
	req := httptest.NewRequest("POST", "/run", nil)
	now := time.Date(2026, time.April, 5, 12, 0, 0, 0, time.UTC)

	if err := SetForwardedExecutionOriginHeaders(req.Header, models.NewExecutionOrigin("upstream-node", "upstream.local", "abc-123"), "tok", now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("SetForwardedExecutionOriginHeaders: %v", err)
	}

	_, ok, err := ForwardedExecutionOriginFromRequest(req, "tok", now)
	if !ok {
		t.Fatal("expected forwarded origin attempt to be detected")
	}
	if err == nil {
		t.Fatal("expected expired timestamp error")
	}
}

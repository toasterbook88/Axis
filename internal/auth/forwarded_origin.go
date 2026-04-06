package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

const (
	ForwardedOriginNodeHeader      = "X-Axis-Forwarded-Node"
	ForwardedOriginHostnameHeader  = "X-Axis-Forwarded-Hostname"
	ForwardedOriginStableIDHeader  = "X-Axis-Forwarded-Stable-ID"
	ForwardedOriginTimeHeader      = "X-Axis-Forwarded-Time"
	ForwardedOriginSignatureHeader = "X-Axis-Forwarded-Signature"

	forwardedOriginVersion = "v1"
	forwardedOriginMaxSkew = 5 * time.Minute
)

// SetForwardedExecutionOriginHeaders stamps signed forwarded-origin headers for
// a trusted upstream AXIS caller.
func SetForwardedExecutionOriginHeaders(header http.Header, origin models.ExecutionOrigin, token string, now time.Time) error {
	if header == nil {
		return fmt.Errorf("forwarded execution origin headers require a header target")
	}

	origin = origin.Normalized()
	if origin.IsZero() {
		return fmt.Errorf("forwarded execution origin requires at least one identity field")
	}

	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("forwarded execution origin requires a signing token")
	}

	stampedAt := normalizeForwardedOriginTime(now)
	header.Set(ForwardedOriginNodeHeader, origin.Node)
	header.Set(ForwardedOriginHostnameHeader, origin.Hostname)
	header.Set(ForwardedOriginStableIDHeader, origin.StableID)
	header.Set(ForwardedOriginTimeHeader, stampedAt)
	header.Set(ForwardedOriginSignatureHeader, signForwardedOrigin(origin, token, stampedAt))
	return nil
}

// ForwardedExecutionOriginFromRequest validates signed forwarded-origin
// headers, returning ok=false when the request did not attempt forwarding.
func ForwardedExecutionOriginFromRequest(r *http.Request, token string, now time.Time) (models.ExecutionOrigin, bool, error) {
	if r == nil {
		return models.ExecutionOrigin{}, false, nil
	}

	node := strings.TrimSpace(r.Header.Get(ForwardedOriginNodeHeader))
	hostname := strings.TrimSpace(r.Header.Get(ForwardedOriginHostnameHeader))
	stableID := strings.TrimSpace(r.Header.Get(ForwardedOriginStableIDHeader))
	stampedAt := strings.TrimSpace(r.Header.Get(ForwardedOriginTimeHeader))
	signature := strings.ToLower(strings.TrimSpace(r.Header.Get(ForwardedOriginSignatureHeader)))

	if node == "" && hostname == "" && stableID == "" && stampedAt == "" && signature == "" {
		return models.ExecutionOrigin{}, false, nil
	}

	token = strings.TrimSpace(token)
	if token == "" {
		return models.ExecutionOrigin{}, true, fmt.Errorf("forwarded execution origin requires a configured signing token")
	}
	if stampedAt == "" {
		return models.ExecutionOrigin{}, true, fmt.Errorf("forwarded execution origin requires %s", ForwardedOriginTimeHeader)
	}
	if signature == "" {
		return models.ExecutionOrigin{}, true, fmt.Errorf("forwarded execution origin requires %s", ForwardedOriginSignatureHeader)
	}

	parsedTime, err := time.Parse(time.RFC3339Nano, stampedAt)
	if err != nil {
		return models.ExecutionOrigin{}, true, fmt.Errorf("invalid forwarded execution origin time: %w", err)
	}
	stampedAt = normalizeForwardedOriginTime(parsedTime)

	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	if delta := now.Sub(parsedTime.UTC()); delta > forwardedOriginMaxSkew || delta < -forwardedOriginMaxSkew {
		return models.ExecutionOrigin{}, true, fmt.Errorf("forwarded execution origin timestamp outside allowed skew")
	}

	origin := models.NewExecutionOrigin(node, hostname, stableID)
	if origin.IsZero() {
		return models.ExecutionOrigin{}, true, fmt.Errorf("forwarded execution origin requires at least one identity field")
	}

	expected := signForwardedOrigin(origin, token, stampedAt)
	if !hmac.Equal([]byte(signature), []byte(expected)) {
		return models.ExecutionOrigin{}, true, fmt.Errorf("invalid forwarded execution origin signature")
	}

	return origin, true, nil
}

func signForwardedOrigin(origin models.ExecutionOrigin, token, stampedAt string) string {
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(forwardedOriginPayload(origin, stampedAt)))
	return hex.EncodeToString(mac.Sum(nil))
}

func forwardedOriginPayload(origin models.ExecutionOrigin, stampedAt string) string {
	origin = origin.Normalized()
	return strings.Join([]string{
		forwardedOriginVersion,
		origin.Node,
		origin.Hostname,
		origin.StableID,
		stampedAt,
	}, "\n")
}

func normalizeForwardedOriginTime(now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	return now.UTC().Format(time.RFC3339Nano)
}

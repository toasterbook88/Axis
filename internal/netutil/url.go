// Package netutil contains small helpers for outbound network operations.
package netutil

import (
	"fmt"
	"net/url"
)

// ValidateOutboundURL accepts only HTTP(S) URLs with a non-empty host.
func ValidateOutboundURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid outbound URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("outbound URL must use http or https scheme")
	}
	if parsed.Host == "" {
		return fmt.Errorf("outbound URL must include a host")
	}
	return nil
}

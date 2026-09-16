package llmrouter

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

const (
	openRouterDefaultBaseURL = "https://openrouter.ai/api/v1"
	groqDefaultBaseURL       = "https://api.groq.com/openai/v1"
	anthropicDefaultBaseURL  = "https://api.anthropic.com"
)

// ResolveEndpoint validates the endpoint and returns the default base URL for the given provider kind if empty.
func ResolveEndpoint(kind, endpoint string) (string, error) {
	trimmed := strings.TrimSpace(endpoint)
	if err := validateEndpointSecurity(trimmed, kind); err != nil {
		return "", err
	}
	if trimmed != "" {
		return strings.TrimRight(trimmed, "/"), nil
	}
	switch strings.ToLower(kind) {
	case "openrouter":
		return openRouterDefaultBaseURL, nil
	case "groq":
		return groqDefaultBaseURL, nil
	case "anthropic", "claude":
		return anthropicDefaultBaseURL, nil
	}
	return "", fmt.Errorf("unsupported provider kind: %q", kind)
}

// validateEndpointSecurity rejects non-HTTPS endpoints unless the hostname
// is a loopback address (localhost, 127.0.0.1, [::1]). Empty endpoints are
// allowed (callers fill in provider defaults).
func validateEndpointSecurity(endpoint, providerName string) error {
	trimmed := strings.TrimSpace(endpoint)
	if trimmed == "" {
		return nil
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("provider %s: invalid endpoint URL: %w", providerName, err)
	}

	if strings.EqualFold(parsed.Scheme, "https") || parsed.Scheme == "" {
		return nil
	}

	if !strings.EqualFold(parsed.Scheme, "http") {
		return fmt.Errorf("provider %s: unsupported endpoint scheme %q", providerName, parsed.Scheme)
	}

	host := parsed.Hostname()
	if isLoopback(host) {
		return nil
	}

	return fmt.Errorf("provider %s: insecure http endpoint not allowed (only loopback addresses permitted for http)", providerName)
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if idx := strings.IndexByte(host, '%'); idx >= 0 {
		host = host[:idx]
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

package cortex

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ParseServiceEndpoint validates an explicit Cortex MCP service URL and returns
// it in normalized form. A bare host or "/" path gets the default /mcp path.
// Only http and https are accepted. URLs with embedded credentials, a query or a
// fragment are rejected so a token never rides in a logged URL.
func ParseServiceEndpoint(raw string) (string, error) {
	u, err := parseHTTPURL(raw)
	if err != nil {
		return "", err
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = mcpPath
	}
	return u.String(), nil
}

// ParseQdrantEndpoint validates an explicit Qdrant REST base URL used only for
// the memory point count. A trailing slash is trimmed.
func ParseQdrantEndpoint(raw string) (string, error) {
	u, err := parseHTTPURL(raw)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

func parseHTTPURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("URL does not parse")
	}
	if u.User != nil {
		return nil, fmt.Errorf("URL must not embed credentials; use AXIS_CORTEX_SECRET or ~/.axis/cortex.token")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("URL must use http or https")
	}
	if u.Host == "" || u.Hostname() == "" {
		return nil, fmt.Errorf("URL has no host")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, fmt.Errorf("URL must not carry a query or fragment")
	}
	return u, nil
}

// NewClientForEndpoint creates a client for an explicit, already validated MCP
// service URL (see ParseServiceEndpoint). qdrantURL may be empty, in which case
// Status reports the memory count as unknown instead of probing a guessed host.
// TLS uses the system trust store; timeout bounds every HTTP call.
func NewClientForEndpoint(mcpURL, qdrantURL, token string, timeout time.Duration) *Client {
	return &Client{
		mcpURL:    mcpURL,
		qdrantURL: qdrantURL,
		token:     token,
		http:      &http.Client{Timeout: timeout},
	}
}

// MCPEndpoint returns the MCP URL this client sends requests to.
func (c *Client) MCPEndpoint() string { return c.mcpURL }

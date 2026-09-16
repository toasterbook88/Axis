package chat

import (
	"net/http"
	"testing"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func stubDefaultHTTPClient(t *testing.T, client *http.Client) func() {
	t.Helper()
	prev := http.DefaultClient
	http.DefaultClient = client
	return func() {
		http.DefaultClient = prev
	}
}

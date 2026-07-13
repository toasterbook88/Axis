package netutil

import "testing"

func TestValidateOutboundURL(t *testing.T) {
	for _, test := range []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		{name: "https", rawURL: "https://example.com/api"},
		{name: "http", rawURL: "http://localhost:8080"},
		{name: "empty", wantErr: true},
		{name: "file", rawURL: "file:///tmp/data", wantErr: true},
		{name: "javascript", rawURL: "javascript:alert(1)", wantErr: true},
		{name: "no host", rawURL: "https:///path", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateOutboundURL(test.rawURL); (err != nil) != test.wantErr {
				t.Fatalf("ValidateOutboundURL(%q) error = %v, wantErr=%v", test.rawURL, err, test.wantErr)
			}
		})
	}
}

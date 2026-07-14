package netutil

import "testing"

func TestValidateOutboundURL(t *testing.T) {
	for _, test := range []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		{name: "https public ip", rawURL: "https://8.8.8.8/api"},
		{name: "unresolvable host fails closed", rawURL: "https://host.invalid/api", wantErr: true},
		{name: "empty", wantErr: true},
		{name: "file", rawURL: "file:///tmp/data", wantErr: true},
		{name: "javascript", rawURL: "javascript:alert(1)", wantErr: true},
		{name: "no host", rawURL: "https:///path", wantErr: true},
		{name: "loopback ip", rawURL: "http://127.0.0.1:8080", wantErr: true},
		{name: "loopback name", rawURL: "http://localhost:8080", wantErr: true},
		{name: "private 10", rawURL: "http://10.0.0.5", wantErr: true},
		{name: "private 192", rawURL: "http://192.168.1.10:9000", wantErr: true},
		{name: "link-local metadata", rawURL: "http://169.254.169.254/latest/meta-data", wantErr: true},
		{name: "unspecified", rawURL: "http://0.0.0.0", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateOutboundURL(test.rawURL); (err != nil) != test.wantErr {
				t.Fatalf("ValidateOutboundURL(%q) error = %v, wantErr=%v", test.rawURL, err, test.wantErr)
			}
		})
	}
}

func TestValidateOutboundURLAllowlist(t *testing.T) {
	defer ResetInternalAllowlist()

	if err := ValidateOutboundURL("http://127.0.0.1:8080"); err == nil {
		t.Fatal("expected loopback to be blocked before allowlisting")
	}

	AllowInternalHost("127.0.0.1")
	if err := ValidateOutboundURL("http://127.0.0.1:8080"); err != nil {
		t.Fatalf("expected allowlisted loopback to pass, got %v", err)
	}
}

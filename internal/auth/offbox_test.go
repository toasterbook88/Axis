package auth

import (
	"strings"
	"testing"
)

func TestRefuseOffBoxBearer(t *testing.T) {
	t.Setenv(AllowOffBoxBearerEnv, "")
	for _, ok := range []string{
		"/run/user/1000/axis.sock",
		"unix:///run/axis.sock",
		"http://127.0.0.1:42425",
		"http://localhost:42425",
		"http://[::1]:42425",
		"localhost:42425",
		"127.0.0.1:42425",
	} {
		if err := RefuseOffBoxBearer(ok); err != nil {
			t.Errorf("local addr %q must pass, got %v", ok, err)
		}
	}
	for _, bad := range []string{
		"http://192.0.2.5:42425",
		"192.0.2.5:42425",
		"http://daemon.internal:42425",
		"example.com:80",
	} {
		if err := RefuseOffBoxBearer(bad); err == nil {
			t.Errorf("off-box addr %q must be refused", bad)
		}
	}
	if err := RefuseOffBoxBearer(""); err != nil {
		t.Errorf("empty addr must pass (no override), got %v", err)
	}

	t.Setenv(AllowOffBoxBearerEnv, "1")
	if err := RefuseOffBoxBearer("http://192.0.2.5:42425"); err != nil {
		t.Errorf("break-glass env must allow off-box, got %v", err)
	}
}

// TestRefuseOffBoxBearer_NamesLocalToken pins the refusal wording: the token
// at stake is this machine's own API token, not a shared cluster secret, and
// the error must say which file (or env var) it is.
func TestRefuseOffBoxBearer_NamesLocalToken(t *testing.T) {
	t.Setenv(AllowOffBoxBearerEnv, "")
	err := RefuseOffBoxBearer("http://192.0.2.5:42425")
	if err == nil {
		t.Fatal("off-box addr must be refused")
	}
	msg := err.Error()
	for _, want := range []string{"local API token", "~/.axis/token", "AXIS_API_TOKEN", AllowOffBoxBearerEnv} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q must mention %q", msg, want)
		}
	}
	if strings.Contains(msg, "cluster") {
		t.Errorf("refusal %q must not call the local token a cluster token", msg)
	}
}

func TestAddrKeepsClusterBearer_IPv6AndSchemeVariants(t *testing.T) {
	if !AddrKeepsClusterBearer("http://[::1]:8080/x") {
		t.Fatal("[::1] must count as loopback")
	}
	if AddrKeepsClusterBearer("http://[fe80::1]:8080/x") {
		t.Fatal("link-local must not count as loopback")
	}
	if AddrKeepsClusterBearer("HTTP://LOCALHOST:1") {
		t.Fatal("uppercase scheme must be refused; the client dials the scheme text as the host")
	}
}

func TestRefuseOffBoxBearer_ClassifiesRequestHost(t *testing.T) {
	t.Setenv(AllowOffBoxBearerEnv, "")
	cases := []string{
		"HTTP://LOCALHOST:42425",
		"offbox.example://localhost:42425",
	}
	for _, raw := range cases {
		base := RequestBaseURL(raw)
		if host := DialHost(base); host == "localhost" || host == "127.0.0.1" || host == "::1" {
			t.Fatalf("request host for %q is %q (base %q); want the off-box host the client dials", raw, host, base)
		}
		if err := RefuseOffBoxBearer(raw); err == nil {
			t.Fatalf("%q must be refused; request base is %q", raw, base)
		}
	}
	if host := DialHost(RequestBaseURL("http://127.0.0.1:42425")); host != "127.0.0.1" {
		t.Fatalf("loopback request host = %q", host)
	}
}

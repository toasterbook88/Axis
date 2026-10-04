package auth

import "testing"

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
		"http://192.168.1.249:42425",
		"192.168.1.249:42425",
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
	if err := RefuseOffBoxBearer("http://192.168.1.249:42425"); err != nil {
		t.Errorf("break-glass env must allow off-box, got %v", err)
	}
}

func TestAddrKeepsClusterBearer_IPv6AndSchemeVariants(t *testing.T) {
	if !AddrKeepsClusterBearer("http://[::1]:8080/x") {
		t.Fatal("[::1] must count as loopback")
	}
	if AddrKeepsClusterBearer("http://[fe80::1]:8080/x") {
		t.Fatal("link-local must not count as loopback")
	}
	if !AddrKeepsClusterBearer("HTTP://LOCALHOST:1") {
		t.Fatal("case-insensitive localhost must pass")
	}
}

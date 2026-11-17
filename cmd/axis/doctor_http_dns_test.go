package main

import (
	"context"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDoctorHTTPDNSHonorsEndpointContext(t *testing.T) {
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			select {
			case <-time.After(150 * time.Millisecond):
				return nil, errors.New("test DNS unavailable")
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}
	t.Cleanup(func() { net.DefaultResolver = previous })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := probeDoctorHTTPEndpoint(ctx, "http://slow-dns.example/mcp", nil, nil)
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("DNS ignored 20ms endpoint deadline: elapsed=%v, err=%v", elapsed, err)
	}
}

func TestDoctorHTTPDNSRebindingRespectsInternalAllowlist(t *testing.T) {
	var reached atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	var lookups atomic.Int32
	stubDoctorDNSAnswers(t, func() []net.IP {
		if lookups.Add(1) > 1 {
			return []net.IP{net.ParseIP("127.0.0.1")}
		}
		return []net.IP{net.ParseIP("192.0.2.10")}
	})
	previousTransport := http.DefaultTransport
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	http.DefaultTransport = transport
	t.Cleanup(func() { transport.CloseIdleConnections(); http.DefaultTransport = previousTransport })
	endpoint := strings.Replace(server.URL, "127.0.0.1", "rebind.example.com", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	status, err := probeDoctorHTTPEndpoint(ctx, endpoint, nil, nil)
	if reached.Load() {
		t.Fatalf("unallowlisted loopback reached after rebinding: lookups=%d, status=%d, err=%v", lookups.Load(), status, err)
	}
	if got := lookups.Load(); got != 1 {
		t.Fatalf("resolved %d times, want one checked resolution", got)
	}
}

func TestDoctorHTTPRejectsMixedDNSAddresses(t *testing.T) {
	stubDoctorDNSAnswers(t, func() []net.IP {
		return []net.IP{net.ParseIP("192.0.2.10"), net.ParseIP("127.0.0.1")}
	})
	_, err := probeDoctorHTTPEndpoint(context.Background(), "http://mixed.example.com/mcp", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "disallowed internal address") {
		t.Fatalf("mixed public/internal answer must be rejected before dialing: %v", err)
	}
}

func TestDoctorHTTPPreservesTLSHostnameHeadersAndSameOriginRedirect(t *testing.T) {
	const hostname = "example.com"
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.TLS.ServerName != hostname || !strings.HasPrefix(r.Host, hostname+":") || r.Header.Get("X-Test-Header") != "yes" {
			t.Errorf("lost request identity: SNI=%q Host=%q header=%q", r.TLS.ServerName, r.Host, r.Header.Get("X-Test-Header"))
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if r.URL.Path == "/mcp" {
			http.Redirect(w, r, "/ready", http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer server.Close()
	certificate, err := x509.ParseCertificate(server.TLS.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := certificate.VerifyHostname(hostname); err != nil {
		t.Fatalf("test certificate must cover the original hostname: %v", err)
	}
	stubDoctorDNSAnswers(t, func() []net.IP { return []net.IP{net.ParseIP("127.0.0.1")} })
	previousTransport := http.DefaultTransport
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = func(*http.Request) (*url.URL, error) {
		return nil, errors.New("MCP probe must connect directly")
	}
	http.DefaultTransport = transport
	t.Cleanup(func() { transport.CloseIdleConnections(); http.DefaultTransport = previousTransport })
	endpoint := strings.Replace(server.URL, "127.0.0.1", hostname, 1) + "/mcp"
	status, err := probeDoctorHTTPEndpoint(context.Background(), endpoint, map[string]string{"X-Test-Header": "yes"}, []string{hostname})
	if err != nil || status != http.StatusMethodNotAllowed || requests.Load() != 2 {
		t.Fatalf("same-origin TLS probe: status=%d err=%v requests=%d", status, err, requests.Load())
	}
}

func TestDoctorHTTPRejectsCrossOriginRedirectAndBoundsLoops(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	status, err := probeDoctorHTTPEndpoint(context.Background(), source.URL, nil, []string{"127.0.0.1"})
	if status != http.StatusFound || err == nil || targetRequests.Load() != 0 {
		t.Fatalf("cross-origin redirect followed: status=%d err=%v target requests=%d", status, err, targetRequests.Load())
	}
	var loopRequests atomic.Int32
	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loopRequests.Add(1)
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	defer loop.Close()
	_, err = probeDoctorHTTPEndpoint(context.Background(), loop.URL, nil, []string{"127.0.0.1"})
	if err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") || loopRequests.Load() != maxDoctorHTTPRedirects {
		t.Fatalf("unbounded redirect loop: err=%v requests=%d", err, loopRequests.Load())
	}
}

func stubDoctorDNSAnswers(t *testing.T, answers func() []net.IP) {
	t.Helper()
	previousResolver := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			var length [2]byte
			if _, err := io.ReadFull(server, length[:]); err != nil {
				return
			}
			query := make([]byte, binary.BigEndian.Uint16(length[:]))
			if _, err := io.ReadFull(server, query); err != nil {
				return
			}
			end := 12
			for query[end] != 0 {
				end += int(query[end]) + 1
			}
			end++
			qtype := binary.BigEndian.Uint16(query[end : end+2])
			end += 4
			response := append([]byte(nil), query[:end]...)
			binary.BigEndian.PutUint16(response[2:4], 0x8180)
			binary.BigEndian.PutUint16(response[6:8], 0)
			binary.BigEndian.PutUint16(response[8:10], 0)
			binary.BigEndian.PutUint16(response[10:12], 0)
			if qtype == 1 {
				ips := answers()
				binary.BigEndian.PutUint16(response[6:8], uint16(len(ips)))
				for _, ip := range ips {
					response = append(response, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 0, 0, 4)
					response = append(response, ip.To4()...)
				}
			}
			binary.BigEndian.PutUint16(length[:], uint16(len(response)))
			_, _ = server.Write(append(length[:], response...))
		}()
		return client, nil
	}}
	t.Cleanup(func() { net.DefaultResolver = previousResolver })
}

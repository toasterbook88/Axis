//go:build linux

package main

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestDoctorHTTPPreservesAddressFallback(t *testing.T) {
	good, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer good.Close()
	port := good.Addr().(*net.TCPAddr).Port
	bad, err := net.ListenTCP("tcp6", &net.TCPAddr{IP: net.ParseIP("::1"), Port: port})
	if err != nil {
		t.Skipf("IPv6 loopback required: %v", err)
	}
	defer bad.Close()
	raw, err := bad.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	// Saturate only the IPv6 backlog; its next connect stalls while IPv4 works.
	var listenErr error
	if err := raw.Control(func(fd uintptr) { listenErr = syscall.Listen(int(fd), 0) }); err != nil {
		t.Fatal(err)
	}
	if listenErr != nil {
		t.Fatal(listenErr)
	}
	fill, err := net.DialTimeout("tcp6", bad.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer fill.Close()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })}
	go server.Serve(good)
	defer server.Close()
	ips, err := net.DefaultResolver.LookupIPAddr(context.Background(), "localhost")
	if err != nil || len(ips) < 2 || ips[0].IP.To4() != nil {
		t.Skipf("IPv6-first localhost required: ips=%v err=%v", ips, err)
	}
	endpoint := "http://" + net.JoinHostPort("localhost", strconv.Itoa(port)) + "/mcp"
	baselineTransport := http.DefaultTransport.(*http.Transport).Clone()
	baselineTransport.Proxy = nil
	defer baselineTransport.CloseIdleConnections()
	baselineClient := &http.Client{Transport: baselineTransport, Timeout: 5 * time.Second}
	start := time.Now()
	resp, err := baselineClient.Get(endpoint)
	if err != nil {
		t.Fatalf("normal transport fallback failed: %v", err)
	}
	resp.Body.Close()
	t.Logf("normal transport fallback succeeded in %v", time.Since(start))
	start = time.Now()
	status, err := probeDoctorHTTPEndpoint(context.Background(), endpoint, nil, []string{"localhost"})
	if err != nil || status != http.StatusOK {
		t.Fatalf("reachable second address not used: elapsed=%v status=%d err=%v", time.Since(start), status, err)
	}
	t.Logf("guarded transport fallback succeeded in %v", time.Since(start))
}

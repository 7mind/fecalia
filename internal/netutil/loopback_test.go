package netutil

import (
	"net"
	"testing"
)

// TestIsLoopbackHost is a direct table test for IsLoopbackHost's fail-closed /
// security branches (T160 round 2): every caller (config.Monitor.validate,
// internal/metrics's requireLoopback) trusts a "false" result to mean "treat
// as off-host and require authentication", so a regression that flips the
// empty-host branch to loopback=true would silently disable the
// non-loopback-requires-token invariant. This test exercises the branches
// directly, rather than only indirectly via config Load.
func TestIsLoopbackHost(t *testing.T) {
	cases := []struct {
		name         string
		addr         string
		wantLoopback bool
		wantErr      bool
	}{
		{name: "empty host fails closed (binds all interfaces)", addr: ":9096", wantLoopback: false, wantErr: false},
		{name: "IPv6 loopback literal", addr: "[::1]:9096", wantLoopback: true, wantErr: false},
		{name: "IPv6 unspecified is non-loopback", addr: "[::]:9096", wantLoopback: false, wantErr: false},
		{name: "IPv4 loopback literal", addr: "127.0.0.1:9096", wantLoopback: true, wantErr: false},
		{name: "IPv4 unspecified is non-loopback", addr: "0.0.0.0:9096", wantLoopback: false, wantErr: false},
		{name: "public IP is non-loopback", addr: "8.8.8.8:9096", wantLoopback: false, wantErr: false},
		{name: "bare port with no host separator is an error", addr: "9096", wantLoopback: false, wantErr: true},
		{name: "missing port is an error", addr: "127.0.0.1", wantLoopback: false, wantErr: true},
		{name: "malformed input is an error", addr: "[::1", wantLoopback: false, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loopback, err := IsLoopbackHost(tc.addr)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("IsLoopbackHost(%q) = (%v, nil), want an error", tc.addr, loopback)
				}
				return
			}
			if err != nil {
				t.Fatalf("IsLoopbackHost(%q) unexpected error: %v", tc.addr, err)
			}
			if loopback != tc.wantLoopback {
				t.Errorf("IsLoopbackHost(%q) = %v, want %v", tc.addr, loopback, tc.wantLoopback)
			}
		})
	}
}

// TestIsLoopbackHostLocalhost exercises the hostname-resolution branch
// (as opposed to the IP-literal fast path above) via the "localhost" name.
// The expected classification is derived from the same net.LookupIP the
// implementation uses, rather than hardcoded, so the test stays correct
// regardless of the test host's /etc/hosts or resolver configuration.
func TestIsLoopbackHostLocalhost(t *testing.T) {
	ips, err := net.LookupIP("localhost")
	if err != nil {
		t.Skipf("cannot resolve localhost in this environment: %v", err)
	}
	wantLoopback := len(ips) > 0
	for _, ip := range ips {
		if !ip.IsLoopback() {
			wantLoopback = false
			break
		}
	}

	loopback, err := IsLoopbackHost("localhost:9096")
	if err != nil {
		t.Fatalf("IsLoopbackHost(\"localhost:9096\") unexpected error: %v", err)
	}
	if loopback != wantLoopback {
		t.Errorf("IsLoopbackHost(\"localhost:9096\") = %v, want %v (resolved %v)", loopback, wantLoopback, ips)
	}
}

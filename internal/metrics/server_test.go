package metrics

import (
	"errors"
	"net"
	"strings"
	"testing"
)

// TestNonLoopbackBindRefused asserts that constructing the server on any
// non-loopback (or wildcard) address is refused with ErrNonLoopbackBind and that
// no listener is opened.
func TestNonLoopbackBindRefused(t *testing.T) {
	cases := []struct {
		name string
		addr string
	}{
		{"wildcard v4", "0.0.0.0:0"},
		{"wildcard empty host", ":9095"},
		{"routable literal", "192.0.2.10:9095"},
		{"wildcard v6", "[::]:0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, err := NewServer(tc.addr, fakeSource{}, testLogger(t))
			if err == nil {
				ctx := t.Context()
				_ = srv.Close(ctx)
				t.Fatalf("NewServer(%q) succeeded, want ErrNonLoopbackBind", tc.addr)
			}
			if !errors.Is(err, ErrNonLoopbackBind) {
				t.Fatalf("NewServer(%q) err = %v, want ErrNonLoopbackBind", tc.addr, err)
			}
			if srv != nil {
				t.Fatalf("NewServer(%q) returned a non-nil server on refusal", tc.addr)
			}
		})
	}
}

// TestLoopbackBindAccepted asserts loopback literals bind successfully.
func TestLoopbackBindAccepted(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:0", "[::1]:0"} {
		t.Run(addr, func(t *testing.T) {
			srv, err := NewServer(addr, fakeSource{}, testLogger(t))
			if err != nil {
				t.Fatalf("NewServer(%q): %v", addr, err)
			}
			ctx := t.Context()
			if err := srv.Close(ctx); err != nil {
				t.Errorf("Close: %v", err)
			}
		})
	}
}

// TestInvalidListenAddress asserts a malformed address is rejected with a parse
// error (not ErrNonLoopbackBind).
func TestInvalidListenAddress(t *testing.T) {
	_, err := NewServer("not-an-address", fakeSource{}, testLogger(t))
	if err == nil {
		t.Fatal("NewServer with malformed address succeeded, want error")
	}
	if !strings.Contains(err.Error(), "invalid listen address") {
		t.Fatalf("err = %v, want invalid listen address", err)
	}
}

// TestVerifyLoopbackBind exercises the fail-closed, act-then-verify half of the
// loopback guard directly against a net.Addr, independent of DNS resolution.
// This is the check that closes the TOCTOU between requireLoopback's resolution
// and net.Listen's own second resolution.
func TestVerifyLoopbackBind(t *testing.T) {
	cases := []struct {
		name    string
		addr    net.Addr
		wantErr bool
	}{
		{"loopback v4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9095}, false},
		{"loopback v4 low", &net.TCPAddr{IP: net.IPv4(127, 255, 255, 254), Port: 9095}, false},
		{"loopback v6", &net.TCPAddr{IP: net.IPv6loopback, Port: 9095}, false},
		{"routable v4", &net.TCPAddr{IP: net.IPv4(192, 0, 2, 10), Port: 9095}, true},
		{"routable v6", &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 9095}, true},
		{"non-tcp", &net.UnixAddr{Name: "/tmp/metrics.sock", Net: "unix"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyLoopbackBind(tc.addr)
			if tc.wantErr {
				if !errors.Is(err, ErrNonLoopbackBind) {
					t.Fatalf("verifyLoopbackBind(%v) err = %v, want ErrNonLoopbackBind", tc.addr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("verifyLoopbackBind(%v): %v", tc.addr, err)
			}
		})
	}
}

// TestHostnameBindVerifiedLoopback asserts that a hostname listen address
// ("localhost:0") binds and the post-bind verification confirms the kernel
// bound a loopback interface — the guard ENFORCES loopback on the concrete
// bound address, not merely on a pre-Listen resolution.
func TestHostnameBindVerifiedLoopback(t *testing.T) {
	srv, err := NewServer("localhost:0", fakeSource{}, testLogger(t))
	if err != nil {
		t.Fatalf("NewServer(localhost:0): %v", err)
	}
	defer func() { _ = srv.Close(t.Context()) }()

	tcp, ok := srv.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("Addr() = %T, want *net.TCPAddr", srv.Addr())
	}
	if !tcp.IP.IsLoopback() {
		t.Fatalf("bound addr %s is not loopback", tcp.IP)
	}
}

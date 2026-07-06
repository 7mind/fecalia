package metrics

import (
	"errors"
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

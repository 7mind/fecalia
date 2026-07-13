package dnsresolve

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSystemResolverResolvesLocalhost(t *testing.T) {
	sr := NewSystemResolver()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	addrs, _, ttlOk, err := sr.Lookup(ctx, "localhost")
	if err != nil {
		t.Fatalf("Lookup(localhost): unexpected error: %v", err)
	}
	if len(addrs) == 0 {
		t.Fatal("Lookup(localhost): got no addrs")
	}
	for _, a := range addrs {
		if !a.IsLoopback() {
			t.Fatalf("Lookup(localhost): addr %v is not loopback", a)
		}
	}
	if ttlOk {
		t.Fatal("Lookup(localhost): ttlOk = true, want false (system resolver never exposes TTL)")
	}
}

func TestSystemResolverContextCancellation(t *testing.T) {
	sr := NewSystemResolver()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, _, _, err := sr.Lookup(ctx, "some.nonexistent.invalid.example")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Lookup(canceled ctx): expected non-nil error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Lookup(canceled ctx): err = %v, want an error wrapping context.Canceled", err)
	}
	if elapsed > time.Second {
		t.Fatalf("Lookup(canceled ctx): took %v, want prompt return", elapsed)
	}
}

package dnsresolve

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

func TestFakeResolverResolvesMappedHost(t *testing.T) {
	want := []netip.Addr{
		netip.MustParseAddr("203.0.113.10"),
		netip.MustParseAddr("2001:db8::10"),
	}
	fr := &FakeResolver{Hosts: map[string][]netip.Addr{"peer.example": want}}

	got, minTTL, ttlOk, err := fr.Lookup(context.Background(), "peer.example")
	if err != nil {
		t.Fatalf("Lookup(mapped): unexpected error: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("Lookup(mapped): got %d addrs, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Lookup(mapped): addr[%d] = %v, want %v (order must be preserved)", i, got[i], want[i])
		}
	}
	if minTTL != 0 || ttlOk {
		t.Fatalf("Lookup(mapped): minTTL=%v ttlOk=%v, want zero-value defaults", minTTL, ttlOk)
	}
}

func TestFakeResolverUnmappedHostErrors(t *testing.T) {
	fr := &FakeResolver{Hosts: map[string][]netip.Addr{
		"peer.example": {netip.MustParseAddr("203.0.113.10")},
	}}

	_, _, _, err := fr.Lookup(context.Background(), "unknown.example")
	if err == nil {
		t.Fatal("Lookup(unmapped): expected non-nil error, got nil")
	}
}

func TestFakeResolverReturnsConfiguredTTL(t *testing.T) {
	fr := &FakeResolver{
		Hosts:  map[string][]netip.Addr{"peer.example": {netip.MustParseAddr("203.0.113.10")}},
		MinTTL: 30 * time.Second,
		TTLOk:  true,
	}

	_, minTTL, ttlOk, err := fr.Lookup(context.Background(), "peer.example")
	if err != nil {
		t.Fatalf("Lookup: unexpected error: %v", err)
	}
	if minTTL != 30*time.Second || !ttlOk {
		t.Fatalf("Lookup: minTTL=%v ttlOk=%v, want 30s/true", minTTL, ttlOk)
	}
}

func TestFakeResolverContextCancellation(t *testing.T) {
	fr := &FakeResolver{Hosts: map[string][]netip.Addr{"peer.example": {netip.MustParseAddr("203.0.113.10")}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, _, _, err := fr.Lookup(ctx, "peer.example")
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Lookup(canceled ctx): err = %v, want context.Canceled", err)
	}
	if elapsed > time.Second {
		t.Fatalf("Lookup(canceled ctx): took %v, want prompt return", elapsed)
	}
}

package bind

import (
	"net/netip"
	"sync/atomic"
	"testing"
)

func TestMultipathDirectSendCompletionIsSynchronousAndExact(t *testing.T) {
	m, err := newMultipath(t, loopbackPaths(1), testKey(t, 0xC8))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	m.paths[0].setRemote(netip.MustParseAddrPort("127.0.0.1:9"))

	var completions atomic.Uint32
	if err := m.SendWithCompletion(
		[][]byte{[]byte("direct-completion")},
		m.virt,
		func() { completions.Add(1) },
	); err != nil {
		t.Fatal(err)
	}
	if got := completions.Load(); got != 1 {
		t.Fatalf("direct completion count = %d, want 1 before return", got)
	}
}

func TestMultipathSendCompletionRunsOnPreTransferError(t *testing.T) {
	m, err := newMultipath(t, loopbackPaths(1), testKey(t, 0xC9))
	if err != nil {
		t.Fatal(err)
	}
	var completions atomic.Uint32
	if err := m.SendWithCompletion(
		[][]byte{[]byte("no-open-path")},
		m.virt,
		func() { completions.Add(1) },
	); err == nil {
		t.Fatal("send without an open path succeeded")
	}
	if got := completions.Load(); got != 1 {
		t.Fatalf("pre-transfer completion count = %d, want 1 before error return", got)
	}
}

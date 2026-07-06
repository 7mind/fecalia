package bind

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

// TestPassthroughLoopback round-trips a datagram through the pass-through Bind on
// loopback: Open a random port, start the receive callbacks, Send to
// 127.0.0.1:<port>, and assert one callback delivers the exact payload. This
// exercises Send + the ReceiveFunc path without a TUN device.
func TestPassthroughLoopback(t *testing.T) {
	b := NewPassthrough()
	fns, port, err := b.Open(0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	if len(fns) == 0 {
		t.Fatal("Open returned no receive functions")
	}

	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, len(fns))
	for _, fn := range fns {
		go func(fn ReceiveFunc) {
			bufs := [][]byte{make([]byte, 2048)}
			sizes := make([]int, 1)
			eps := make([]Endpoint, 1)
			n, err := fn(bufs, sizes, eps)
			if err != nil {
				ch <- result{err: err}
				return
			}
			if n >= 1 {
				ch <- result{data: append([]byte(nil), bufs[0][:sizes[0]]...)}
			}
		}(fn)
	}

	// Let the receive goroutines reach their blocking read before sending.
	time.Sleep(50 * time.Millisecond)

	ep, err := b.ParseEndpoint(fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("ParseEndpoint: %v", err)
	}
	payload := []byte("wanbond-loopback-probe")
	if err := b.Send([][]byte{payload}, ep); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("receive: %v", r.err)
		}
		if !bytes.Equal(r.data, payload) {
			t.Fatalf("received %q, want %q", r.data, payload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for loopback datagram")
	}
}

// TestBatchSizePositive is a light contract check on the delegated bind.
func TestBatchSizePositive(t *testing.T) {
	b := NewPassthrough()
	if b.BatchSize() < 1 {
		t.Fatalf("BatchSize = %d, want >= 1", b.BatchSize())
	}
}

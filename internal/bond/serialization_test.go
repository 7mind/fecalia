package bond

import (
	"testing"
	"time"
)

func TestSerializationDoesNotInventForwardQueue(t *testing.T) {
	now := time.Unix(100, 0)
	a := New(Epoch{Boot: 1, Generation: 1})
	remote := Epoch{Boot: 2, Generation: 1}
	a.SetRemote(remote, true)
	a.Path(0, 0, 40*time.Millisecond, now)
	// Establish the receiver clock origin with a lone empty keepalive.
	a.Poll(now.Add(200 * time.Millisecond))
	const capacity = 50000 // A 0.4 Mbit/s path, with no other traffic or router queue.
	const propagation = 20 * time.Millisecond
	serialization := func(bytes int) time.Duration { return time.Duration(float64(bytes) / capacity * float64(time.Second)) }
	keepaliveArrival := now.Add(200*time.Millisecond + propagation + serialization(wireOverhead))
	ack := acknowledgement{observed: a.Epoch(), high: 1, mask: 1, bytes: wireOverhead}
	if _, err := a.Receive(0, ackFrame(remote, 0, 1, ack), keepaliveArrival.Add(propagation+serialization(193))); err != nil {
		t.Fatal(err)
	}
	sent := now.Add(350 * time.Millisecond)
	if err := a.Enqueue(make([]byte, 1200), sent); err != nil {
		t.Fatal(err)
	}
	packets := a.Poll(sent)
	if len(packets) != 1 {
		t.Fatalf("expected one isolated data transmission, got %d", len(packets))
	}
	arrival := sent.Add(propagation + serialization(1200+wireOverhead))
	ack = acknowledgement{observed: a.Epoch(), high: 2, mask: 3, bytes: 1200 + 2*wireOverhead, elapsed: uint64(arrival.Sub(keepaliveArrival)), receivedHigh: 1, receivedMask: [ackReceiptWords]uint64{1}}
	if _, err := a.Receive(0, ackFrame(remote, 0, 2, ack), arrival.Add(propagation+serialization(193))); err != nil {
		t.Fatal(err)
	}
	if delay := a.Snapshot(arrival).Paths[0].QueueDelay; delay > targetQueue {
		t.Fatalf("an isolated larger datagram acquired %s of invented queue delay from serialization", delay)
	}
	sent = now.Add(500 * time.Millisecond)
	if err := a.Enqueue(make([]byte, 1200), sent); err != nil {
		t.Fatal(err)
	}
	a.Poll(sent)
	const actualQueue = 40 * time.Millisecond
	arrival = sent.Add(propagation + serialization(1200+wireOverhead) + actualQueue)
	ack = acknowledgement{observed: a.Epoch(), high: 3, mask: 7, bytes: 2400 + 3*wireOverhead, elapsed: uint64(arrival.Sub(keepaliveArrival)), receivedHigh: 2, receivedMask: [ackReceiptWords]uint64{3}}
	if _, err := a.Receive(0, ackFrame(remote, 0, 3, ack), arrival.Add(propagation+serialization(193))); err != nil {
		t.Fatal(err)
	}
	if delay := a.Snapshot(arrival).Paths[0].QueueDelay; delay != actualQueue {
		t.Fatalf("actual queue delay was hidden by the size baseline: got %s, want %s", delay, actualQueue)
	}
}

package bond

import (
	"testing"
	"time"
)

func TestInitialFeedbackAllowsDataSerialization(t *testing.T) {
	now := time.Unix(100, 0)
	a := New(Epoch{Boot: 1, Generation: 1})
	a.SetRemote(Epoch{Boot: 2, Generation: 1}, true)
	a.Path(0, 0, 40*time.Millisecond, now)
	if err := a.Enqueue(make([]byte, 1200), PacketMetadata{}, now); err != nil {
		t.Fatal(err)
	}
	poll(a, now)
	// At 0.4 Mbit/s, a 1329-byte datagram and 193-byte ACK add 30.4 ms
	// serialization to 40 ms propagation and up to 25 ms ACK delay.
	poll(a, now.Add(90*time.Millisecond))
	state := a.Snapshot(now.Add(90 * time.Millisecond)).Paths[0]
	if !state.Up || state.Rate != initialRate || state.InFlight < 1200+wireOverhead {
		t.Fatalf("healthy initial data was declared lost before its first ACK could return: %+v", state)
	}
}

func TestPriorityWindowBorrowsOnlyOneDatagram(t *testing.T) {
	now := time.Unix(100, 0)
	a := New(Epoch{Boot: 1, Generation: 1})
	a.SetRemote(Epoch{Boot: 2, Generation: 1}, true)
	a.Path(0, 0, 40*time.Millisecond, now)
	a.paths[0].rate = 10000000
	a.paths[0].inflight = a.paths[0].window()
	for range 20 {
		if err := a.Enqueue(make([]byte, 224), PacketMetadata{}, now); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(poll(a, now)); got != 1 {
		t.Fatalf("full congestion window admitted %d additional priority datagrams, want one", got)
	}
}

func TestLoadedRTTVariationDoesNotExpandWindow(t *testing.T) {
	p := lane{rate: 100000, baseRTT: 40 * time.Millisecond, idleRTTVariation: 5 * time.Millisecond, confirmedWireBytes: 100000}
	unloaded := p.window()
	p.rttVariation = 80 * time.Millisecond
	if p.window() > unloaded {
		t.Fatalf("queue-induced RTT variation expanded in-flight allowance from %d to %d bytes", unloaded, p.window())
	}
}

func TestLoadedBaseRTTDoesNotExpandWindow(t *testing.T) {
	p := lane{rate: 100000, baseRTT: 40 * time.Millisecond, idleRTT: 40 * time.Millisecond, confirmedWireBytes: 100000}
	unloaded := p.window()
	p.baseRTT = 80 * time.Millisecond
	if p.window() > unloaded {
		t.Fatalf("loaded RTT minimum expanded the window from %d to %d bytes", unloaded, p.window())
	}
}

func TestStartupAllowsOneDatagramLargerThanWindow(t *testing.T) {
	now := time.Unix(100, 0)
	a := New(Epoch{Boot: 1, Generation: 1})
	a.SetRemote(Epoch{Boot: 2, Generation: 1}, true)
	a.Path(0, 0, 80*time.Millisecond, now)
	for range 2 {
		if err := a.Enqueue(make([]byte, maxDatagram), PacketMetadata{}, now); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(poll(a, now)); got != 1 {
		t.Fatalf("startup sent %d large datagrams, want exactly one", got)
	}
	if got := len(poll(a, now.Add(80*time.Millisecond))); got != 0 {
		t.Fatalf("unacknowledged large datagram admitted %d more", got)
	}
}

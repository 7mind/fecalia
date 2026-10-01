package bond_test

import (
	"testing"
	"time"
)

// One call needs a third of a 0.5 Mbit/s lane. Bulk took the rest: every full
// datagram ahead of a voice datagram is 22 ms of serialization there, for a
// third of a megabit beside a 50 Mbit/s lane. Before bulk could discover a
// lane that carries a call, the lane stayed undiscovered and clean by
// accident; a lane whose capacity was already known was shared.
func TestBulkKeepsOffTheSlowLaneACallRides(t *testing.T) {
	o := mixedLoad{lanes: []varyingLane{lowLatencyLane, steadyLane}, offered: 8e6, seconds: 30, failed: -1}.run()
	t.Logf("bulk %.0f B/s; voice %d/%d, one-way p99 %s", o.bulk, o.voiceDelivered, o.voiceSent, o.voiceP99)
	// The lane costs 14 ms and a voice datagram 6 ms of serialization.
	if o.voiceDelivered < o.voiceSent*99/100 || o.voiceP99 > 25*time.Millisecond {
		t.Errorf("voice %d/%d delivered, one-way p99 %s", o.voiceDelivered, o.voiceSent, o.voiceP99)
	}
	if o.bulk < 0.8*steadyLane.rate {
		t.Errorf("bulk received %.0f B/s, want at least %.0f", o.bulk, 0.8*steadyLane.rate)
	}
}

// A lane fast enough that a full datagram costs a call less than the queue it
// is allowed carries bulk beside it: keeping bulk off a 2 Mbit/s lane would
// give up a quarter of a 2+6 Mbit/s bond for 6 ms.
func TestBulkSharesAFastLaneWithACall(t *testing.T) {
	first := varyingLane{rate: 250000, delay: 15 * time.Millisecond, buffer: 100 * time.Millisecond}
	second := varyingLane{rate: 750000, delay: 25 * time.Millisecond, buffer: 100 * time.Millisecond}
	o := mixedLoad{lanes: []varyingLane{first, second}, offered: 2e6, seconds: 30, failed: -1}.run()
	t.Logf("bulk %.0f B/s; voice %d/%d, one-way p99 %s", o.bulk, o.voiceDelivered, o.voiceSent, o.voiceP99)
	if combined := first.rate + second.rate; o.bulk < 0.75*combined {
		t.Errorf("bulk received %.0f B/s, want at least %.0f", o.bulk, 0.75*combined)
	}
	if o.voiceDelivered < o.voiceSent*99/100 || o.voiceP99 > 40*time.Millisecond {
		t.Errorf("voice %d/%d delivered, one-way p99 %s", o.voiceDelivered, o.voiceSent, o.voiceP99)
	}
}

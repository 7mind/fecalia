package bond_test

import "testing"

// A bulk flood must not shut voice out at the tunnel's entrance. The bound on
// queued and outstanding datagrams was one count for every class: a sender
// offering bulk faster than the lanes drain it held the count at the bound,
// and each voice datagram arriving meanwhile was refused. Model, 0.5 + 50
// Mbit/s, 128 Mbit/s of bulk offered: 18% of the voice datagrams lost.
func TestBulkFloodDoesNotRefuseVoice(t *testing.T) {
	o := mixedLoad{lanes: []varyingLane{lowLatencyLane, steadyLane}, offered: 16e6, seconds: 20, failed: -1}.run()
	t.Logf("voice %d/%d, one-way p99 %s, longest gap %s; bulk %.0f B/s", o.voiceDelivered, o.voiceSent, o.voiceP99, o.voiceGap, o.bulk)
	if o.voiceDelivered < o.voiceSent*99/100 {
		t.Errorf("voice delivered %d/%d", o.voiceDelivered, o.voiceSent)
	}
	if o.bulk < 0.8*steadyLane.rate {
		t.Errorf("bulk received %.0f B/s, want at least %.0f", o.bulk, 0.8*steadyLane.rate)
	}
}

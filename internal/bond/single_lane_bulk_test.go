package bond_test

import (
	"testing"
	"time"
)

// A download that starts beside a call on the only lane must discover the
// lane. The target of a discovering lane that carries a real-time stream is
// held within the discovery gain of what it delivered; voice was then more
// than half of that target, which made the lane a real-time lane and left
// bulk its minimum share, so delivery never grew and the bound never rose: a
// 50 Mbit/s model lane carried one bulk datagram a second for as long as the
// call lasted.
func TestBulkBesideVoiceDiscoversTheOnlyLane(t *testing.T) {
	for name, rate := range map[string]float64{"5 Mbit/s": 625000, "50 Mbit/s": 6.25e6} {
		t.Run(name, func(t *testing.T) {
			lane := steadyLane
			lane.rate = rate
			o := mixedLoad{lanes: []varyingLane{lane}, offered: 8e6, seconds: 30, failed: -1}.run()
			t.Logf("bulk %.0f B/s of a %.0f B/s lane; voice %d/%d, one-way p99 %s", o.bulk, rate, o.voiceDelivered, o.voiceSent, o.voiceP99)
			if o.bulk < 0.7*rate {
				t.Errorf("bulk received %.0f B/s, want at least %.0f", o.bulk, 0.7*rate)
			}
			// The lane costs 25 ms; a pulse's queue and a bulk datagram ahead the rest.
			if o.voiceDelivered < o.voiceSent*99/100 || o.voiceP99 > 60*time.Millisecond {
				t.Errorf("voice %d/%d delivered, one-way p99 %s", o.voiceDelivered, o.voiceSent, o.voiceP99)
			}
		})
	}
}

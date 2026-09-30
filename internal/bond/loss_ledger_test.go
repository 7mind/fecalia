package bond

import (
	"testing"
	"time"
)

// Datagrams that arrive late are missing from the receiver's byte count when a
// later one is acknowledged, and are in it soon after: the deficit rises and
// falls back. Only a deficit that stays is loss.
func TestLossLedgerTellsLateFromLost(t *testing.T) {
	const datagram = 1378
	start := time.Unix(100, 0)
	var late, lossy lossLedger
	var sent, lost uint64
	now := start
	// One datagram is sent and acknowledged every millisecond.
	for tick := 0; tick < 1500; tick++ {
		now = start.Add(time.Duration(tick) * time.Millisecond)
		seq := uint64(tick + 1)
		sent += datagram
		// For 30 of every 100 ms, twenty datagrams are overtaken.
		var overtaken uint64
		if tick%100 < 30 {
			overtaken = 20 * datagram
		}
		late.record(now, seq, sent, sent-overtaken, 0)
		// Every twentieth datagram is lost.
		if tick%20 == 0 {
			lost += datagram
		}
		lossy.record(now, seq, sent, sent-lost, 0)
	}
	if bytes, _, _ := late.lost(now); bytes != 0 || late.material(now) {
		t.Fatalf("late datagrams counted as %.0f lost bytes", bytes)
	}
	bytes, through, datagrams := lossy.lost(now)
	if share := bytes / through; share < 0.04 || share > 0.06 || !lossy.material(now) {
		t.Fatalf("a path losing 5%% measured %.0f of %.0f bytes lost over %.0f datagrams, material %v", bytes, through, datagrams, lossy.material(now))
	}
}

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

// What the sender takes to be still on its way is an estimate: every datagram
// below the acknowledged one that this acknowledgement did not confirm and
// that was sent shortly before it. It over-counts datagrams that arrived
// unconfirmed. After a stall the path delivers hundreds at once, and one
// acknowledgement confirms 64 of them by lane and 256 by receipt; while the
// rate is high more of them fall within the allowance than when it is low.
// The deficit then dipped below the loss so far, and its return read as loss.
// Production, 2026-10-02: a discovery ended at 7.9 MB/s, the target was cut
// to 3 MB/s, and the next second showed material loss on a lane whose peer
// received 25.90 of the 25.98 MB sent and skipped no sequence; the estimate
// was measured anew at 3.9 MB/s and the next two downloads ran at 24.8 Mbit/s
// where the lane carried 45 a minute later.
func TestLossLedgerIgnoresDatagramsCountedLateThatArrived(t *testing.T) {
	const datagram = 1378
	start := time.Unix(100, 0)
	var ledger lossLedger
	var sent uint64
	now := start
	// One datagram is sent every millisecond; all arrive.
	for tick := 0; tick < 2600; tick++ {
		now = start.Add(time.Duration(tick) * time.Millisecond)
		sent += datagram
		// For 200 ms, 300 datagrams that have arrived are still unconfirmed
		// and counted as on their way; afterwards 20 are.
		counted := uint64(20 * datagram)
		if tick >= 1300 && tick < 1500 {
			counted = 300 * datagram
		}
		ledger.record(now, uint64(tick+1), sent, sent, counted)
		ledger.settled(now)
	}
	if bytes, through, _ := ledger.lost(now); bytes != 0 || ledger.material(now) {
		t.Fatalf("a path that lost nothing measured %.0f of %.0f bytes lost, material %v", bytes, through, ledger.material(now))
	}
}

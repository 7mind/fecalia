package bond_test

import (
	"testing"
	"time"
)

// isolatedStall is a 50 Mbit/s lane that serves nothing for about 400 ms
// about every sixteen seconds.
var isolatedStall = varyingLane{rate: 6.25e6, delay: 25 * time.Millisecond, buffer: time.Second, stall: 400 * time.Millisecond, stallEvery: 16 * time.Second}

// A datagram unconfirmed after the retransmission timeout is sent again, on
// another lane if one has room and otherwise on the same one. While a lane
// delivers nothing, everything in flight on it times out together, and what
// is sent again on it waits behind the originals: every repair is a
// duplicate, and the path delivers them before anything new once it serves
// again. One 400 ms stall of the model lane drew 1342 repairs, 1.8 MB, a
// quarter of a second of the lane's capacity. In production the concentrator
// sent 319-354 repairs within a second on the mobile lane at each such stall
// and the edge counted as many duplicates (2026-10-01: 1119 repairs and 1035
// duplicates in one 15 s download, the resequencer skipping 4 sequences).
func TestSilentLaneIsNotSentRepairs(t *testing.T) {
	steady := isolatedStall
	steady.stall, steady.stallEvery = 0, 0
	// Copies of the call's datagrams are counted with the repairs; the lane
	// carries as many of them without a stall.
	baseline := mixedLoad{lanes: []varyingLane{lowLatencyLane, steady}, offered: 8e6, seconds: 40, failed: -1}.run()
	o := mixedLoad{lanes: []varyingLane{lowLatencyLane, isolatedStall}, offered: 8e6, seconds: 40, failed: -1}.run()
	copies, repairs := baseline.sender.Paths[1].Retransmits, o.sender.Paths[1].Retransmits
	t.Logf("%d repairs and copies on the stalling lane against %d on the same lane without stalls; %d expired; bulk %.0f B/s", repairs, copies, o.sender.Expired, o.bulk)
	if repairs > copies+60 {
		t.Errorf("%d repairs sent on a lane that lost nothing", repairs-copies)
	}
}

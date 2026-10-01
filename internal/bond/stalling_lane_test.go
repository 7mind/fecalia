package bond_test

import (
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

// stallingLane carries 50 Mbit/s and serves nothing for about stall about
// once a second: it delivers what waited afterwards, in order, and loses
// nothing. The mobile downlink in production does this (2026-10-01, at 17
// samples a second: the bytes in flight doubled for 0.2-0.4 s about once a
// second, with queue delays of 65-180 ms, and the edge counted a duplicate
// for nearly every repair the concentrator sent).
func stallingLane(stall time.Duration) varyingLane {
	return varyingLane{rate: 6.25e6, delay: 25 * time.Millisecond, buffer: time.Second, stall: stall, stallEvery: time.Second}
}

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

// served is what the lane carries on average, stalls included.
func served(l varyingLane) float64 {
	return l.rate * (float64(l.stallEvery-l.stall) + l.stallRate*float64(l.stall)) / float64(l.stallEvery)
}

// coldStart reports the stalling lane's estimate ten seconds after bulk
// begins on it beside a call on the other lane, for each of eight schedules
// of stalls.
func coldStart(stall time.Duration) (estimates []float64, lane varyingLane) {
	lane = stallingLane(stall)
	for seed := uint64(0); seed < 8; seed++ {
		var estimate float64
		m := mixedLoad{lanes: []varyingLane{lowLatencyLane, lane}, offered: 8e6, seconds: 13, failed: -1, seed: seed}
		m.observe = func(second int, s bond.Snapshot) {
			if second == 11 {
				estimate = s.Paths[1].Capacity
			}
		}
		m.run()
		estimates = append(estimates, estimate)
	}
	return estimates, lane
}

// Discovery ends at the first congestion signal, with what the lane then
// delivers as its capacity. A stall is no such signal: the lane carries
// nothing for its length whatever is sent, and what it delivers when the
// stall hits says nothing about what it can carry. Ended by the first stall,
// discovery left the model lane at 3% of its capacity, and pulses needed half
// a minute to find the rest. In production the mobile uplink ended its first
// discovery after a restart at 0.26 MB/s, carried 1.5 Mbit/s through that
// Speedtest, and 4.0-4.9 two hours and several transfers later (2026-10-01).
func TestStallDoesNotEndDiscovery(t *testing.T) {
	for _, stall := range []time.Duration{100 * time.Millisecond, 200 * time.Millisecond} {
		t.Run(stall.String(), func(t *testing.T) {
			estimates, lane := coldStart(stall)
			t.Logf("estimate ten seconds after the start, per schedule: %.0f; the lane serves %.0f B/s", estimates, served(lane))
			for seed, estimate := range estimates {
				if estimate < 0.6*served(lane) {
					t.Errorf("schedule %d: estimate %.0f B/s ten seconds after the start", seed, estimate)
				}
			}
		})
	}
}

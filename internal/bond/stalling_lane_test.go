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

// stalledBulk runs saturating bulk over a call's lane and the given one for
// each of four schedules of stalls, and reports per schedule the bulk
// delivered in the measured half and the lowest estimate the lane held in it.
func stalledBulk(lane varyingLane) (bulk, lowest []float64) {
	for seed := uint64(0); seed < 4; seed++ {
		var low float64
		m := mixedLoad{lanes: []varyingLane{lowLatencyLane, lane}, offered: 8e6, seconds: 30, failed: -1, seed: seed}
		m.observe = func(second int, s bond.Snapshot) {
			// No estimate is reported while the lane discovers.
			if c := s.Paths[1].Capacity; second >= 15 && c > 0 && (low == 0 || c < low) {
				low = c
			}
		}
		bulk, lowest = append(bulk, m.run().bulk), append(lowest, low)
	}
	return bulk, lowest
}

// A stall is not a fall in capacity: the lane carries afterwards what it
// carried before. Its backlog delays what follows as a queue does, and read
// as one it ended discovery, lost the probes it fell on and lowered the
// estimate at every repeated signal. The model lane delivered 55% of what it
// serves under 200 ms stalls and 49% under 300 ms.
func TestStalledLaneKeepsItsEstimate(t *testing.T) {
	for _, stall := range []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond} {
		t.Run(stall.String(), func(t *testing.T) {
			lane := stallingLane(stall)
			bulk, lowest := stalledBulk(lane)
			t.Logf("the lane serves %.0f B/s; bulk per schedule %.0f, lowest estimates %.0f", served(lane), bulk, lowest)
			for seed := range bulk {
				if lowest[seed] < 0.7*served(lane) {
					t.Errorf("schedule %d: the estimate fell to %.0f B/s", seed, lowest[seed])
				}
				if bulk[seed] < 0.7*served(lane) {
					t.Errorf("schedule %d: bulk received %.0f B/s", seed, bulk[seed])
				}
			}
		})
	}
}

// A path may also slow down for a few hundred milliseconds without going
// silent. Delivery measured over a control interval follows it down, and an
// estimate taken or lowered from that was low by as much. In production a
// lane that had delivered 4.8-5.3 MB/s for a second and a half ended
// discovery at 5.06 MB/s; delivery then sank to 3.0-3.4 MB/s for half a
// second with queue delays of 72-184 ms, the estimate was measured anew at
// 3.22 MB/s, and the 7 s transfer ended before it was back (2026-10-01:
// 18-23 Mbit/s through the bond against 49-50 on the mobile link alone).
func TestSlowdownDoesNotLowerTheEstimate(t *testing.T) {
	lane := stallingLane(300 * time.Millisecond)
	lane.stallRate = 0.5
	bulk, lowest := stalledBulk(lane)
	t.Logf("the lane serves %.0f B/s; bulk per schedule %.0f, lowest estimates %.0f", served(lane), bulk, lowest)
	for seed := range bulk {
		if lowest[seed] < 0.75*served(lane) {
			t.Errorf("schedule %d: the estimate fell to %.0f B/s", seed, lowest[seed])
		}
		if bulk[seed] < 0.7*served(lane) {
			t.Errorf("schedule %d: bulk received %.0f B/s", seed, bulk[seed])
		}
	}
}

// A probe is judged by delay: it is lost if delay rises while its feedback is
// awaited. On a link whose delay rises now and then at any rate, that loses
// about half the probes to coincidence, and an estimate that began low stayed
// low. Production, 2026-10-02: delay signals came 13 times in a download at
// 22 Mbit/s and 15 times at 52; a lane that ended its first discovery at
// 3.5 MB/s won one probe in four or five and reached the 9 MB/s it then held
// in its fifth download.
//
// The lane sends below its estimate, so delivery above the estimate is the
// path catching up after it slowed: it carries that much.
func TestCatchUpRaisesTheEstimate(t *testing.T) {
	// The lane serves a tenth of its rate for about 150 ms about twice a
	// second, and delay rises each time whatever the lane sends.
	lane := varyingLane{rate: 6.25e6, delay: 25 * time.Millisecond, buffer: time.Second, stall: 150 * time.Millisecond, stallEvery: 500 * time.Millisecond, stallRate: 0.1}
	for seed := uint64(0); seed < 3; seed++ {
		var estimate float64
		m := mixedLoad{lanes: []varyingLane{lowLatencyLane, lane}, offered: 8e6, seconds: 40, failed: -1, seed: seed}
		m.observe = func(second int, s bond.Snapshot) {
			if second == 20 {
				estimate = s.Paths[1].Capacity
			}
		}
		o := m.run()
		t.Logf("schedule %d: estimate %.0f B/s eighteen seconds after the start; bulk %.0f B/s of the %.0f the lane serves", seed, estimate, o.bulk, served(lane))
		if estimate < 0.8*lane.rate {
			t.Errorf("schedule %d: estimate %.0f B/s on a lane of %.0f", seed, estimate, lane.rate)
		}
		if o.bulk < 0.75*served(lane) {
			t.Errorf("schedule %d: bulk received %.0f B/s", seed, o.bulk)
		}
	}
}

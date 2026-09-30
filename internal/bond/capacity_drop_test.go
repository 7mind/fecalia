package bond_test

import (
	"container/heap"
	"fmt"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

// changingLane offers saturating bulk over one lane whose rate and one-way
// delay change with time. It reports the payload delivered in each second and
// the longest wait for the link that a datagram sent in that second met.
func changingLane(t *testing.T, rate func(time.Duration) float64, delay func(time.Duration) time.Duration, seconds int) (delivered []float64, backlog []time.Duration) {
	t.Helper()
	start := time.Unix(100, 0)
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, p := range peers {
		p.SetRemote(peers[1-side].Epoch(), true)
	}
	queue := &events{}
	heap.Init(queue)
	var available [2]time.Time
	delivered, backlog = make([]float64, seconds), make([]time.Duration, seconds)
	const routerBuffer = 200 * time.Millisecond
	for tick := 0; tick < seconds*1000; tick++ {
		elapsed := time.Duration(tick) * time.Millisecond
		now := start.Add(elapsed)
		for _, p := range peers {
			p.Path(0, 0, 2*delay(0), now)
		}
		for side, p := range peers {
			if side == 0 && tick >= 1000 {
				for range 40 {
					if err := p.Enqueue(make([]byte, 1300), bond.PacketMetadata{Flow: bond.FlowID{4, 6, 1}}, now); err != nil {
						break
					}
				}
			}
			for _, tx := range p.Poll(now) {
				begin := maxTimeTest(now, available[side])
				if begin.Sub(now) > routerBuffer {
					continue
				}
				if side == 0 {
					backlog[tick/1000] = max(backlog[tick/1000], begin.Sub(now))
				}
				available[side] = begin.Add(time.Duration(float64(len(tx.Frame.Payload)+78) / rate(elapsed) * float64(time.Second)))
				heap.Push(queue, event{available[side].Add(delay(elapsed)), 1 - side, tx.Path, tx.Frame})
			}
		}
		for queue.Len() > 0 && !(*queue)[0].at.After(now) {
			e := heap.Pop(queue).(event)
			got, err := peers[e.to].Receive(e.path, e.frame, now)
			if err != nil {
				continue
			}
			if e.to == 1 {
				for _, d := range got {
					delivered[tick/1000] += float64(len(d.Payload))
				}
			}
		}
	}
	return delivered, backlog
}

func megabytes(bytesPerSecond []float64) []float64 {
	out := make([]float64, len(bytesPerSecond))
	for i, bytes := range bytesPerSecond {
		out[i] = float64(int(bytes/1e5)) / 10
	}
	return out
}

// The lane probes its capacity about once a second. What a change of the path
// does to it depends on where in that cycle it falls, so each change is tried
// at several points of it.
var changeTimes = []time.Duration{5000 * time.Millisecond, 5400 * time.Millisecond, 5800 * time.Millisecond, 6200 * time.Millisecond}

const (
	changeRunSeconds = 14
	changeSettled    = 11 // the first second by which the lane must have settled
	beforeChange     = 4  // a second of the steady state before any change
)

// A lane that lost part of its capacity queues, and the queue must not pass
// for a higher latency level: the target follows the capacity, and the path's
// queue goes. Delay found right after a probe was taken for the probe's queue
// for as long as it lasted: a lane that lost half its capacity while a probe
// drained kept its target at three quarters of the old capacity, and the
// path's buffer full.
func TestCapacityDropIsNotALevelShift(t *testing.T) {
	delay := func(time.Duration) time.Duration { return 20 * time.Millisecond }
	for _, remaining := range []float64{0.8, 0.5, 0.25} {
		for _, dropAt := range changeTimes {
			t.Run(fmt.Sprintf("%.0f%% left at %s", remaining*100, dropAt), func(t *testing.T) {
				t.Parallel()
				rate := func(elapsed time.Duration) float64 {
					if elapsed >= dropAt {
						return remaining * fastLaneRate
					}
					return fastLaneRate
				}
				delivered, backlog := changingLane(t, rate, delay, changeRunSeconds)
				t.Logf("delivered %v MB/s, link queue %v", megabytes(delivered), backlog)
				for second := changeSettled; second < changeRunSeconds; second++ {
					if backlog[second] > 60*time.Millisecond {
						t.Fatalf("a datagram waited %s for the link in second %d", backlog[second], second)
					}
					if delivered[second] < 0.75*remaining*delivered[beforeChange] {
						t.Fatalf("second %d delivered %.1f MB/s of the %.1f MB/s before", second, delivered[second]/1e6, delivered[beforeChange]/1e6)
					}
				}
			})
		}
	}
}

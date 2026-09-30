package bond_test

import (
	"container/heap"
	"encoding/binary"
	"math/rand/v2"
	"sort"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

// standbyModel is a slow low-latency lane beside a faster jittery one, as a
// standby satellite link beside a mobile link. Bulk is offered at 2.4 MB/s in
// each direction.
type standbyModel struct {
	seed uint64
	// bursts offers the bulk as 40 datagrams every 20 ms, as a sender limited
	// by its own window does, so the tunnel queue empties between them.
	bursts bool
	// fast is the faster lane's capacity in bytes per second.
	fast float64
	// loss is the share of datagrams the slow lane drops.
	loss float64
	// failAt and failFor, in milliseconds, take the fast lane down; failFor
	// zero keeps it up.
	failAt, failFor int
	duration        int
	// observe, when set, sees one endpoint every millisecond.
	observe func(tick int, endpoint *bond.Transport, now time.Time)
}

type voiceOutcome struct {
	sent int
	// waits holds the one-way delay of each voice datagram delivered.
	waits []time.Duration
	// uncopied counts voice datagrams transmitted once only.
	uncopied int
}

func (o voiceOutcome) later(than time.Duration) (count int) {
	for _, wait := range o.waits {
		if wait > than {
			count++
		}
	}
	return
}

// run offers saturating-rate bulk and two 50 Hz voice streams in both
// directions from three seconds on and reports on the voice sent from
// measureFrom, in milliseconds.
func (m standbyModel) run(t *testing.T, measureFrom int) voiceOutcome {
	t.Helper()
	rates := []float64{62500, m.fast}
	delays := []time.Duration{20 * time.Millisecond, 40 * time.Millisecond}
	jitters := []time.Duration{10 * time.Millisecond, 30 * time.Millisecond}
	const routerBufferDatagrams = 100
	start := time.Unix(100, 0)
	random := rand.New(rand.NewPCG(m.seed, 0))
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, p := range peers {
		p.SetRemote(peers[1-side].Epoch(), true)
	}
	queue := &events{}
	heap.Init(queue)
	available := make([][2]time.Time, len(rates))
	transmissions := map[int]int{}
	var outcome voiceOutcome
	for tick := 0; tick < m.duration+500; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		down := func(lane int) bool { return lane == 1 && tick >= m.failAt && tick < m.failAt+m.failFor }
		for _, p := range peers {
			for lane := range rates {
				// Probes stop confirming a failed path; its lease then expires.
				if !down(lane) {
					p.Path(bond.PathID(lane), bond.PathID(lane), 2*delays[lane], now)
				}
			}
		}
		for side, p := range peers {
			if tick >= 3000 && tick < m.duration {
				bulk := 2
				if m.bursts {
					bulk = 0
					if tick%20 == 0 {
						bulk = 40
					}
				}
				for range bulk {
					if err := p.Enqueue(make([]byte, 1200), bond.PacketMetadata{Flow: bond.FlowID{4, 6, 1}}, now); err != nil {
						t.Fatal(err)
					}
				}
				if tick%10 == 0 {
					voice := make([]byte, voiceWireGuardBytes)
					binary.BigEndian.PutUint64(voice, uint64(tick))
					if err := p.Enqueue(voice, bond.PacketMetadata{Flow: voiceFlow(byte(1 + tick/10%2))}, now); err != nil {
						t.Fatal(err)
					}
					if side == 0 && tick >= measureFrom {
						outcome.sent++
					}
				}
			}
			if side == 0 && m.observe != nil {
				m.observe(tick, p, now)
			}
			for _, tx := range p.Poll(now) {
				lane := int(tx.Path)
				if payload := tx.Frame.Payload; side == 0 && len(payload) > voiceWireGuardBytes && len(payload) < 2*voiceWireGuardBytes {
					transmissions[int(binary.BigEndian.Uint64(payload[len(payload)-voiceWireGuardBytes:]))]++
				}
				if down(lane) {
					continue
				}
				begin := maxTimeTest(now, available[lane][side])
				if begin.Sub(now) > time.Duration(routerBufferDatagrams*1300/rates[lane]*float64(time.Second)) {
					continue
				}
				available[lane][side] = begin.Add(time.Duration(float64(len(tx.Frame.Payload)+78) / rates[lane] * float64(time.Second)))
				if lane == 0 && random.Float64() < m.loss {
					continue
				}
				delay := delays[lane] + time.Duration(random.Int64N(int64(2*jitters[lane]))) - jitters[lane]
				heap.Push(queue, event{available[lane][side].Add(delay), 1 - side, tx.Path, tx.Frame})
			}
		}
		for queue.Len() > 0 && !(*queue)[0].at.After(now) {
			e := heap.Pop(queue).(event)
			if down(int(e.path)) {
				continue
			}
			got, err := peers[e.to].Receive(e.path, e.frame, now)
			if err != nil {
				// A frame in flight when the path's lease expired.
				continue
			}
			for _, d := range got {
				if e.to == 1 && len(d.Payload) == voiceWireGuardBytes {
					if at := int(binary.BigEndian.Uint64(d.Payload)); at >= measureFrom {
						outcome.waits = append(outcome.waits, time.Duration(tick-at)*time.Millisecond)
					}
				}
			}
		}
	}
	for at, count := range transmissions {
		if at >= measureFrom && count == 1 {
			outcome.uncopied++
		}
	}
	sort.Slice(outcome.waits, func(i, j int) bool { return outcome.waits[i] < outcome.waits[j] })
	return outcome
}

const lateVoice = 75 * time.Millisecond

// A voice datagram lost on its lane is repaired about 250 ms later unless a
// copy travelled on the other lane. The other lane must keep room for the
// copies: bulk saturating it filled its window and pacing slots, and four in ten voice
// datagrams went uncopied whatever the allowance (VM series of 2026-09-29:
// 0.7-3.5% of voice round trips above 150 ms at 10% and at 20%).
func TestVoiceCopiesHaveRoomOnTheOtherLane(t *testing.T) {
	// A copy on the slower lane arrives within its 70 ms and a queue; a
	// repair takes longer than this.
	const repaired = 150 * time.Millisecond
	outcome := standbyModel{seed: 1, fast: 156250, loss: 0.004, duration: 63000}.run(t, 8000)
	late := outcome.later(repaired)
	t.Logf("delivered %d/%d, %d uncopied, %d later than %s, longest %s", len(outcome.waits), outcome.sent, outcome.uncopied, late, repaired, outcome.waits[len(outcome.waits)-1])
	if outcome.uncopied > outcome.sent/20 {
		t.Errorf("%d of %d voice datagrams had no copy", outcome.uncopied, outcome.sent)
	}
	if len(outcome.waits) < outcome.sent*999/1000 || late > 0 {
		t.Errorf("delivered %d/%d, %d later than %s", len(outcome.waits), outcome.sent, late, repaired)
	}
}

// When the fast lane fails, the slow lane's capacity barely exceeds what voice
// needs, and the backlog formed at the failure drained over seconds: every
// datagram meanwhile arrived late by the backlog. The oldest must give way.
func TestVoiceCatchesUpAfterTakeover(t *testing.T) {
	var sent, delivered, late int
	for seed := uint64(1); seed <= 8; seed++ {
		outcome := standbyModel{seed: seed, fast: 12500000, failAt: 20000, failFor: 5000, duration: 27000}.run(t, 20000)
		t.Logf("seed %d: delivered %d/%d, %d later than %s", seed, len(outcome.waits), outcome.sent, outcome.later(lateVoice), lateVoice)
		sent += outcome.sent
		delivered += len(outcome.waits)
		late += outcome.later(lateVoice)
	}
	if delivered < sent*98/100 || late > sent/100 {
		t.Fatalf("delivered %d/%d, %d later than %s", delivered, sent, late, lateVoice)
	}
}

// The slow lane carries voice at about half its capacity. Its target must not
// drift above what the lane can carry: nothing it delivers is evidence for
// more (VM runs of 2026-09-29: targets of 81-97 kB/s on this 62.5 kB/s lane).
func TestLightlyLoadedLaneTargetStaysWithinCapacity(t *testing.T) {
	const capacity = 62500
	var highest float64
	model := standbyModel{seed: 1, fast: 12500000, bursts: true, duration: 60000}
	model.observe = func(tick int, endpoint *bond.Transport, now time.Time) {
		if tick >= 6000 && tick%100 == 0 {
			highest = max(highest, endpoint.Snapshot(now).Paths[0].Rate)
		}
	}
	model.run(t, 6000)
	t.Logf("highest target %.0f B/s on a %d B/s lane", highest, capacity)
	// A capacity pulse exceeds the estimate by a tenth, and the estimate
	// follows a delivery measurement spread by jitter; 63-71 kB/s over forty
	// runs, which differ by map iteration order.
	if highest > 1.2*capacity {
		t.Fatalf("target reached %.0f B/s on a %d B/s lane", highest, capacity)
	}
}

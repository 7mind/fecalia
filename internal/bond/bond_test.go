package bond_test

import (
	"container/heap"
	"encoding/binary"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
	"github.com/7mind/wanbond/internal/frame"
)

func TestLocalRestartRejectsOldDataAndRestartsSequence(t *testing.T) {
	now := time.Unix(100, 0)
	a, b := bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})
	a.SetRemote(b.Epoch(), true)
	b.SetRemote(a.Epoch(), true)
	a.Path(0, 0, time.Millisecond, now)
	b.Path(0, 0, time.Millisecond, now)
	if err := a.Enqueue(make([]byte, 1000), now); err != nil {
		t.Fatal(err)
	}
	old := a.Poll(now)[0]
	b = bond.New(bond.Epoch{Boot: 2, Generation: 2})
	b.SetRemote(a.Epoch(), true)
	b.Path(0, 0, time.Millisecond, now)
	if _, err := b.Receive(0, old.Frame, now); err == nil {
		t.Error("data for old receiver generation accepted after reopen")
	}
	a.SetRemote(b.Epoch(), true)
	a.Path(0, 0, time.Millisecond, now)
	if err := a.Enqueue(make([]byte, 1000), now); err != nil {
		t.Fatal(err)
	}
	var got []bond.Delivery
	for _, tx := range a.Poll(now.Add(20 * time.Millisecond)) {
		deliveries, err := b.Receive(0, tx.Frame, now.Add(20*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, deliveries...)
	}
	if len(got) == 0 || got[0].Sequence != 1 {
		t.Fatalf("new epoch pair must begin at sequence 1 (received %d deliveries)", len(got))
	}
}

func TestSlowLaneRepairSurvivesFastLaneProgress(t *testing.T) {
	now := time.Unix(100, 0)
	a, b := bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})
	a.SetRemote(b.Epoch(), true)
	b.SetRemote(a.Epoch(), true)
	a.Path(0, 0, time.Millisecond, now)
	b.Path(0, 0, time.Millisecond, now)
	b.Path(1, 1, time.Millisecond, now)
	if err := a.Enqueue(make([]byte, 1200), now); err != nil {
		t.Fatal(err)
	}
	first := a.Poll(now)[0].Frame
	for seq := uint64(2); seq <= 600; seq++ {
		f := first
		f.Seq = seq
		f.Payload = append([]byte(nil), first.Payload...)
		binary.BigEndian.PutUint16(f.Payload[17:], 1)
		binary.BigEndian.PutUint64(f.Payload[35:], seq)
		binary.BigEndian.PutUint64(f.Payload[43:], seq)
		if got, err := b.Receive(1, f, now.Add(50*time.Millisecond)); err != nil || len(got) != 1 {
			t.Fatalf("fast lane %d: %d deliveries, %v", seq, len(got), err)
		}
	}
	if got, err := b.Receive(0, first, now.Add(100*time.Millisecond)); err != nil || len(got) != 1 {
		t.Fatalf("slow lane packet lost within repair lifetime: %d deliveries, %v", len(got), err)
	}
	if got, err := b.Receive(0, first, now.Add(101*time.Millisecond)); err != nil || len(got) != 0 {
		t.Fatalf("slow lane replay: %d deliveries, %v", len(got), err)
	}
}

func TestMalformedDataDoesNotConsumeAttempt(t *testing.T) {
	now := time.Unix(100, 0)
	a, b := bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})
	a.SetRemote(b.Epoch(), true)
	b.SetRemote(a.Epoch(), true)
	a.Path(0, 0, time.Millisecond, now)
	b.Path(0, 0, time.Millisecond, now)
	if err := a.Enqueue([]byte("test"), now); err != nil {
		t.Fatal(err)
	}
	valid := a.Poll(now)[0].Frame
	malformed := valid
	malformed.Payload = append([]byte(nil), valid.Payload...)
	binary.BigEndian.PutUint64(malformed.Payload[35:], 0)
	if _, err := b.Receive(0, malformed, now); err == nil {
		t.Fatal("malformed keepalive accepted")
	}
	got, err := b.Receive(0, valid, now)
	if err != nil || len(got) != 1 {
		t.Fatalf("malformed frame consumed valid attempt: %v %v", got, err)
	}
}

func TestACKRejectsOverflowingDurations(t *testing.T) {
	now := time.Unix(100, 0)
	a, b := bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})
	a.SetRemote(b.Epoch(), true)
	b.SetRemote(a.Epoch(), true)
	a.Path(0, 0, time.Millisecond, now)
	b.Path(0, 0, time.Millisecond, now)
	if err := a.Enqueue([]byte("test"), now); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Receive(0, a.Poll(now)[0].Frame, now); err != nil {
		t.Fatal(err)
	}
	valid := b.Poll(now.Add(30 * time.Millisecond))[0].Frame
	for _, offset := range []int{59, 67} {
		malformed := valid
		malformed.Payload = append([]byte(nil), valid.Payload...)
		binary.BigEndian.PutUint64(malformed.Payload[offset:], ^uint64(0))
		if _, err := a.Receive(0, malformed, now.Add(30*time.Millisecond)); err == nil {
			t.Errorf("overflowing duration at %d accepted", offset)
		}
	}
	if _, err := a.Receive(0, valid, now.Add(30*time.Millisecond)); err != nil {
		t.Fatalf("malformed duration consumed valid ACK revision: %v", err)
	}
}

type event struct {
	at    time.Time
	to    int
	path  bond.PathID
	frame frame.Control
}

type events []event

func (e events) Len() int           { return len(e) }
func (e events) Less(i, j int) bool { return e[i].at.Before(e[j].at) }
func (e events) Swap(i, j int)      { e[i], e[j] = e[j], e[i] }
func (e *events) Push(v any)        { *e = append(*e, v.(event)) }
func (e *events) Pop() any          { old := *e; v := old[len(old)-1]; *e = old[:len(old)-1]; return v }

// BG: same transport on both ends; controllable bandwidth, propagation, outage
// and clock. The VM tier exercises the real UDP adapter and kernel queues.
func TestBidirectionalCapacityAndOutage(t *testing.T) {
	start := time.Unix(100, 0)
	a, b := bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})
	a.SetRemote(b.Epoch(), true)
	b.SetRemote(a.Epoch(), true)
	peers := []*bond.Transport{a, b}
	queue := &events{}
	heap.Init(queue)
	var available [2][2]time.Time
	var bytes [2]int
	var voices [2]int
	var lastVoice [2]time.Time
	var voiceGap [2]time.Duration
	type deliveryID struct {
		sequence    uint64
		interactive bool
	}
	seen := [2]map[deliveryID]bool{make(map[deliveryID]bool), make(map[deliveryID]bool)}
	bulk, voice := make([]byte, 1200), make([]byte, 160)
	for tick := 0; tick < 30000; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		outage := tick >= 18000 && tick < 21000
		if tick == 17000 {
			for i, peer := range peers {
				rate := peer.Snapshot(now).Paths[0].Rate
				if rate > 125000 {
					t.Errorf("peer %d did not reduce its target after 5 seconds at 0.5 Mbit/s: %.0f bytes/s", i, rate)
				}
			}
		}
		for i, p := range peers {
			for path := 0; path < 2; path++ {
				if tick%200 == 0 && !(path == 0 && outage) {
					p.Path(bond.PathID(path), bond.PathID(path), time.Duration(30+20*path)*time.Millisecond, now)
				}
			}
			if tick%20 == 0 {
				if err := p.Enqueue(voice, now); err != nil {
					t.Fatal(err)
				}
			}
			for n := 0; n < 2; n++ {
				if err := p.Enqueue(bulk, now); err != nil {
					t.Fatal(err)
				}
			}
			for _, tx := range p.Poll(now) {
				path := int(tx.Path)
				if path == 0 && outage {
					continue
				}
				rate := float64(250000 + 500000*path)
				if path == 0 && tick >= 12000 && tick < 17000 {
					rate = 62500
				}
				wire := len(tx.Frame.Payload) + 78
				begin := now
				if available[i][path].After(begin) {
					begin = available[i][path]
				}
				if begin.Sub(now) > 100*time.Millisecond {
					continue
				}
				available[i][path] = begin.Add(time.Duration(float64(wire) / rate * float64(time.Second)))
				heap.Push(queue, event{available[i][path].Add(time.Duration(15+10*path) * time.Millisecond), 1 - i, tx.Path, tx.Frame})
			}
		}
		for queue.Len() > 0 && !(*queue)[0].at.After(now) {
			e := heap.Pop(queue).(event)
			deliveries, err := peers[e.to].Receive(e.path, e.frame, now)
			if err != nil {
				continue
			}
			for _, d := range deliveries {
				if seen[e.to][deliveryID{d.Sequence, d.Interactive}] {
					continue
				}
				seen[e.to][deliveryID{d.Sequence, d.Interactive}] = true
				if len(d.Payload) == len(voice) {
					voices[e.to]++
					if !lastVoice[e.to].IsZero() && tick > 1000 {
						voiceGap[e.to] = max(voiceGap[e.to], now.Sub(lastVoice[e.to]))
					}
					lastVoice[e.to] = now
				}
				if tick >= 5000 && tick < 12000 {
					bytes[e.to] += len(d.Payload)
				}
			}
		}
	}
	for i, p := range peers {
		t.Logf("peer %d: %.2f Mbit/s, voices %d, max gap %s, stats %+v", i, float64(bytes[i])*8/7e6, voices[i], voiceGap[i], p.Snapshot(start.Add(30*time.Second)))
		if bytes[i] < 5_000_000 {
			t.Errorf("peer %d underused combined capacity: %d bytes/7s", i, bytes[i])
		}
		if voiceGap[i] > 150*time.Millisecond {
			t.Errorf("peer %d voice gap %s", i, voiceGap[i])
		}
	}
}

func TestEpochAndReplayIsolation(t *testing.T) {
	now := time.Unix(100, 0)
	a, b := bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})
	a.SetRemote(b.Epoch(), true)
	b.SetRemote(a.Epoch(), true)
	a.Path(0, 0, time.Millisecond, now)
	b.Path(0, 0, time.Millisecond, now)
	if err := a.Enqueue([]byte("test"), now); err != nil {
		t.Fatal(err)
	}
	tx := a.Poll(now)[0]
	got, err := b.Receive(0, tx.Frame, now)
	if err != nil || len(got) != 1 {
		t.Fatalf("delivery: %v %v", got, err)
	}
	got, err = b.Receive(0, tx.Frame, now)
	if err != nil || len(got) != 0 {
		t.Fatalf("replay delivered: %v %v", got, err)
	}
	ack := b.Poll(now.Add(30 * time.Millisecond))[0]
	restarted := bond.New(bond.Epoch{Boot: 1, Generation: 2})
	restarted.SetRemote(b.Epoch(), true)
	restarted.Path(0, 0, time.Millisecond, now)
	if err := restarted.Enqueue([]byte("new"), now); err != nil {
		t.Fatal(err)
	}
	restarted.Poll(now)
	if _, err := restarted.Receive(0, ack.Frame, now.Add(10*time.Millisecond)); err == nil {
		t.Fatal("old ACK accepted after local reopen")
	}
	if !b.SetRemote(restarted.Epoch(), false) {
		t.Fatal("new generation rejected")
	}
	b.Path(0, 0, time.Millisecond, now)
	if _, err := b.Receive(0, tx.Frame, now); err == nil {
		t.Fatal("old DATA accepted after remote reopen")
	}
	if b.SetRemote(a.Epoch(), true) {
		t.Fatal("generation rollback accepted")
	}
	if b.SetRemote(bond.Epoch{Boot: 3, Generation: 1}, false) {
		t.Fatal("unchallenged boot accepted")
	}
}

// BA regression: a capacity collapse must not erase the timing evidence needed
// to reduce the sender's rate merely because its ACK exceeded the repair timer.
func TestLateACKStillMeasuresCongestion(t *testing.T) {
	now := time.Unix(100, 0)
	a, b := bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})
	a.SetRemote(b.Epoch(), true)
	b.SetRemote(a.Epoch(), true)
	a.Path(0, 0, 30*time.Millisecond, now)
	b.Path(0, 0, 30*time.Millisecond, now)
	for _, step := range []struct{ send, arrival, ack time.Duration }{{0, 15 * time.Millisecond, 60 * time.Millisecond}, {100 * time.Millisecond, 200 * time.Millisecond, 245 * time.Millisecond}} {
		if err := a.Enqueue(make([]byte, 1200), now.Add(step.send)); err != nil {
			t.Fatal(err)
		}
		packet := a.Poll(now.Add(step.send))[0]
		if step.send > 0 {
			a.Poll(now.Add(step.send + 110*time.Millisecond))
			if err := a.Enqueue(make([]byte, 1200), now.Add(step.ack)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := b.Receive(0, packet.Frame, now.Add(step.arrival)); err != nil {
			t.Fatal(err)
		}
		ack := b.Poll(now.Add(step.arrival + 30*time.Millisecond))[0]
		if _, err := a.Receive(0, ack.Frame, now.Add(step.ack)); err != nil {
			t.Fatal(err)
		}
	}
	s := a.Snapshot(now.Add(245 * time.Millisecond)).Paths[0]
	if s.QueueDelay < 80*time.Millisecond || s.Rate >= 125000 {
		t.Fatalf("late delivery evidence was lost: %+v", s)
	}
}

// BA: a busy ACK stream must not change the delivery class of a small datagram.
// Pacing limits transmission; replication has its own bounded allowance.
func TestSmallDatagramClassSurvivesACKBurst(t *testing.T) {
	now := time.Unix(100, 0)
	a, b := bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})
	a.SetRemote(b.Epoch(), true)
	b.SetRemote(a.Epoch(), true)
	a.Path(0, 0, time.Millisecond, now)
	b.Path(0, 0, time.Millisecond, now)
	for i := 0; i < 40; i++ {
		if err := a.Enqueue(make([]byte, 160), now); err != nil {
			t.Fatal(err)
		}
	}
	delivered := 0
	for tick := 0; tick < 100; tick++ {
		at := now.Add(time.Duration(tick) * time.Millisecond)
		for _, tx := range a.Poll(at) {
			got, err := b.Receive(0, tx.Frame, at)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range got {
				delivered++
				if !d.Interactive {
					t.Fatal("ACK burst demoted small datagram to bulk receive ordering")
				}
			}
		}
		for _, tx := range b.Poll(at) {
			if _, err := a.Receive(0, tx.Frame, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	if delivered != 40 {
		t.Fatalf("delivered %d of 40", delivered)
	}
}

// BG regression: a short router queue drops excess traffic before queue delay
// reaches the target. Continuing ACK progress must not hide that congestion.
func TestCapacityWithShallowRouterBuffer(t *testing.T) {
	start := time.Unix(100, 0)
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, p := range peers {
		p.SetRemote(peers[1-side].Epoch(), true)
	}
	queue := &events{}
	heap.Init(queue)
	var available [2]time.Time
	bytes := 0
	steadySent, steadyDropped := 0, 0
	for tick := 0; tick < 5000; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		for _, p := range peers {
			p.Path(0, 0, 30*time.Millisecond, now)
		}
		if err := peers[0].Enqueue(make([]byte, 1200), now); err != nil {
			t.Fatal(err)
		}
		for side, p := range peers {
			for _, tx := range p.Poll(now) {
				begin := now
				if available[side].After(begin) {
					begin = available[side]
				}
				measured := side == 0 && tick >= 2000 && len(tx.Frame.Payload) > 1000
				if measured {
					steadySent++
				}
				if begin.Sub(now) > 5*time.Millisecond {
					if measured {
						steadyDropped++
					}
					continue
				}
				available[side] = begin.Add(time.Duration(float64(len(tx.Frame.Payload)+78) / 125000 * float64(time.Second)))
				heap.Push(queue, event{available[side].Add(15 * time.Millisecond), 1 - side, 0, tx.Frame})
			}
		}
		for queue.Len() > 0 && !(*queue)[0].at.After(now) {
			e := heap.Pop(queue).(event)
			deliveries, err := peers[e.to].Receive(0, e.frame, now)
			if err != nil {
				t.Fatal(err)
			}
			if e.to == 1 {
				for _, d := range deliveries {
					bytes += len(d.Payload)
				}
			}
		}
	}
	rate := peers[0].Snapshot(start.Add(5 * time.Second)).Paths[0].Rate
	t.Logf("shallow buffer: %d/%d steady datagrams dropped; target %.0f bytes/s; delivered %d bytes", steadyDropped, steadySent, rate, bytes)
	if steadySent == 0 || steadyDropped > steadySent/10 {
		t.Errorf("persistent shallow-buffer loss: %d of %d wire datagrams dropped", steadyDropped, steadySent)
	}
	if rate > 187500 || bytes < 400000 {
		t.Fatalf("1 Mbit/s shallow queue: target %.0f bytes/s, delivered %d bytes in 5s", rate, bytes)
	}
}

func TestPropagationDelayIncreaseDoesNotBecomePermanentCongestion(t *testing.T) {
	start := time.Unix(100, 0)
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, p := range peers {
		p.SetRemote(peers[1-side].Epoch(), true)
	}
	queue := &events{}
	heap.Init(queue)
	var available [2]time.Time
	bytes := 0
	for tick := 0; tick < 20000; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		for _, p := range peers {
			p.Path(0, 0, 30*time.Millisecond, now)
		}
		if err := peers[0].Enqueue(make([]byte, 1200), now); err != nil {
			t.Fatal(err)
		}
		for side, p := range peers {
			for _, tx := range p.Poll(now) {
				begin := now
				if available[side].After(begin) {
					begin = available[side]
				}
				if begin.Sub(now) > 100*time.Millisecond {
					continue
				}
				available[side] = begin.Add(time.Duration(float64(len(tx.Frame.Payload)+78) / 125000 * float64(time.Second)))
				delay := 15 * time.Millisecond
				if tick >= 3000 {
					delay = 40 * time.Millisecond
				}
				heap.Push(queue, event{available[side].Add(delay), 1 - side, 0, tx.Frame})
			}
		}
		for queue.Len() > 0 && !(*queue)[0].at.After(now) {
			e := heap.Pop(queue).(event)
			deliveries, err := peers[e.to].Receive(0, e.frame, now)
			if err != nil {
				t.Fatal(err)
			}
			if e.to == 1 && tick >= 15000 {
				for _, d := range deliveries {
					bytes += len(d.Payload)
				}
			}
		}
	}
	rate := peers[0].Snapshot(start.Add(20 * time.Second)).Paths[0].Rate
	if bytes < 350000 {
		t.Fatalf("1 Mbit/s after propagation increase: target %.0f bytes/s, delivered %d bytes in final 5s", rate, bytes)
	}
}

func TestDeliveryEstimateIgnoresACKArrivalCompression(t *testing.T) {
	start := time.Unix(100, 0)
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, p := range peers {
		p.SetRemote(peers[1-side].Epoch(), true)
	}
	queue := &events{}
	heap.Init(queue)
	var available [2]time.Time
	bytes := 0
	for tick := 0; tick < 5000; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		for _, p := range peers {
			p.Path(0, 0, 30*time.Millisecond, now)
		}
		if err := peers[0].Enqueue(make([]byte, 1200), now); err != nil {
			t.Fatal(err)
		}
		for side, p := range peers {
			for _, tx := range p.Poll(now) {
				begin := now
				if available[side].After(begin) {
					begin = available[side]
				}
				if begin.Sub(now) > 100*time.Millisecond {
					continue
				}
				available[side] = begin.Add(time.Duration(float64(len(tx.Frame.Payload)+78) / 125000 * float64(time.Second)))
				delay := 15 * time.Millisecond
				if side == 1 && tx.Frame.Seq%2 == 0 {
					delay += 8 * time.Millisecond
				}
				heap.Push(queue, event{available[side].Add(delay), 1 - side, 0, tx.Frame})
			}
		}
		for queue.Len() > 0 && !(*queue)[0].at.After(now) {
			e := heap.Pop(queue).(event)
			deliveries, err := peers[e.to].Receive(0, e.frame, now)
			if err != nil {
				t.Fatal(err)
			}
			if e.to == 1 && tick >= 4000 {
				for _, d := range deliveries {
					bytes += len(d.Payload)
				}
			}
		}
	}
	rate := peers[0].Snapshot(start.Add(5 * time.Second)).Paths[0].DeliveryRate
	actual := float64(bytes) * 1329 / 1200
	if rate < actual*0.9 || rate > actual*1.1 {
		t.Fatalf("ACK timing bias: estimate %.0f wire bytes/s, delivered %d payload bytes in final second", rate, bytes)
	}
}

func TestSmallPacketBacklogCanDiscoverCapacity(t *testing.T) {
	start := time.Unix(100, 0)
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, p := range peers {
		p.SetRemote(peers[1-side].Epoch(), true)
	}
	queue := &events{}
	heap.Init(queue)
	delivered := 0
	for tick := 0; tick < 5000; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		for _, p := range peers {
			p.Path(0, 0, 2*time.Millisecond, now)
		}
		for n := 0; n < 10; n++ {
			if err := peers[0].Enqueue(make([]byte, 160), now); err != nil {
				t.Fatal(err)
			}
		}
		for side, p := range peers {
			for _, tx := range p.Poll(now) {
				heap.Push(queue, event{now.Add(time.Millisecond), 1 - side, 0, tx.Frame})
			}
		}
		for queue.Len() > 0 && !(*queue)[0].at.After(now) {
			e := heap.Pop(queue).(event)
			got, err := peers[e.to].Receive(e.path, e.frame, now)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range got {
				delivered += len(d.Payload)
			}
		}
	}
	if delivered < 1_000_000 {
		t.Fatalf("small-packet traffic stayed at initial pacing ceiling: %d payload bytes in 5s", delivered)
	}
}

func TestReceivingFastBulkDoesNotStarveReverseVoice(t *testing.T) {
	start := time.Unix(100, 0)
	a, b := bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})
	a.SetRemote(b.Epoch(), true)
	b.SetRemote(a.Epoch(), true)
	a.Path(0, 0, 30*time.Millisecond, start)
	b.Path(0, 0, 30*time.Millisecond, start)
	if err := b.Enqueue(make([]byte, 1200), start); err != nil {
		t.Fatal(err)
	}
	template := b.Poll(start)[0].Frame
	peers := [2]*bond.Transport{a, b}
	queue := &events{}
	heap.Init(queue)
	var seq uint64
	voices := 0
	for tick := 0; tick < 500; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		a.Path(0, 0, 30*time.Millisecond, now)
		b.Path(0, 0, 30*time.Millisecond, now)
		for n := 0; n < 8; n++ {
			seq++
			f := template
			f.Seq = seq
			f.Payload = append([]byte(nil), template.Payload...)
			binary.BigEndian.PutUint64(f.Payload[35:], seq)
			binary.BigEndian.PutUint64(f.Payload[43:], seq)
			if _, err := a.Receive(0, f, now); err != nil {
				t.Fatal(err)
			}
		}
		if tick%20 == 0 {
			if err := a.Enqueue(make([]byte, 160), now); err != nil {
				t.Fatal(err)
			}
		}
		for side, p := range peers {
			for _, tx := range p.Poll(now) {
				if side == 0 && tx.Frame.ControlType == bond.DataType || side == 1 && tx.Frame.ControlType == bond.ACKType {
					heap.Push(queue, event{now.Add(15 * time.Millisecond), 1 - side, 0, tx.Frame})
				}
			}
		}
		for queue.Len() > 0 && !(*queue)[0].at.After(now) {
			e := heap.Pop(queue).(event)
			got, err := peers[e.to].Receive(0, e.frame, now)
			if err != nil {
				t.Fatal(err)
			}
			voices += len(got)
		}
	}
	if voices < 23 {
		t.Fatalf("bulk ACK traffic starved reverse voice: %d of 25 delivered", voices)
	}
}

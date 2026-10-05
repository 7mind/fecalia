//go:build adaptivepolicy

package bond_test

import (
	"bytes"
	"container/heap"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

// Reliability-Blackbox-Group: a proven alternate must recover a lost original within its lifetime.
func TestAdaptivePolicyBulkRecoveryUsesAlternateBeforeRepairExpires(t *testing.T) {
	const repairLifetime = 250 * time.Millisecond
	start := time.Unix(100, 0)
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, peer := range peers {
		peer.SetRemote(peers[1-side].Epoch(), true)
		if err := peer.Path(0, 0, 150*time.Millisecond, start); err != nil {
			t.Fatal(err)
		}
	}
	lost := bytes.Repeat([]byte{0x51}, 1200)
	if err := peers[0].Enqueue(lost, bond.PacketMetadata{}, start); err != nil {
		t.Fatal(err)
	}
	wire := &events{}
	heap.Init(wire)
	var delivered int
	var firstSubmission, recoveredAt time.Time
	var alternateProgress bool
	for tick := 0; tick <= 300; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		if tick == 20 {
			if err := peers[0].Enqueue(bytes.Repeat([]byte{0x72}, 1200), bond.PacketMetadata{}, now); err != nil {
				t.Fatal(err)
			}
		}
		if tick == 100 {
			for _, peer := range peers {
				if err := peer.Path(1, 1, 20*time.Millisecond, now); err != nil {
					t.Fatal(err)
				}
			}
			if err := peers[0].Enqueue(bytes.Repeat([]byte{0x73}, 160), bond.PacketMetadata{}, now); err != nil {
				t.Fatal(err)
			}
		}
		for side, peer := range peers {
			for _, tx := range poll(peer, now) {
				if side == 0 && firstSubmission.IsZero() && bytes.HasSuffix(tx.Frame.Payload, lost) {
					firstSubmission = now
				}
				if side == 0 && tx.Path == 0 && bytes.HasSuffix(tx.Frame.Payload, lost) {
					continue
				}
				delay := 75 * time.Millisecond
				if tx.Path == 1 {
					delay = 10 * time.Millisecond
				}
				heap.Push(wire, event{at: now.Add(delay), to: 1 - side, path: tx.Path, frame: tx.Frame})
			}
		}
		for wire.Len() > 0 && !(*wire)[0].at.After(now) {
			e := heap.Pop(wire).(event)
			items, err := peers[e.to].Receive(e.path, e.frame, now)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				if bytes.Equal(item.Payload, lost) {
					delivered++
					recoveredAt = now
				}
			}
		}
		if tick == 200 {
			state := peers[0].Snapshot(now).Paths[1]
			alternateProgress = state.Liveness == bond.LaneLive && state.ACKProgressKnown
		}
	}
	if !alternateProgress {
		t.Fatal("fixture did not establish physical ACK progress on the alternate before expiry")
	}
	if delivered != 1 {
		t.Fatalf("receiver got %d/1 lost bulk originals despite a proven alternate before the repair deadline; sender expired %d", delivered, peers[0].Snapshot(start.Add(300*time.Millisecond)).Expired)
	}
	if firstSubmission.IsZero() || recoveredAt.Sub(firstSubmission) >= repairLifetime {
		t.Fatalf("recovery took %s, must complete before %s", recoveredAt.Sub(firstSubmission), repairLifetime)
	}
	if expired := peers[0].Snapshot(start.Add(300 * time.Millisecond)).Expired; expired != 0 {
		t.Fatalf("successfully recovered original still expired: %d", expired)
	}
	t.Logf("recovered lost original after %s without expiry", recoveredAt.Sub(firstSubmission))
}

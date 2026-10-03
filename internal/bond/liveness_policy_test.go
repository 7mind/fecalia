//go:build adaptivepolicy

package bond_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

func TestAdaptivePolicy1ProgressFreshness(t *testing.T) {
	start := time.Unix(100, 0)
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, peer := range peers {
		peer.SetRemote(peers[1-side].Epoch(), true)
		if err := peer.Path(0, 0, 200*time.Millisecond, start); err != nil {
			t.Fatal(err)
		}
	}
	if err := peers[0].Enqueue(bytes.Repeat([]byte{0x71}, 1200), bond.PacketMetadata{}, start); err != nil {
		t.Fatal(err)
	}
	first := poll(peers[0], start)
	if len(first) != 1 {
		t.Fatalf("first submission contains %d transmissions, want one datagram", len(first))
	}
	rate := peers[0].Snapshot(start).Paths[0].Rate
	for tick := 10; tick <= 700; tick += 10 {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		if err := peers[0].Path(0, 0, 200*time.Millisecond, now); err != nil {
			t.Fatal(err)
		}
		poll(peers[0], now)
		state := peers[0].Snapshot(now).Paths[0]
		if tick == 50 && state.Liveness != "suspect" {
			t.Errorf("no progress for two sparse ACK intervals: liveness %q, want suspect", state.Liveness)
		}
		if tick == 700 {
			if state.Liveness != bond.LaneDead {
				t.Errorf("fresh hellos and no reported progress beyond the RTO: liveness %q, want dead", state.Liveness)
			}
			if state.Rate != rate {
				t.Errorf("silence changed the pacing estimate from %.0f to %.0f without delay or loss evidence", rate, state.Rate)
			}
		}
	}
	if _, err := peers[1].Receive(0, first[0].Frame, start.Add(750*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	acks := poll(peers[1], start.Add(775*time.Millisecond))
	if len(acks) == 0 {
		t.Fatal("receiver produced no progress report")
	}
	for _, ack := range acks {
		if _, err := peers[0].Receive(0, ack.Frame, start.Add(776*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	state := peers[0].Snapshot(start.Add(776 * time.Millisecond)).Paths[0]
	if state.ACKed == 0 {
		t.Fatal("receiver frames did not report a physical receipt")
	}
	if state.Liveness != bond.LaneLive {
		t.Errorf("fresh reported progress left liveness %q, want live", state.Liveness)
	}
}

func TestAdaptivePolicy1SuspectCopiesBeforeTheRepairTimer(t *testing.T) {
	start := time.Unix(100, 0)
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, peer := range peers {
		peer.SetRemote(peers[1-side].Epoch(), true)
		for lane := bond.PathID(0); lane < 2; lane++ {
			if err := peer.Path(lane, lane, time.Duration(20+20*lane)*time.Millisecond, start); err != nil {
				t.Fatal(err)
			}
		}
	}
	payload := bytes.Repeat([]byte{0x51}, 224)
	if err := peers[0].Enqueue(payload, bond.PacketMetadata{}, start); err != nil {
		t.Fatal(err)
	}
	var delivered int
	for tick := 0; tick <= 50; tick++ {
		for _, transmission := range poll(peers[0], start.Add(time.Duration(tick)*time.Millisecond)) {
			if transmission.Path == 0 {
				continue
			}
			items, err := peers[1].Receive(1, transmission.Frame, start.Add(time.Duration(tick+20)*time.Millisecond))
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				if !bytes.Equal(item.Payload, payload) {
					t.Fatal("cross-lane copy changed the encrypted datagram")
				}
				delivered++
			}
		}
	}
	if delivered != 1 {
		t.Errorf("receiver obtained %d copies by twice the peer's ACK cadence plus alternate transit, want one", delivered)
	}
}

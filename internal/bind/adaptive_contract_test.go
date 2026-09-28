package bind

import (
	"bytes"
	"io"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/telemetry"
)

type adaptiveHarness struct {
	send   func(int, []byte, PacketMetadata) error
	step   func()
	read   func(int) [][]byte
	outage func(bool)
}

// BG/BC dual contract: the same loss/recovery and exactly-once requirements run
// against the manually clocked transport and the real UDP Bind adapter.
func TestAdaptiveDeliveryContract(t *testing.T) {
	for name, factory := range map[string]func(*testing.T) adaptiveHarness{
		"memory": memoryAdaptiveHarness, "udp": udpAdaptiveHarness,
	} {
		t.Run(name, func(t *testing.T) {
			h := factory(t)
			for _, outage := range []bool{false, true, false} {
				h.outage(outage)
				seen := [2]map[byte]bool{make(map[byte]bool), make(map[byte]bool)}
				for seq := byte(1); seq <= 8; seq++ {
					size := 1024
					if seq%2 == 0 {
						size = 160
					}
					for side := range 2 {
						if err := h.send(side, bytes.Repeat([]byte{seq}, size), PacketMetadata{}); err != nil {
							t.Fatal(err)
						}
					}
				}
				for range 500 {
					h.step()
					for side := range 2 {
						for _, p := range h.read(side) {
							if len(p) == 0 || p[0] < 1 || p[0] > 8 || seen[side][p[0]] {
								t.Fatalf("duplicate or invalid payload: %x", p)
							}
							size := 1024
							if p[0]%2 == 0 {
								size = 160
							}
							if !bytes.Equal(p, bytes.Repeat(p[:1], size)) {
								t.Fatal("payload changed")
							}
							seen[side][p[0]] = true
						}
					}
				}
				if len(seen[0]) != 8 || len(seen[1]) != 8 {
					t.Fatalf("outage=%v delivered %d/%d of 8 each direction", outage, len(seen[0]), len(seen[1]))
				}
			}
		})
	}
}

func TestAdaptiveSmallFlowIsolation(t *testing.T) {
	for name, factory := range map[string]func(*testing.T) adaptiveHarness{
		"memory": memoryAdaptiveHarness, "udp": udpAdaptiveHarness,
	} {
		t.Run(name, func(t *testing.T) {
			h := factory(t)
			for range 1024 {
				if err := h.send(0, make([]byte, 80), PacketMetadata{Flow: FlowID{1}}); err != nil {
					t.Fatal(err)
				}
			}
			voice := bytes.Repeat([]byte{0x7f}, 224)
			if err := h.send(0, voice, PacketMetadata{Flow: FlowID{2}}); err != nil {
				t.Fatal(err)
			}
			for range 50 {
				h.step()
				for _, p := range h.read(1) {
					if bytes.Equal(p, voice) {
						return
					}
				}
			}
			t.Fatal("a small-packet burst from one flow prevented another flow from receiving priority service")
		})
	}
}

func TestAdaptiveCumulativeACKCoalescing(t *testing.T) {
	for name, factory := range map[string]func(*testing.T) adaptiveHarness{
		"memory": memoryAdaptiveHarness, "udp": udpAdaptiveHarness,
	} {
		t.Run(name, func(t *testing.T) {
			h := factory(t)
			const last = 200
			for seq := 1; seq <= last; seq++ {
				meta := PacketMetadata{Flow: FlowID{1}, ACK: TCPACK{Eligible: true, Sequence: 7, Acknowledgement: uint32(seq), Window: 4096}}
				if err := h.send(0, bytes.Repeat([]byte{byte(seq)}, 80), meta); err != nil {
					t.Fatal(err)
				}
			}
			for range 50 {
				h.step()
				for _, p := range h.read(1) {
					if bytes.Equal(p, bytes.Repeat([]byte{last}, 80)) {
						return
					}
				}
			}
			t.Fatal("superseded TCP acknowledgements delayed the latest cumulative acknowledgement")
		})
	}
}

func memoryAdaptiveHarness(t *testing.T) adaptiveHarness {
	now := time.Unix(100, 0)
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	var received [2][][]byte
	drop := false
	for side, p := range peers {
		p.SetRemote(peers[1-side].Epoch(), true)
		for lane := range 2 {
			p.Path(bond.PathID(lane), bond.PathID(lane), 30*time.Millisecond, now)
		}
	}
	return adaptiveHarness{
		send: func(side int, p []byte, meta PacketMetadata) error {
			return peers[side].Enqueue(p, bond.PacketMetadata{Flow: bond.FlowID(meta.Flow), ACK: bond.TCPACK(meta.ACK)}, now)
		},
		outage: func(value bool) { drop = value },
		read:   func(side int) [][]byte { out := received[side]; received[side] = nil; return out },
		step: func() {
			now = now.Add(time.Millisecond)
			for side, p := range peers {
				for lane := range 2 {
					if !drop || lane != 0 {
						p.Path(bond.PathID(lane), bond.PathID(lane), 30*time.Millisecond, now)
					}
				}
				for _, tx := range p.Poll(now) {
					if drop && tx.Path == 0 {
						continue
					}
					deliveries, err := peers[1-side].Receive(tx.Path, tx.Frame, now)
					if err != nil {
						t.Fatal(err)
					}
					for _, d := range deliveries {
						received[1-side] = append(received[1-side], d.Payload)
					}
				}
			}
		},
	}
}

func udpAdaptiveHarness(t *testing.T) adaptiveHarness {
	psk := testKey(t, 0x42)
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var peers [2]*Multipath
	var incoming [2]chan []byte
	var drop atomic.Bool
	for side := range 2 {
		paths := loopbackPaths(1 + side)
		if side == 1 {
			paths[1].SourceAddr = netip.MustParseAddr("127.0.0.2")
		}
		scheduler, probers, factory := concPeerWiring(t, paths, psk, uint64(side+100), telemetry.SystemClock{})
		m, err := NewMultipath(paths, psk, scheduler, probers, factory, nil, nil, config.Amnezia{}, lg)
		if err != nil {
			t.Fatal(err)
		}
		if err = m.EnableAdaptive(); err != nil {
			t.Fatal(err)
		}
		receive, _, err := m.Open(0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = m.Close() })
		peers[side] = m
		incoming[side] = make(chan []byte, 64)
		for _, ps := range m.paths {
			ps.writeUDP = func(raw []byte, to netip.AddrPort) (int, error) {
				blocked := side == 1 && ps.id == 0
				if side == 0 {
					blocked = to.Addr() == netip.MustParseAddr("127.0.0.1")
				}
				if drop.Load() && blocked {
					return len(raw), nil
				}
				return ps.conn.WriteToUDPAddrPort(raw, to)
			}
		}
		go func() {
			for {
				packets, sizes, endpoints := [][]byte{make([]byte, 2048)}, make([]int, 1), make([]Endpoint, 1)
				if _, err := receive[0](packets, sizes, endpoints); err != nil {
					return
				}
				if endpoints[0] != m.virt {
					incoming[side] <- nil
					return
				}
				incoming[side] <- append([]byte(nil), packets[0][:sizes[0]]...)
			}
		}()
	}
	peers[1].SetPeerRemote(peers[0].paths[0].conn.LocalAddr().(*net.UDPAddr).AddrPort())
	for _, p := range peers {
		t.Cleanup(p.StartProbeLoop(20 * time.Millisecond))
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		ready := true
		for _, p := range peers {
			if len(p.PeerSnapshots()[0].Adaptive.Paths) != 2 {
				ready = false
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("adaptive lane negotiation did not converge")
		}
		time.Sleep(time.Millisecond)
	}
	return adaptiveHarness{
		send: func(side int, p []byte, meta PacketMetadata) error {
			return peers[side].SendWithMetadata([][]byte{p}, []PacketMetadata{meta}, peers[side].virt, func() {})
		},
		outage: drop.Store,
		step:   func() { time.Sleep(time.Millisecond) },
		read: func(side int) [][]byte {
			var out [][]byte
			for {
				select {
				case p := <-incoming[side]:
					out = append(out, p)
				default:
					return out
				}
			}
		},
	}
}

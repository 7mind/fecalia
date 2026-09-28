package bind

import (
	"net/netip"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/reseq"
)

func TestReceiveDrainsReadyBatch(t *testing.T) {
	m, err := newMultipath(t, loopbackPaths(1), testKey(t, 0x45))
	if err != nil {
		t.Fatal(err)
	}
	receivers, _, err := m.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	const count = 8
	packets := make([][]byte, count)
	sizes := make([]int, count)
	endpoints := make([]Endpoint, count)
	src := netip.MustParseAddrPort("192.0.2.1:51820")
	for i := range packets {
		packets[i] = make([]byte, 1500)
		m.peerState.resequencer.Load().ObserveFromPath(uint64(i), []byte{byte(i)}, src, 0)
	}
	n, err := receivers[0](packets, sizes, endpoints)
	if err != nil || n != count {
		t.Fatalf("ready receive batch returned %d packets, want %d: %v", n, count, err)
	}
	for i := range packets {
		if sizes[i] != 1 || packets[i][0] != byte(i) || endpoints[i] != m.peerState.virt {
			t.Errorf("packet %d lost order or peer identity: size=%d data=%v endpoint=%v", i, sizes[i], packets[i][:sizes[i]], endpoints[i])
		}
	}
}

func TestAdaptiveReceiveCoalescesBulkAndFlushesInteractive(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "bulk", true: "interactive"}[interactive], func(t *testing.T) {
			clock := newFakeClock()
			m, _, _ := newProbingMultipath(t, loopbackPaths(1), testKey(t, 0x46), clock)
			if err := m.EnableAdaptive(); err != nil {
				t.Fatal(err)
			}
			receivers, _, err := m.Open(0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Close() })
			start := clock.Now()
			src := netip.MustParseAddrPort("192.0.2.1:51820")
			rq := m.peerState.resequencer.Load()
			rq.ObserveFromPath(1, []byte{1}, src, 0)
			injected := false
			m.beforeReceivePark = func(deadline time.Time) {
				if injected || deadline != start.Add(4*time.Millisecond) {
					t.Fatalf("unexpected receive deadline: %v, injected=%v", deadline, injected)
				}
				injected = true
				if interactive {
					a := m.adaptive.Load()
					a.interactive <- reseq.Item{Payload: []byte{2}, Src: src}
					a.notify()
				} else {
					rq.ObserveFromPath(2, []byte{2}, src, 0)
					clock.advance(4 * time.Millisecond)
				}
			}
			packets := [][]byte{make([]byte, 1500), make([]byte, 1500), make([]byte, 1500)}
			sizes, endpoints := make([]int, len(packets)), make([]Endpoint, len(packets))
			n, err := receivers[0](packets, sizes, endpoints)
			if err != nil || n != 2 || !injected {
				t.Fatalf("burst split before coalescing: packets=%d injected=%v error=%v", n, injected, err)
			}
			if interactive && clock.Now() != start {
				t.Fatal("interactive delivery waited for bulk timer")
			}
		})
	}
}

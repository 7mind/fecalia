package bind

import (
	"net"
	"testing"
	"time"
)

func TestSharedSocketAccountsReceivedBytesToResolvedPeer(t *testing.T) {
	keyA, keyB := testKey(t, 0x11), testKey(t, 0x22)
	m, _, second := twoPeerConcentrator(t, keyA, keyB)
	remote, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	clock := newFakeClock()
	path := peerPathByName(second, "a")
	var expected uint64
	for seq := uint64(1); seq <= 2; seq++ {
		raw := authProbe(t, keyB, path.id, seq, clock)
		expected += uint64(len(raw))
		if _, err := remote.WriteToUDP(raw, m.paths[0].conn.LocalAddr().(*net.UDPAddr)); err != nil {
			t.Fatal(err)
		}
		if err := remote.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := remote.ReadFromUDP(make([]byte, 1500)); err != nil {
			t.Fatalf("wait for authenticated probe response: %v", err)
		}
		snapshots := m.PeerSnapshots()
		if got := snapshots[1].Paths[0].RxBytes; got != expected {
			t.Fatalf("peer B received %d bytes, counter is %d; primary counter is %d", expected, got, snapshots[0].Paths[0].RxBytes)
		}
		if got := snapshots[0].Paths[0].RxBytes; got != 0 {
			t.Fatalf("peer B's traffic was charged to primary peer: %d bytes", got)
		}
	}
}

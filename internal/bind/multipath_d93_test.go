package bind

import (
	"net"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/frame"
)

// d93Harness opens a single-path Multipath and returns its engine receive func
// plus a persistent client socket aimed at the path — the fixture for the D93
// wiring tests: one local socket, one outer source, sender-controlled OuterSeq
// and frame PathID per datagram.
func d93Harness(t *testing.T) (fn ReceiveFunc, send func(outerSeq uint64, pathID uint8, payload []byte)) {
	t.Helper()
	psk := testKey(t, 0xD9)
	m, err := newMultipath(t, loopbackPaths(1), psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	fns, _, err := m.Open(0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	dst := m.paths[0].conn.LocalAddr().(*net.UDPAddr)
	cl, err := net.DialUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")}, dst)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatalf("new codec: %v", err)
	}
	send = func(outerSeq uint64, pathID uint8, payload []byte) {
		wire, err := codec.Encode(nil, frame.Data{OuterSeq: outerSeq, PathID: pathID, Payload: payload})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if _, err := cl.Write(wire); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return fns[0], send
}

// recvOne blocks on fn until one frame is delivered and returns the elapsed time
// since start.
func recvOne(t *testing.T, fn ReceiveFunc, start time.Time) time.Duration {
	t.Helper()
	bufs := [][]byte{make([]byte, 2048)}
	sizes := make([]int, 1)
	eps := make([]Endpoint, 1)
	n, err := fn(bufs, sizes, eps)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if n != 1 {
		t.Fatalf("receive: n=%d, want 1", n)
	}
	return time.Since(start)
}

// TestSingleActivePathGapReleasesImmediately is the T241 wiring assertion for the
// D93 fix, end-to-end at the bind layer: on ONE delivering path (the active-backup
// steady state) a head-of-line gap is genuine loss, so the successor frame must be
// delivered with ~0 hold — NOT after the 250 ms resequencerTimeout. RED before the
// dispatchInbound call-site swap to ObserveFromPath (the legacy Observe ingests
// with unknown path identity, forcing the conservative full hold).
func TestSingleActivePathGapReleasesImmediately(t *testing.T) {
	fn, send := d93Harness(t)

	// Pin the release point at seq 0 (delivered), constant frame PathID 1.
	send(0, 1, []byte("seed"))
	_ = recvOne(t, fn, time.Now())

	// seq 1 is dropped by the "path"; seq 2 arrives on the same single path.
	start := time.Now()
	send(2, 1, []byte("successor"))
	elapsed := recvOne(t, fn, start)
	// The 250 ms hold is the RED signature; allow generous scheduling slack while
	// staying far below the hold cap.
	if elapsed >= 150*time.Millisecond {
		t.Fatalf("single-path gap successor delivered after %v; want ~0 hold (immediate release), not the %v cap (D93 wiring not engaged)", elapsed, 250*time.Millisecond)
	}
}

// TestSameSrcInterleavedPathIDsStillHeld is the R250 regression at the bind
// layer: ONE local socket and ONE outer source, but DATA frames interleaving two
// DISTINCT sender-stamped frame PathIDs — the concentrator-uplink topology where
// only the frame PathID discriminates the edge's WANs. The composite pathKey must
// see two delivering paths, so the gap is held the full resequencerTimeout, not
// fast-released (which would drop a genuine cross-WAN straggler as late).
func TestSameSrcInterleavedPathIDsStillHeld(t *testing.T) {
	fn, send := d93Harness(t)

	send(0, 1, []byte("seed"))
	_ = recvOne(t, fn, time.Now())

	// The successor arrives stamped with a DIFFERENT frame PathID (a second edge
	// WAN sharing the same socket+src): the resequencer must keep the full hold.
	start := time.Now()
	send(2, 2, []byte("successor"))
	elapsed := recvOne(t, fn, start)
	if elapsed < 200*time.Millisecond {
		t.Fatalf("same-src interleaved-PathID gap released after %v; want the full ~250ms hold (two delivering paths must not fast-release)", elapsed)
	}
}

package bind

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// newAlwaysUpScheduler builds a trivial single-path AlwaysUp active-backup scheduler for a
// concentrator peer fixture (D44 fan-out test): Pick always resolves to the peer's own sole
// path without requiring a real prober/liveness bring-up.
func newAlwaysUpScheduler(t testing.TB) sched.Scheduler {
	t.Helper()
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	scheduler, err := sched.NewActiveBackup([]sched.PathHealth{sched.AlwaysUp{}}, sched.Config{FailbackAfter: time.Second}, telemetry.SystemClock{}, lg)
	if err != nil {
		t.Fatalf("build scheduler: %v", err)
	}
	return scheduler
}

// recvParity reads until the staged group exposes its parity frame. It returns
// the decoded frame plus the raw bytes, so a caller can additionally
// attempt (and expect to fail) decoding the same raw bytes under a DIFFERENT peer's codec —
// the cross-psk isolation check.
func recvParity(t testing.TB, conn *net.UDPConn, codec *frame.Codec, wantDataCount int) (frame.Parity, []byte) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, maxDatagram)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("read parity: %v", err)
		}
		raw := append([]byte(nil), buf[:n]...)
		fr, err := codec.Decode(raw)
		if err != nil {
			t.Fatalf("decode parity: %v", err)
		}
		par, ok := fr.(frame.Parity)
		if !ok {
			continue
		}
		if int(par.DataCount) != wantDataCount {
			t.Fatalf("parity DataCount = %d, want %d", par.DataCount, wantDataCount)
		}
		return par, raw
	}
}

// TestFECFlushDeadlineFansOutAcrossPeers is the D44 regression: each peer owner must close and
// flush its OWN partial FEC group on the exact deadline, not just the embedded primary. Two
// concentrator peers (A, the primary, and B) each admit a PARTIAL group (fewer than
// DataShards); one fecFlushDeadline() call must emit parity for BOTH — each decodable ONLY
// under its own peer's psk-derived codec — while a THIRD, torn-down peer (C, also primed with a
// partial group before teardown) is nil-skipped without disturbing A's or B's flush.
//
// On pre-fix code this fails at the peer-B assertion: fecFlushDeadline read only
// m.fecSend/m.scheduler/m.paths (the embedded primary, via promotion), so peer B's partial
// group received no parity on this tick and would have had to wait for a full size-triggered
// close — the straggler-parity loss D44 describes.
func TestFECFlushDeadlineFansOutAcrossPeers(t *testing.T) {
	const dataShards, parityShards = 4, 1
	fecCfg := &fec.Config{DataShards: dataShards, ParityShards: parityShards, Deadline: testFECDeadline}

	pskA := testKey(t, 0x61)
	pskB := testKey(t, 0x62)
	pskC := testKey(t, 0x63)

	m := newMultipathFEC(t, loopbackPaths(1), pskA, fecCfg)

	if err := m.AddConcentratorPeer("peer-b", pskB, newAlwaysUpScheduler(t), nil, nil); err != nil {
		t.Fatalf("AddConcentratorPeer B: %v", err)
	}
	if err := m.AddConcentratorPeer("peer-c", pskC, newAlwaysUpScheduler(t), nil, nil); err != nil {
		t.Fatalf("AddConcentratorPeer C: %v", err)
	}

	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	peerA := m.peerState
	peerB := m.peersByName["peer-b"]
	peerC := m.peersByName["peer-c"]

	rawA, rawAAddr := rawPeer(t)
	rawB, rawBAddr := rawPeer(t)
	rawC, rawCAddr := rawPeer(t)

	peerPathByName(peerA, "a").setRemote(rawAAddr)
	peerPathByName(peerB, "a").setRemote(rawBAddr)
	peerPathByName(peerC, "a").setRemote(rawCAddr)

	// Tear peer C down before A/B open their groups. A dormant peer's absent owner
	// must not disturb either live owner's independent deadline.
	if !m.TearDownPeer("peer-c") {
		t.Fatal("TearDownPeer refused to tear down peer C")
	}
	if peerC.fecSend.Load() != nil {
		t.Fatal("peer C's FEC send plane survived teardown")
	}

	send := func(peer *peerState, tag byte) <-chan error {
		done := make(chan error, 1)
		payloads := make([][]byte, dataShards-1)
		for i := range payloads {
			payloads[i] = []byte{tag, byte(i)}
		}
		go func() {
			done <- m.Send(payloads, peer.virt)
		}()
		return done
	}
	sendA := send(peerA, 0xA0)
	sendB := send(peerB, 0xB0)
	for name, done := range map[string]<-chan error{"A": sendA, "B": sendB} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("send peer %s: %v", name, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("send peer %s did not close on its deadline", name)
		}
	}

	codecA, err := frame.NewCodec(pskA)
	if err != nil {
		t.Fatalf("codec A: %v", err)
	}
	codecB, err := frame.NewCodec(pskB)
	if err != nil {
		t.Fatalf("codec B: %v", err)
	}

	// Peer A (primary) and peer B each got their own partial group flushed on this one tick.
	parA, _ := recvParity(t, rawA, codecA, dataShards-1)
	parB, rawBBytes := recvParity(t, rawB, codecB, dataShards-1)

	if parA.PathID != peerPathByName(peerA, "a").id {
		t.Fatalf("peer A parity PathID = %d, want %d", parA.PathID, peerPathByName(peerA, "a").id)
	}
	if parB.PathID != peerPathByName(peerB, "a").id {
		t.Fatalf("peer B parity PathID = %d, want %d", parB.PathID, peerPathByName(peerB, "a").id)
	}

	// Cross-psk isolation (T84): peer B's parity must NOT decode under peer A's codec into a
	// frame matching peer B's actual emitted parity. Parity frames carry NO MAC by design
	// (internal/frame/frame.go), so codecA.Decode(rawBBytes) can occasionally garble the kind
	// byte to a value that lands on KindParity (~1/256 of the time) and parses without error —
	// that accidental garbage-decode is the designed behaviour of an unauthenticated frame
	// kind, not a cross-psk isolation violation. Only a decode that lands on a frame.Parity
	// whose semantic fields (group/index/count/path/payload) actually MATCH peer B's real
	// parity would demonstrate that codec A can read peer B's traffic — see perpsk_test.go:57
	// for the analogous "err==nil but not the same content" idiom.
	if frB, err := codecA.Decode(rawBBytes); err == nil {
		if got, ok := frB.(frame.Parity); ok &&
			got.FECGroup == parB.FECGroup &&
			got.ParityIndex == parB.ParityIndex &&
			got.DataCount == parB.DataCount &&
			got.PathID == parB.PathID &&
			bytes.Equal(got.Payload, parB.Payload) {
			t.Fatal("peer B's parity decoded under peer A's codec as a matching frame.Parity — cross-psk isolation violated")
		}
	}

	// The torn-down peer C is skipped WITHOUT disturbing the others: its own remote never
	// receives anything for this tick.
	if err := rawC.SetReadDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, maxDatagram)
	if n, _, err := rawC.ReadFromUDP(buf); err == nil {
		t.Fatalf("torn-down peer C unexpectedly received a %d-byte frame — teardown was not respected by the flush", n)
	}
}

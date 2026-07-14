package bind

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// newMultipathFEC builds a Multipath over paths with a default all-Up active-backup
// scheduler (selection reduces to path 0) and the given FEC configuration — the T24
// datapath under test. It mirrors newMultipath but threads fecCfg through.
func newMultipathFEC(t testing.TB, paths []config.Path, psk config.Key, fecCfg *fec.Config) *Multipath {
	t.Helper()
	health := make([]sched.PathHealth, len(paths))
	for i := range health {
		health[i] = sched.AlwaysUp{}
	}
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	scheduler, err := sched.NewActiveBackup(health, sched.Config{FailbackAfter: time.Second}, telemetry.SystemClock{}, lg)
	if err != nil {
		t.Fatalf("build scheduler: %v", err)
	}
	m, err := NewMultipath(paths, psk, scheduler, nil, nil, fecCfg, nil, config.Amnezia{}, lg)
	if err != nil {
		t.Fatalf("NewMultipath(fec): %v", err)
	}
	return m
}

// capturedFrame is one outer datagram the sender wrote, kept both raw (to replay
// verbatim into a receiver's handleInbound) and decoded (to classify DATA vs PARITY
// and read its FEC coordinates).
type capturedFrame struct {
	raw []byte
	fr  frame.Frame
}

// sendAndCapture drives sender.Send over payloads in batches of groupSize (one Send
// call per FEC group), then drains EVERY outer datagram the sender egressed onto the
// peer socket and decodes it. The sender's single path must already point at peer.
// Sending a whole group's payloads in ONE Send admits them under a single m.mu hold, so
// the group fills (size-close) atomically and the async deadline-tick — which only ever
// TryLocks m.mu — can never split a group mid-capture: captures are deterministic even
// with a realistic (sub-resequencer-timeout) FEC deadline. It reads until a short
// deadline with no further datagram, so it captures the DATA frames plus the FEC PARITY
// each group-fill emitted.
func sendAndCapture(t testing.TB, sender *Multipath, peer *net.UDPConn, codec *frame.Codec, payloads [][]byte, groupSize int) []capturedFrame {
	t.Helper()
	for i := 0; i < len(payloads); i += groupSize {
		end := i + groupSize
		if end > len(payloads) {
			end = len(payloads)
		}
		if err := sender.Send(payloads[i:end], sender.virt); err != nil {
			t.Fatalf("send batch [%d:%d): %v", i, end, err)
		}
	}
	var out []capturedFrame
	buf := make([]byte, maxDatagram)
	if err := peer.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	for {
		n, _, err := peer.ReadFromUDPAddrPort(buf)
		if err != nil {
			break // deadline: the sender wrote nothing more
		}
		raw := append([]byte(nil), buf[:n]...)
		fr, derr := codec.Decode(raw)
		if derr != nil {
			t.Fatalf("decode captured datagram: %v", derr)
		}
		out = append(out, capturedFrame{raw: raw, fr: fr})
	}
	return out
}

// drainExact collects exactly want inner datagrams from the engine-facing ReceiveFunc,
// returning early (with fewer) if timeout elapses or the bind closes. A single
// goroutine is the sole caller of fn (the ReceiveFunc's single-consumer contract); it
// is retired once want items are gathered.
func drainExact(t testing.TB, fn ReceiveFunc, want int, timeout time.Duration) [][]byte {
	t.Helper()
	type item struct {
		p   []byte
		err error
	}
	ch := make(chan item, want+8)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			bufs := [][]byte{make([]byte, maxDatagram)}
			sizes := make([]int, 1)
			eps := make([]Endpoint, 1)
			n, err := fn(bufs, sizes, eps)
			if err != nil {
				ch <- item{err: err}
				return
			}
			if n == 1 {
				ch <- item{p: append([]byte(nil), bufs[0][:sizes[0]]...)}
			}
		}
	}()
	out := make([][]byte, 0, want)
	deadline := time.After(timeout)
	for len(out) < want {
		select {
		case it := <-ch:
			if it.err != nil {
				return out
			}
			out = append(out, it.p)
		case <-deadline:
			close(done)
			return out
		}
	}
	close(done)
	return out
}

// testFECDeadline is a realistic group-close deadline for the bind FEC tests: well
// below the datapath bound (maxFECDeadline, half the resequencer timeout), so the
// configuration is one NewMultipath accepts. Determinism does not rely on the deadline
// being large — sendAndCapture fills each group atomically in one Send — so a
// production-shaped value is used.
const testFECDeadline = 50 * time.Millisecond

// payloadStream returns n distinct, non-trivial inner datagrams so a transparency /
// ordering assertion cannot pass by accident (each payload is unique).
func payloadStream(n int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = []byte(fmt.Sprintf("inner-wireguard-datagram-#%04d-payload", i))
	}
	return out
}

// TestMultipathFECTransparentAndOverhead proves the 0-loss send side: with FEC on,
// every inner payload rides a DATA frame UNCHANGED (transparency), the DATA frames
// carry contiguous outer-seqs and correct FEC group/shard coordinates, and each full
// group emits exactly ParityShards parity frames — so the parity overhead equals the
// configured ratio and the /metrics parity counters reflect it.
func TestMultipathFECTransparentAndOverhead(t *testing.T) {
	const (
		dataShards   = 4
		parityShards = 2
		groups       = 3
	)
	psk := testKey(t, 0x24)
	fecCfg := &fec.Config{DataShards: dataShards, ParityShards: parityShards, Deadline: testFECDeadline}
	sender := newMultipathFEC(t, loopbackPaths(1), psk, fecCfg)
	if _, _, err := sender.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = sender.Close() })

	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen peer: %v", err)
	}
	defer peer.Close()
	sender.paths[0].setRemote(peer.LocalAddr().(*net.UDPAddr).AddrPort())

	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatalf("codec: %v", err)
	}
	payloads := payloadStream(dataShards * groups)
	captured := sendAndCapture(t, sender, peer, codec, payloads, dataShards)

	var data []frame.Data
	var parity []frame.Parity
	for _, c := range captured {
		switch f := c.fr.(type) {
		case frame.Data:
			data = append(data, f)
		case frame.Parity:
			parity = append(parity, f)
		default:
			t.Fatalf("unexpected captured frame kind %T", c.fr)
		}
	}

	if len(data) != dataShards*groups {
		t.Fatalf("captured %d DATA frames, want %d", len(data), dataShards*groups)
	}
	if len(parity) != parityShards*groups {
		t.Fatalf("captured %d PARITY frames, want %d (K=%d per group, %d groups)", len(parity), parityShards*groups, parityShards, groups)
	}
	// Overhead ratio: parity/data == M/K exactly (the configured fixed ratio).
	if len(parity)*dataShards != len(data)*parityShards {
		t.Fatalf("overhead ratio mismatch: %d parity / %d data != %d/%d", len(parity), len(data), parityShards, dataShards)
	}

	// Transparency + coordinates: the i-th DATA frame carries the i-th payload UNCHANGED
	// (data frames are captured in send order), a contiguous outer-seq (1-based), and the
	// expected FEC group/shard index.
	for i, d := range data {
		if !bytes.Equal(d.Payload, payloads[i]) {
			t.Fatalf("DATA %d payload = %q, want %q (FEC must be transparent)", i, d.Payload, payloads[i])
		}
		if d.OuterSeq != uint64(i+1) {
			t.Fatalf("DATA %d outer-seq = %d, want %d (contiguous)", i, d.OuterSeq, i+1)
		}
		wantGroup := uint32(i / dataShards)
		wantIndex := uint8(i % dataShards)
		if d.FECGroup != wantGroup || d.FECIndex != wantIndex {
			t.Fatalf("DATA %d FEC coords = (group %d, index %d), want (%d, %d)", i, d.FECGroup, d.FECIndex, wantGroup, wantIndex)
		}
	}
	// Parity frames carry the group cardinality M so the decoder can group them.
	for _, p := range parity {
		if p.DataCount != dataShards {
			t.Fatalf("PARITY group %d carries DataCount %d, want %d", p.FECGroup, p.DataCount, dataShards)
		}
	}

	snap := sender.PeerSnapshots()[0].FEC
	if snap.DataFrames != uint64(dataShards*groups) {
		t.Fatalf("PeerSnapshots FEC.DataFrames = %d, want %d (the fixed-ratio overhead denominator)", snap.DataFrames, dataShards*groups)
	}
	if snap.ParityFrames != uint64(parityShards*groups) {
		t.Fatalf("PeerSnapshots FEC.ParityFrames = %d, want %d", snap.ParityFrames, parityShards*groups)
	}
	// The /metrics overhead ratio the P3 e2e asserts is ParityFrames/DataFrames; on full
	// groups it equals the configured M/K exactly.
	if snap.ParityFrames*uint64(dataShards) != snap.DataFrames*uint64(parityShards) {
		t.Fatalf("overhead ratio ParityFrames/DataFrames = %d/%d != %d/%d (M/K)", snap.ParityFrames, snap.DataFrames, parityShards, dataShards)
	}
	if snap.ParityBytes == 0 {
		t.Fatal("PeerSnapshots FEC.ParityBytes = 0, want > 0 (parity has wire cost)")
	}
	if snap.Recovered != 0 || snap.Unrecoverable != 0 {
		t.Fatalf("send-only path recovered/unrecoverable = %d/%d, want 0/0", snap.Recovered, snap.Unrecoverable)
	}
}

// feedInbound replays a captured frame into rx's receive path exactly as an arriving
// datagram would, so the FEC decode + resequencing integration is exercised end to end
// under deterministic, hand-chosen drops.
func feedInbound(rx *Multipath, c capturedFrame, src netip.AddrPort) {
	rx.handleInbound(rx.paths[0], c.raw, src)
}

// TestMultipathFECRecoversWithinBudget is the core acceptance test: a receive stream
// dropping <= ParityShards DATA frames per group reconstructs the missing frames from
// parity and delivers the FULL, ORDERED inner payload set to WG, and the recovery
// counter advances by exactly the number of frames rebuilt. It is a mutation witness:
// without the FEC recovery integration the dropped frames would never be delivered and
// the ordered-equality assertion fails.
func TestMultipathFECRecoversWithinBudget(t *testing.T) {
	const (
		dataShards   = 4
		parityShards = 2
		groups       = 3
	)
	psk := testKey(t, 0x25)
	fecCfg := &fec.Config{DataShards: dataShards, ParityShards: parityShards, Deadline: testFECDeadline}

	// Sender produces a real FEC-coded wire stream.
	sender := newMultipathFEC(t, loopbackPaths(1), psk, fecCfg)
	if _, _, err := sender.Open(0); err != nil {
		t.Fatalf("sender Open: %v", err)
	}
	t.Cleanup(func() { _ = sender.Close() })
	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen peer: %v", err)
	}
	defer peer.Close()
	sender.paths[0].setRemote(peer.LocalAddr().(*net.UDPAddr).AddrPort())
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatalf("codec: %v", err)
	}
	payloads := payloadStream(dataShards * groups)
	captured := sendAndCapture(t, sender, peer, codec, payloads, dataShards)

	// Receiver runs the FEC decode + resequencing integration under test.
	rx := newMultipathFEC(t, loopbackPaths(1), psk, fecCfg)
	fns, _, err := rx.Open(0)
	if err != nil {
		t.Fatalf("rx Open: %v", err)
	}
	t.Cleanup(func() { _ = rx.Close() })
	src := netip.MustParseAddrPort("127.0.0.1:9999")

	// Drop up to M data frames per group, but never the very first frame of the whole
	// stream (outer-seq 1): the resequencer pins its release point to the first frame it
	// sees, so keeping group 0 intact anchors it at seq 1 and every later group's losses
	// fall at or above the release point where recovery fills them in order. Groups 1 and
	// 2 each lose exactly M=2 data frames (shard indices 1 and 2), within the repair
	// budget.
	const (
		droppedPerLossyGroup = parityShards
		lossyGroups          = 2 // groups 1 and 2
	)
	for _, c := range captured {
		if d, ok := c.fr.(frame.Data); ok {
			if d.FECGroup >= 1 && (d.FECIndex == 1 || d.FECIndex == 2) {
				continue // dropped in transit (recoverable: <= M per group)
			}
		}
		feedInbound(rx, c, src)
	}

	delivered := drainExact(t, fns[0], len(payloads), 3*time.Second)
	if len(delivered) != len(payloads) {
		t.Fatalf("delivered %d payloads, want %d (recovery must fill every gap in order)", len(delivered), len(payloads))
	}
	for i, got := range delivered {
		if !bytes.Equal(got, payloads[i]) {
			t.Fatalf("delivered[%d] = %q, want %q (ordered, reconstructed payloads must match)", i, got, payloads[i])
		}
	}

	snap := rx.PeerSnapshots()[0].FEC
	wantRecovered := uint64(droppedPerLossyGroup * lossyGroups)
	if snap.Recovered != wantRecovered {
		t.Fatalf("PeerSnapshots FEC.Recovered = %d, want %d (exactly the reconstructed frames)", snap.Recovered, wantRecovered)
	}
	if snap.Unrecoverable != 0 {
		t.Fatalf("PeerSnapshots FEC.Unrecoverable = %d, want 0 (all loss was within budget)", snap.Unrecoverable)
	}
}

// TestMultipathFECUnrecoverableDoesNotStall proves the > M-loss path: a group losing
// MORE than ParityShards data frames cannot be reconstructed, yet the datapath does
// NOT deadlock — the resequencer's per-gap timeout skips the unrecoverable seqs and
// releases the surviving run, and FEC fabricates nothing (recovered stays 0 for the
// unrecoverable frames). The unrecoverable COUNTER itself is asserted at the decoder
// unit level (fec.TestDecoderUnrecoverableCounter) and its wiring to /metrics at
// device.TestMetricsSourceMapsFEC + metrics.TestExpositionFECCounters, since the
// counter only lands once the group is evicted from the retain window.
func TestMultipathFECUnrecoverableDoesNotStall(t *testing.T) {
	const (
		dataShards   = 4
		parityShards = 2
		groups       = 3
	)
	psk := testKey(t, 0x26)
	fecCfg := &fec.Config{DataShards: dataShards, ParityShards: parityShards, Deadline: testFECDeadline}

	sender := newMultipathFEC(t, loopbackPaths(1), psk, fecCfg)
	if _, _, err := sender.Open(0); err != nil {
		t.Fatalf("sender Open: %v", err)
	}
	t.Cleanup(func() { _ = sender.Close() })
	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen peer: %v", err)
	}
	defer peer.Close()
	sender.paths[0].setRemote(peer.LocalAddr().(*net.UDPAddr).AddrPort())
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatalf("codec: %v", err)
	}
	payloads := payloadStream(dataShards * groups)
	captured := sendAndCapture(t, sender, peer, codec, payloads, dataShards)

	rx := newMultipathFEC(t, loopbackPaths(1), psk, fecCfg)
	fns, _, err := rx.Open(0)
	if err != nil {
		t.Fatalf("rx Open: %v", err)
	}
	t.Cleanup(func() { _ = rx.Close() })
	src := netip.MustParseAddrPort("127.0.0.1:9999")

	// Group 1 loses M+1 = 3 data frames (shard indices 0,1,2), leaving 1 data + 2 parity
	// = 3 < M=4 survivors — unrecoverable. Its three seqs become a permanent gap the
	// resequencer must time out. Groups 0 and 2 are intact. The surviving payload set is
	// every frame EXCEPT group 1's shard indices 0,1,2 (outer-seqs 5,6,7).
	lostSeqs := map[uint64]bool{5: true, 6: true, 7: true}
	for _, c := range captured {
		if d, ok := c.fr.(frame.Data); ok {
			if d.FECGroup == 1 && d.FECIndex <= 2 {
				continue // > M loss in this group
			}
		}
		feedInbound(rx, c, src)
	}

	want := make([][]byte, 0, len(payloads))
	for i, p := range payloads {
		if lostSeqs[uint64(i+1)] {
			continue
		}
		want = append(want, p)
	}

	// The drain must COMPLETE (no deadlock) within a few resequencer timeouts: the gap at
	// seqs 5,6,7 is skipped after resequencerTimeout and the run released.
	delivered := drainExact(t, fns[0], len(want), 3*time.Second)
	if len(delivered) != len(want) {
		t.Fatalf("delivered %d survivors, want %d (datapath stalled on the unrecoverable gap)", len(delivered), len(want))
	}
	for i, got := range delivered {
		if !bytes.Equal(got, want[i]) {
			t.Fatalf("survivor[%d] = %q, want %q (ordering must hold across the skipped gap)", i, got, want[i])
		}
	}

	snap := rx.PeerSnapshots()[0].FEC
	if snap.Recovered != 0 {
		t.Fatalf("PeerSnapshots FEC.Recovered = %d, want 0 (a > M-loss group must not fabricate data)", snap.Recovered)
	}
}

// TestMultipathFECResidualLossNonVacuous proves the post-FEC-recovery residual-loss gauge
// (T29, PeerSnapshots()[0].FEC.ResidualLoss) actually MEASURES residual — the whole "equal masking"
// leg of P4 rests on it, so a dead-low gauge (dropped Observe wiring or a Loss() defect)
// must not read ~0 under real unmasked loss. It drives two hand-chosen receive streams
// through the SAME K=4/M=2 datapath:
//
//   - UNMASKED: group 1 loses 3 (> M) DATA frames — unrecoverable — so its seqs are neither
//     natively received nor reconstructed, and the gauge must read ~the drop rate (3 of the
//     12 outer-seqs missing = 0.25), strictly > 0.
//   - MASKED: group 1 loses exactly M=2 DATA frames — reconstructed from parity — so every
//     outer-seq is present (natively or via recovery) and the gauge must read ~0.
//
// It is a two-sided mutation witness: breaking the NATIVE-seq Observe collapses the unmasked
// case to 0 (>0 assertion fails); breaking the RECOVERED-seq Observe leaves the masked case's
// reconstructed seqs unmarked, so it reads >0 (the ~0 assertion fails).
func TestMultipathFECResidualLossNonVacuous(t *testing.T) {
	const (
		dataShards   = 4
		parityShards = 2
		groups       = 3
	)
	psk := testKey(t, 0x29)
	fecCfg := &fec.Config{DataShards: dataShards, ParityShards: parityShards, Deadline: testFECDeadline}

	// One sender produces a real FEC-coded wire stream reused by both cases.
	sender := newMultipathFEC(t, loopbackPaths(1), psk, fecCfg)
	if _, _, err := sender.Open(0); err != nil {
		t.Fatalf("sender Open: %v", err)
	}
	t.Cleanup(func() { _ = sender.Close() })
	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen peer: %v", err)
	}
	defer peer.Close()
	sender.paths[0].setRemote(peer.LocalAddr().(*net.UDPAddr).AddrPort())
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatalf("codec: %v", err)
	}
	payloads := payloadStream(dataShards * groups) // outer-seqs 1..12; group g = seqs 4g+1..4g+4
	captured := sendAndCapture(t, sender, peer, codec, payloads, dataShards)

	// residualAfterDrops feeds a fresh receiver every captured frame EXCEPT group 1's DATA
	// shard indices in dropIdx (its parity is always delivered), then returns the residual
	// gauge. Groups 0 and 2 are always intact, so the residual window spans seqs 1..12 and a
	// hole in group 1 is charged as residual.
	residualAfterDrops := func(t *testing.T, dropIdx map[uint8]bool) float64 {
		t.Helper()
		rx := newMultipathFEC(t, loopbackPaths(1), psk, fecCfg)
		if _, _, err := rx.Open(0); err != nil {
			t.Fatalf("rx Open: %v", err)
		}
		t.Cleanup(func() { _ = rx.Close() })
		src := netip.MustParseAddrPort("127.0.0.1:9999")
		for _, c := range captured {
			if d, ok := c.fr.(frame.Data); ok && d.FECGroup == 1 && dropIdx[d.FECIndex] {
				continue // "lost" in transit
			}
			feedInbound(rx, c, src)
		}
		return rx.PeerSnapshots()[0].FEC.ResidualLoss
	}

	// UNMASKED: drop 3 of group 1's 4 data shards (> M=2). 1 data + 2 parity = 3 < K=4, so the
	// group is unrecoverable and seqs 5,6,7 are pure loss. Residual = 3 missing / 12 span.
	unmasked := residualAfterDrops(t, map[uint8]bool{0: true, 1: true, 2: true})
	const wantUnmasked = 3.0 / 12.0
	if unmasked <= 0 {
		t.Fatalf("residual under UNMASKED loss = %.4f, want > 0 — the gauge is dead (Observe wiring dropped or Loss() broken); the P4 equal-masking leg would pass vacuously", unmasked)
	}
	if unmasked < 0.20 || unmasked > 0.30 {
		t.Fatalf("residual under UNMASKED loss = %.4f, want ~%.4f (3 unrecovered of 12 seqs)", unmasked, wantUnmasked)
	}

	// MASKED: drop exactly M=2 of group 1's data shards (indices 0,1). 2 data + 2 parity = 4,
	// so the group reconstructs seqs 5,6 and every outer-seq is present. Residual must be ~0 —
	// this witnesses that the RECOVERED-seq Observe marks reconstructed frames present.
	masked := residualAfterDrops(t, map[uint8]bool{0: true, 1: true})
	if masked > 0.01 {
		t.Fatalf("residual under MASKED (fully recovered) loss = %.4f, want ~0 — recovered frames were not marked present (recovered-seq Observe wiring broken)", masked)
	}

	t.Logf("residual gauge non-vacuous: unmasked=%.4f (want ~%.4f) masked=%.4f (want ~0)", unmasked, wantUnmasked, masked)
}

// TestMultipathFECDeadlineEmitsPartialGroupParity exercises the deadline-tick
// goroutine: a partial group (fewer than DataShards admitted) that never reaches the
// size threshold still gets its ParityShards emitted once the grouping deadline
// elapses, so a low-load flow is protected rather than stranded.
func TestMultipathFECDeadlineEmitsPartialGroupParity(t *testing.T) {
	const (
		dataShards   = 8
		parityShards = 2
		partial      = 3 // < dataShards, so only the deadline tick can close the group
	)
	psk := testKey(t, 0x27)
	fecCfg := &fec.Config{DataShards: dataShards, ParityShards: parityShards, Deadline: 20 * time.Millisecond}
	sender := newMultipathFEC(t, loopbackPaths(1), psk, fecCfg)
	if _, _, err := sender.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = sender.Close() })
	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen peer: %v", err)
	}
	defer peer.Close()
	sender.paths[0].setRemote(peer.LocalAddr().(*net.UDPAddr).AddrPort())

	for i, p := range payloadStream(partial) {
		if err := sender.Send([][]byte{p}, sender.virt); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	// Read every datagram the sender emits within a bounded window: the `partial` DATA
	// frames immediately, then the K parity frames after the ~20ms deadline tick fires.
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatalf("codec: %v", err)
	}
	dataSeen, paritySeen := 0, 0
	buf := make([]byte, maxDatagram)
	deadline := time.Now().Add(2 * time.Second)
	for paritySeen < parityShards && time.Now().Before(deadline) {
		if err := peer.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
			t.Fatalf("set read deadline: %v", err)
		}
		n, _, rerr := peer.ReadFromUDPAddrPort(buf)
		if rerr != nil {
			continue // no datagram this interval; the deadline tick may not have fired yet
		}
		fr, derr := codec.Decode(append([]byte(nil), buf[:n]...))
		if derr != nil {
			t.Fatalf("decode: %v", derr)
		}
		switch f := fr.(type) {
		case frame.Data:
			dataSeen++
		case frame.Parity:
			paritySeen++
			if f.DataCount != partial {
				t.Fatalf("deadline-flushed parity DataCount = %d, want %d (M = admitted count)", f.DataCount, partial)
			}
		}
	}
	if dataSeen != partial {
		t.Fatalf("saw %d DATA frames, want %d", dataSeen, partial)
	}
	if paritySeen != parityShards {
		t.Fatalf("saw %d PARITY frames from the deadline tick, want %d (partial group was stranded)", paritySeen, parityShards)
	}
	// fs.parityFrames is incremented AFTER the socket write (production semantics kept as-is,
	// D69), so the moment this goroutine has read both parity wires off the socket, the async
	// fecTickLoop goroutine may not yet have run its post-write increment. Poll with a short
	// bounded retry instead of a single immediate read to close that race (~2% flake under
	// -race).
	snapDeadline := time.Now().Add(200 * time.Millisecond)
	var snap FECStats
	for {
		snap = sender.PeerSnapshots()[0].FEC
		if snap.ParityFrames >= parityShards || !time.Now().Before(snapDeadline) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if snap.ParityFrames != parityShards {
		t.Fatalf("PeerSnapshots FEC.ParityFrames = %d, want %d after deadline flush", snap.ParityFrames, parityShards)
	}
}

// TestMultipathFECLateRecoveryNotCountedDelivered is the fix witness for the honest-
// recovery-counter defect (T24 #4): a group reconstructed AFTER the resequencer already
// skipped its gap is reconstructed but never delivered (its seqs fell below the release
// point), so /metrics must count it 0 — recovered means delivered ahead of the release
// point, not merely reconstructed. It also proves the coupling with #2: the late
// recovered frames reach the resequencer via the NON-resyncing path and are simply
// dropped.
func TestMultipathFECLateRecoveryNotCountedDelivered(t *testing.T) {
	const (
		dataShards   = 4
		parityShards = 2
	)
	psk := testKey(t, 0x28)
	fecCfg := &fec.Config{DataShards: dataShards, ParityShards: parityShards, Deadline: testFECDeadline}

	sender := newMultipathFEC(t, loopbackPaths(1), psk, fecCfg)
	if _, _, err := sender.Open(0); err != nil {
		t.Fatalf("sender Open: %v", err)
	}
	t.Cleanup(func() { _ = sender.Close() })
	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen peer: %v", err)
	}
	defer peer.Close()
	sender.paths[0].setRemote(peer.LocalAddr().(*net.UDPAddr).AddrPort())
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatalf("codec: %v", err)
	}
	payloads := payloadStream(dataShards * 2) // groups 0 and 1
	captured := sendAndCapture(t, sender, peer, codec, payloads, dataShards)

	rx := newMultipathFEC(t, loopbackPaths(1), psk, fecCfg)
	fns, _, err := rx.Open(0)
	if err != nil {
		t.Fatalf("rx Open: %v", err)
	}
	t.Cleanup(func() { _ = rx.Close() })
	src := netip.MustParseAddrPort("127.0.0.1:9999")

	// Feed group 0 fully; feed group 1's surviving DATA (drop shard indices 0,1 = seqs
	// 5,6) but HOLD BACK group 1's parity, so the group 1 gap cannot yet be repaired.
	var heldParity []capturedFrame
	for _, c := range captured {
		if d, ok := c.fr.(frame.Data); ok && d.FECGroup == 1 && d.FECIndex <= 1 {
			continue // seqs 5,6 lost in transit
		}
		if p, ok := c.fr.(frame.Parity); ok && p.FECGroup == 1 {
			heldParity = append(heldParity, c)
			continue
		}
		feedInbound(rx, c, src)
	}

	// Drain the survivors: group 0 (seqs 1-4) + group 1's two received frames (7,8). The
	// gap at 5,6 is SKIPPED after the resequencer timeout, advancing the release point
	// past them — that is what makes the imminent recovery structurally late.
	delivered := drainExact(t, fns[0], 6, 3*time.Second)
	if len(delivered) != 6 {
		t.Fatalf("delivered %d survivors before recovery, want 6 (gap 5,6 must be skipped)", len(delivered))
	}

	// NOW deliver group 1's parity: the decoder reconstructs seqs 5,6, but the release
	// point has already advanced past them.
	for _, c := range heldParity {
		feedInbound(rx, c, src)
	}

	// The decoder DID reconstruct the two lost data shards ...
	if got := rx.fecRecv.Load().stats().Recovered; got != parityShards {
		t.Fatalf("decoder reconstructed %d shards, want %d", got, parityShards)
	}
	// ... but they were delivered too late, so the honest /metrics recovered count is 0.
	if snap := rx.PeerSnapshots()[0].FEC; snap.Recovered != 0 {
		t.Fatalf("PeerSnapshots FEC.Recovered = %d, want 0 (reconstructed-but-late must not count as delivered)", snap.Recovered)
	}
}

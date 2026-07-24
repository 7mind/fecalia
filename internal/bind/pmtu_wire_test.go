package bind

import (
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/telemetry"
)

// TestPMTUProbeAccessor verifies device.Up's per-path PMTU handle (T227, defect D88): a
// probing bind builds one telemetry.EchoAwaitProbe per path, the PRIMARY-peer accessor
// returns exactly that path's probe, and an unknown path returns nil.
func TestPMTUProbeAccessor(t *testing.T) {
	psk := testKey(t, 0x24)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(2), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	if got := m.PMTUProbe("nonexistent"); got != nil {
		t.Fatalf("PMTUProbe(unknown) = %v, want nil", got)
	}
	for i, name := range []string{"a", "b"} {
		got := m.PMTUProbe(name)
		if got == nil {
			t.Fatalf("PMTUProbe(%q) = nil, want the path's own echo-await probe", name)
		}
		if got != m.paths[i].pmtuProbe {
			t.Fatalf("PMTUProbe(%q) returned a different probe than path %d holds", name, i)
		}
		var _ telemetry.PMTUProbe = got // it satisfies the discovery seam
	}
}

// TestPMTURoamCallback verifies the concentrator roam re-probe trigger (R245): the
// per-path onRoam callback fires ONCE on an actual learned-remote CHANGE, and never on
// the first set nor a same-address re-learn (setRemote runs on every inbound probe).
func TestPMTURoamCallback(t *testing.T) {
	psk := testKey(t, 0x24)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	ap1 := netip.MustParseAddrPort("192.0.2.1:51820")
	ap2 := netip.MustParseAddrPort("192.0.2.2:51820")

	// Establish a current remote BEFORE registering, so the count reflects only changes
	// observed after registration.
	m.paths[0].setRemote(ap1)

	var roams atomic.Int32
	m.OnPathRoam("a", func() { roams.Add(1) })

	m.paths[0].setRemote(ap1) // same address: no roam
	if got := roams.Load(); got != 0 {
		t.Fatalf("roam fired %d times on a same-address re-learn, want 0", got)
	}
	m.paths[0].setRemote(ap2) // actual change: exactly one roam
	if got := roams.Load(); got != 1 {
		t.Fatalf("roam fired %d times on an actual remote change, want 1", got)
	}

	// An unknown-path registration is a no-op (must not panic).
	m.OnPathRoam("nonexistent", func() {})
}

func echoPMTUProbe(t testing.TB, m *Multipath, psk config.Key, peer *net.UDPConn, peerAP netip.AddrPort) []byte {
	t.Helper()
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, maxDatagram)
	n, err := peer.Read(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw = raw[:n]
	decoded, err := frame.Decode(psk, raw)
	if err != nil {
		t.Fatal(err)
	}
	probe, ok := decoded.(frame.Probe)
	if !ok || !probe.Padded || probe.IsEcho {
		t.Fatalf("PMTU wire frame = %#v, want originating padded PROBE", decoded)
	}
	probe.IsEcho = true
	echo, err := frame.Encode(psk, probe)
	if err != nil {
		t.Fatal(err)
	}
	m.handleInbound(m.paths[0], echo, peerAP)
	return raw
}

type pmtuProbeResult struct {
	echoed bool
	err    error
}

func startPMTUProbe(probe telemetry.PMTUProbe, pathMTU int) <-chan pmtuProbeResult {
	result := make(chan pmtuProbeResult, 1)
	go func() {
		echoed, err := probe.ProbePMTU(pathMTU)
		result <- pmtuProbeResult{echoed: echoed, err: err}
	}()
	return result
}

func waitPendingPMTUProbe(t testing.TB, ps *peerPathState) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		ps.generatedProbeMu.Lock()
		pending := ps.pendingPMTU != nil
		ps.generatedProbeMu.Unlock()
		if pending {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for PMTU probe to enter the shared cadence")
		}
	}
}

func TestPMTUProbeSuccessfulWriteAccountsExactPaddedBytes(t *testing.T) {
	psk := testKey(t, 0x25)
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, newFakeClock())
	recorder := &priorityRecordingShaper{}
	openWithPriorityRecorder(t, m, recorder)
	peer, peerAP := rawPeer(t)
	m.paths[0].setRemote(peerAP)

	result := startPMTUProbe(m.PMTUProbe("a"), 1500)
	waitPendingPMTUProbe(t, m.paths[0])
	m.emitProbes()
	raw := echoPMTUProbe(t, m, psk, peer, peerAP)
	got := <-result
	if got.err != nil || !got.echoed {
		t.Fatalf("ProbePMTU = (%v, %v), want (true, nil)", got.echoed, got.err)
	}
	if len(raw) != 1500-28 {
		t.Fatalf("padded PMTU UDP payload = %d bytes, want %d", len(raw), 1500-28)
	}
	if got := recorder.debitSnapshot(); len(got) != 1 || got[0] != len(raw) {
		t.Fatalf("PMTU priority debits = %v, want exact successful padded length [%d]", got, len(raw))
	}
	if got := m.paths[0].txBytes.Load(); got != uint64(len(raw)) {
		t.Fatalf("PMTU txBytes = %d, want exact successful padded length %d", got, len(raw))
	}
}

func TestPMTUProbeCadenceWaitDoesNotInflateMeasuredRTT(t *testing.T) {
	psk := testKey(t, 0x2A)
	clock := newFakeClock()
	m, probers, _ := newProbingMultipath(t, loopbackPaths(1), psk, clock)
	openWithPriorityRecorder(t, m, &priorityRecordingShaper{})
	peer, peerAP := rawPeer(t)
	m.paths[0].setRemote(peerAP)

	result := startPMTUProbe(m.PMTUProbe("a"), 1500)
	waitPendingPMTUProbe(t, m.paths[0])
	clock.advance(199 * time.Millisecond)
	m.emitProbes()
	echoPMTUProbe(t, m, psk, peer, peerAP)
	got := <-result
	if got.err != nil || !got.echoed {
		t.Fatalf("ProbePMTU = (%v, %v), want (true, nil)", got.echoed, got.err)
	}
	if got := probers[0].Estimate().RTT; got != 0 {
		t.Fatalf("PMTU RTT = %v, want 0: queued cadence delay must precede timestamp/registration", got)
	}
}

func TestPMTUProbeFailedWriteCreatesNoDebtOrWireBytes(t *testing.T) {
	psk := testKey(t, 0x26)
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, newFakeClock())
	recorder := &priorityRecordingShaper{}
	openWithPriorityRecorder(t, m, recorder)
	_, peerAP := rawPeer(t)
	m.paths[0].setRemote(peerAP)
	if err := m.paths[0].conn.Close(); err != nil {
		t.Fatal(err)
	}

	result := startPMTUProbe(m.PMTUProbe("a"), 1500)
	waitPendingPMTUProbe(t, m.paths[0])
	m.emitProbes()
	got := <-result
	if got.err == nil || got.echoed {
		t.Fatalf("failed ProbePMTU = (%v, %v), want (false, write error)", got.echoed, got.err)
	}
	if !errors.Is(got.err, net.ErrClosed) {
		t.Fatalf("failed ProbePMTU error = %v, want original net.ErrClosed identity", got.err)
	}
	if got := recorder.debitSnapshot(); len(got) != 0 {
		t.Fatalf("failed PMTU write created priority debt %v", got)
	}
	if got := m.paths[0].txBytes.Load(); got != 0 {
		t.Fatalf("failed PMTU write added txBytes = %d, want 0", got)
	}
	if got := m.paths[0].probeSendErrors.Load(); got != 1 {
		t.Fatalf("failed PMTU write error count = %d, want 1", got)
	}
}

func TestPMTUProbeSubstitutesForPeriodicProbeAtSharedCadence(t *testing.T) {
	psk := testKey(t, 0x27)
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, newFakeClock())
	recorder := &priorityRecordingShaper{}
	openWithPriorityRecorder(t, m, recorder)
	peer, peerAP := rawPeer(t)
	m.paths[0].setRemote(peerAP)

	result := startPMTUProbe(m.PMTUProbe("a"), 1500)
	waitPendingPMTUProbe(t, m.paths[0])

	if err := peer.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	early := make([]byte, maxDatagram)
	if n, err := peer.Read(early); err == nil {
		decoded, derr := frame.Decode(psk, early[:n])
		if derr == nil {
			if probe, ok := decoded.(frame.Probe); ok {
				probe.IsEcho = true
				if echo, eerr := frame.Encode(psk, probe); eerr == nil {
					m.handleInbound(m.paths[0], echo, peerAP)
				}
			}
		}
		<-result
		t.Fatal("padded PMTU probe bypassed the shared local probe cadence")
	} else if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("pre-cadence PMTU read error = %v, want timeout", err)
	}

	m.emitProbes()
	raw := echoPMTUProbe(t, m, psk, peer, peerAP)
	if len(raw) != 1500-28 {
		t.Fatalf("cadence-selected PMTU UDP payload = %d, want %d", len(raw), 1500-28)
	}
	select {
	case got := <-result:
		if got.err != nil || !got.echoed {
			t.Fatalf("ProbePMTU = (%v, %v), want (true, nil)", got.echoed, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("PMTU probe did not complete after its cadence slot and echo")
	}

	padLen, err := frame.PadLenForOnWire(1500 - 28)
	if err != nil {
		t.Fatal(err)
	}
	remoteRequest, err := frame.Encode(psk, frame.Probe{
		PathID:         0,
		ProbeSeq:       77,
		TimestampNanos: time.Now().UnixNano(),
		SessionID:      0x27,
		Padded:         true,
		PadLen:         padLen,
	})
	if err != nil {
		t.Fatal(err)
	}
	m.handleInbound(m.paths[0], remoteRequest, peerAP)
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, maxDatagram)
	n, err := peer.Read(echo)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := frame.Decode(psk, echo[:n])
	if err != nil {
		t.Fatal(err)
	}
	reflected, ok := decoded.(frame.Probe)
	if !ok || !reflected.IsEcho || n != len(raw) {
		t.Fatalf("reactive frame = %#v (%d bytes), want immediate padded echo of %d bytes", decoded, n, len(raw))
	}

	if err := peer.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.Read(early); err == nil {
		t.Fatal("periodic PROBE was added beside the cadence-selected PMTU probe")
	} else if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("post-cadence read error = %v, want timeout", err)
	}
	if got := recorder.debitSnapshot(); len(got) != 2 || got[0] != len(raw) || got[1] != len(raw) {
		t.Fatalf("shared-cadence priority debits = %v, want exact coincident Pburst [%d %d]", got, len(raw), len(raw))
	}
	if got := m.paths[0].txBytes.Load(); got != uint64(2*len(raw)) {
		t.Fatalf("coincident local PMTU + reactive echo txBytes = %d, want Pburst=%d", got, 2*len(raw))
	}
}

func TestPendingPMTUProbeUnblocksOnCloseAndPathRemoval(t *testing.T) {
	tests := []struct {
		name     string
		paths    []config.Path
		teardown func(*testing.T, *Multipath)
	}{
		{
			name:  "close",
			paths: loopbackPaths(1),
			teardown: func(t *testing.T, m *Multipath) {
				t.Helper()
				if err := m.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:  "runtime path removal",
			paths: loopbackPaths(2),
			teardown: func(t *testing.T, m *Multipath) {
				t.Helper()
				if err := m.RemovePath("a"); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			psk := testKey(t, 0x28)
			m, _, _ := newProbingMultipath(t, test.paths, psk, newFakeClock())
			if _, _, err := m.Open(0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Close() })
			probe := m.PMTUProbe("a")
			ps := m.paths[0]
			result := startPMTUProbe(probe, 1500)
			waitPendingPMTUProbe(t, ps)

			test.teardown(t, m)
			select {
			case got := <-result:
				if got.err == nil || got.echoed {
					t.Fatalf("pending ProbePMTU after teardown = (%v, %v), want (false, error)", got.echoed, got.err)
				}
			case <-time.After(time.Second):
				t.Fatal("pending PMTU probe remained blocked after path teardown")
			}
		})
	}
}

func TestPMTUProbeFailuresCannotConsumeConsecutiveLocalCadenceSlots(t *testing.T) {
	psk := testKey(t, 0x2B)
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, newFakeClock())
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	_, peerAP := rawPeer(t)
	m.paths[0].setRemote(peerAP)
	if err := m.paths[0].conn.Close(); err != nil {
		t.Fatal(err)
	}

	first := startPMTUProbe(m.PMTUProbe("a"), 1500)
	waitPendingPMTUProbe(t, m.paths[0])
	m.emitProbes()
	if got := <-first; got.err == nil || got.echoed {
		t.Fatalf("first failed ProbePMTU = (%v, %v), want (false, write error)", got.echoed, got.err)
	}

	second := startPMTUProbe(m.PMTUProbe("a"), 1499)
	waitPendingPMTUProbe(t, m.paths[0])
	m.emitProbes()
	select {
	case got := <-second:
		t.Fatalf("second PMTU failure consumed the immediately following cadence slot: (%v, %v)", got.echoed, got.err)
	case <-time.After(30 * time.Millisecond):
	}
	m.emitProbes()
	if got := <-second; got.err == nil || got.echoed {
		t.Fatalf("second failed ProbePMTU = (%v, %v), want (false, write error)", got.echoed, got.err)
	}
	if got := m.paths[0].probeSendErrors.Load(); got != 3 {
		t.Fatalf("two PMTU failures with intervening ordinary probe attempts counted %d errors, want 3", got)
	}
}

func TestPMTUProbeWriteErrorMappingPreservesEMSGSIZEAndTransportErrors(t *testing.T) {
	wrappedEMSGSIZE := errors.Join(errors.New("send padded probe"), syscall.EMSGSIZE)
	if got := mapPMTUProbeWriteError(wrappedEMSGSIZE); !errors.Is(got, telemetry.ErrProbeTooLarge) {
		t.Fatalf("wrapped EMSGSIZE mapped to %v, want telemetry.ErrProbeTooLarge", got)
	}
	transportErr := errors.New("transport unavailable")
	if got := mapPMTUProbeWriteError(transportErr); !errors.Is(got, transportErr) {
		t.Fatalf("transport error mapped to %v, want original %v", got, transportErr)
	}
}

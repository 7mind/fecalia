package bind

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/telemetry"
)

type mixedVersionProbe struct {
	raw   []byte
	probe frame.Probe
}

// legacyBaseAcceptProbe reproduces the c03555d Bind's recovery branch: it
// decodes WBRC directly from Probe.Payload and invalidates fast evidence for
// every accepted non-padded, non-bootstrap probe that produces no ACK.
func legacyBaseAcceptProbe(
	t testing.TB,
	base *Multipath,
	raw []byte,
	source netip.AddrPort,
) ([]byte, bool) {
	t.Helper()
	accepted, err := base.peerState.reflector.AcceptProbe(raw)
	if err != nil {
		return nil, false
	}

	if accepted.Acceptance == telemetry.ProbeAdopted {
		generation, changed := base.contracts.adoptReceivedSession(accepted.Probe.SessionID)
		if changed {
			base.publishPeerRecoveryGeneration(base.peerState, generation)
		}
	}
	pathKey := reseqPathKey(base.paths[0].id, accepted.Probe.PathID)
	if generation, roamed := base.contracts.observeReceivedSource(pathKey, source); roamed {
		base.publishPeerRecoveryGeneration(base.peerState, generation)
	}

	echoPayload := accepted.Probe.Payload
	haveRecoveryACK := false
	var admission receivedACKAdmission
	haveAdmission := false
	if !accepted.Probe.Padded && accepted.Acceptance != telemetry.ProbeBootstrap {
		message, recognized, decodeErr := telemetry.DecodeRecoveryContract(accepted.Probe.Payload)
		if decodeErr == nil && recognized && message.Type == telemetry.RecoveryContractOffer {
			beforeGeneration := base.contracts.receivedSnapshot().generation
			ack, ok := base.contracts.acceptOffer(accepted.Probe.SessionID, message, func() {})
			currentGeneration := base.contracts.receivedSnapshot().generation
			if currentGeneration != beforeGeneration {
				base.publishPeerRecoveryGeneration(base.peerState, currentGeneration)
				base.contracts.observeReceivedSource(pathKey, source)
			}
			if ok {
				echoPayload, err = telemetry.EncodeRecoveryContract(ack)
				if err != nil {
					t.Fatal(err)
				}
				haveRecoveryACK = true
				admission, haveAdmission = base.contracts.admitReceivedACK(
					accepted.Probe.SessionID,
					message,
					pathKey,
					source,
				)
			}
		}
	}
	if accepted.Acceptance == telemetry.ProbeBootstrap ||
		(!accepted.Probe.Padded && !haveRecoveryACK) {
		base.invalidatePeerRecoveryFastEvidence(base.peerState)
	}
	base.refreshPeerRecoveryWindow(base.peerState)

	echo, err := base.peerState.reflector.EncodeAcceptedProbe(accepted, echoPayload)
	if err != nil {
		if haveAdmission {
			base.contracts.cancelReceivedACK(admission)
		}
		t.Fatal(err)
	}
	if haveAdmission && base.contracts.completeReceivedACK(admission) {
		base.refreshPeerRecoveryWindow(base.peerState)
	}
	return echo, true
}

func emitOneResultProbe(
	t testing.TB,
	result *Multipath,
	peerAddr netip.AddrPort,
	peer *net.UDPConn,
) mixedVersionProbe {
	t.Helper()
	result.paths[0].setRemote(peerAddr)
	result.emitProbes()
	raw := make([]byte, maxDatagram)
	if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err := peer.Read(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw = raw[:n]
	decoded, err := frame.Decode(result.psk, raw)
	if err != nil {
		t.Fatal(err)
	}
	probe, ok := decoded.(frame.Probe)
	if !ok {
		t.Fatalf("result emitted %T, want PROBE", decoded)
	}
	return mixedVersionProbe{raw: raw, probe: probe}
}

func TestMixedVersionFeedbackCadencePreservesBaseRecoveryWindow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reverse bool
	}{
		{name: "ordered"},
		{name: "reordered", reverse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, baseProbers, baseClock := openShapedRecoveryWindowPeer(t, 1)
			psk := base.psk
			result := openNegotiatingPeer(t, psk)
			bringProberUpClean(t, baseProbers[0], psk, baseClock, testProbeUpSucc)
			peer, peerAddr := rawPeer(t)
			resultSource := leftAddrPort(t, result.paths[0].conn)

			for range 2 {
				wire := emitOneResultProbe(t, result, peerAddr, peer)
				echo, accepted := legacyBaseAcceptProbe(t, base, wire.raw, resultSource)
				if !accepted {
					t.Fatal("base rejected result bootstrap/adoption probe")
				}
				result.handleInbound(result.paths[0], echo, peerAddr)
			}
			if snapshot := base.contracts.receivedSnapshot(); !snapshot.acked || len(snapshot.venues) == 0 {
				t.Fatalf("precondition: base recovery OFFER produced no ACK venue: %+v", snapshot)
			}

			seedPriorityTestFeedback(t, result)
			result.emitProbes()
			wires := []mixedVersionProbe{
				readMixedVersionProbe(t, peer, psk),
				readMixedVersionProbe(t, peer, psk),
			}
			if tc.reverse {
				wires[0], wires[1] = wires[1], wires[0]
			}
			for _, wire := range wires {
				if echo, accepted := legacyBaseAcceptProbe(t, base, wire.raw, resultSource); accepted {
					result.handleInbound(result.paths[0], echo, peerAddr)
				}
			}

			snapshot := base.contracts.receivedSnapshot()
			stats := base.contracts.stats().Receiver
			if !snapshot.acked || len(snapshot.venues) == 0 ||
				!stats.FastEligible || stats.Window <= 0 ||
				stats.Window >= conservativeRecoveryService {
				t.Fatalf(
					"base recovery evidence after both cadence probes = snapshot %+v receiver %+v",
					snapshot,
					stats,
				)
			}
		})
	}
}

func readMixedVersionProbe(t testing.TB, peer *net.UDPConn, psk config.Key) mixedVersionProbe {
	t.Helper()
	raw := make([]byte, maxDatagram)
	if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
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
	if !ok {
		t.Fatalf("result emitted %T, want PROBE", decoded)
	}
	return mixedVersionProbe{raw: raw, probe: probe}
}

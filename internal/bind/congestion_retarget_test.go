package bind

import (
	"crypto/rand"
	"net/netip"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/shaper"
	"github.com/7mind/wanbond/internal/telemetry"
)

func TestCongestionRetargetPreservesPathRecoveryAndSequenceGeneration(t *testing.T) {
	clock := newFakeClock()
	psk := testKey(t, 0xC4)
	m, probers, scheduler := newProbingMultipath(
		t,
		loopbackPaths(1),
		psk,
		clock,
	)
	m.shaperConfigs = []config.PathShaperConfig{{
		RateBytesPerSecond:      1_000_000,
		CongestionControlled:    true,
		LinkRTT:                 50 * time.Millisecond,
		DataBurstBytes:          50_000,
		ControlReserveBytes:     1472,
		MaxEncodedDatagramBytes: 1472,
		ProbeRateBytesPerSecond: 1,
		ProbeBurstBytes:         2944,
		PriorityReserveBytes:    2944,
		FECGroupReserveBytes:    10_000,
		RecoveryWriteSlack:      config.RecoveryWriteSlack,
	}}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	reflector := telemetry.NewReflector(psk, rand.Reader)
	for range testProbeUpSucc {
		raw, err := probers[0].SendProbe()
		if err != nil {
			t.Fatal(err)
		}
		echo, _, err := reflector.Reflect(raw)
		if err != nil {
			t.Fatal(err)
		}
		clock.advance(testProbeRTT)
		m.handleInbound(
			m.paths[0],
			echo,
			netip.MustParseAddrPort("192.0.2.40:6040"),
		)
	}
	scheduler.Recompute()

	path := m.paths[0]
	writer := path.shaper
	beforeContract := writer.(recoveryPathShaper).RecoveryContract()
	beforeOpenGeneration := path.openGeneration
	beforeRecoveryGeneration := m.contracts.receivedSnapshot().generation
	beforeOuterSequence := m.outerSeq.Load()

	m.driveCongestionControllers()
	if got, want := pathCongestionRTTVariation(m), probers[0].Estimate().Jitter; got != want {
		t.Fatalf("controller RTT variation = %s, want prober RTTVAR %s", got, want)
	}
	firstCarrierGeneration := m.congestionGeneration
	if firstCarrierGeneration == 0 {
		t.Fatal("controller did not establish the active carrier generation")
	}
	actual := writer.(pathShaperReporter).Snapshot()
	if actual.RateBytesPerSecond != 850_000 || actual.DataBudgetBytes != 42_500 {
		t.Fatalf("initial retarget actual R/B = %g/%d, want 850000/42500",
			actual.RateBytesPerSecond, actual.DataBudgetBytes)
	}
	rate, epoch, dataBudget, ok := m.TUNIngressTarget()
	if !ok || rate <= 0 || dataBudget != 42_500 {
		t.Fatalf("TUN target rate/B/ok = %g/%d/%v, want positive/42500/true",
			rate, dataBudget, ok)
	}
	if err := m.ObserveTUNIngressActual(
		rate,
		epoch,
		clock.Now(),
		true,
	); err != nil {
		t.Fatal(err)
	}

	clock.advance(2 * time.Second)
	m.driveCongestionControllers()
	if m.paths[0] != path || m.paths[0].shaper != writer {
		t.Fatal("in-envelope retarget replaced the path or shaper generation")
	}
	if path.openGeneration != beforeOpenGeneration {
		t.Fatalf("open generation = %d, want unchanged %d",
			path.openGeneration, beforeOpenGeneration)
	}
	if got := m.contracts.receivedSnapshot().generation; got != beforeRecoveryGeneration {
		t.Fatalf("recovery generation = %d, want unchanged %d",
			got, beforeRecoveryGeneration)
	}
	if got := m.outerSeq.Load(); got != beforeOuterSequence {
		t.Fatalf("outer sequence = %d, want unchanged %d", got, beforeOuterSequence)
	}
	if got := m.congestionGeneration; got != firstCarrierGeneration {
		t.Fatalf("stable carrier generation = %d, want %d", got, firstCarrierGeneration)
	}
	if got := writer.(recoveryPathShaper).RecoveryContract(); got != beforeContract {
		t.Fatalf("recovery contract changed from %+v to %+v", beforeContract, got)
	}
	if snapshot := writer.(pathShaperReporter).Snapshot(); snapshot.AcceptedBytes != 0 ||
		snapshot.EmittedBytes != 0 ||
		snapshot.AsyncWriteErrorBytes != 0 {
		t.Fatalf("retarget changed byte conservation counters: %+v", snapshot)
	}

	if err := m.ObserveTUNIngressPressure(
		200*time.Millisecond,
		uint64(rate*0.2),
		200*time.Millisecond,
		true,
		epoch,
		clock.Now(),
	); err != nil {
		t.Fatal(err)
	}
	reducedRate, reducedEpoch, _, ok := m.TUNIngressTarget()
	if !ok || reducedEpoch != epoch || reducedRate >= rate {
		t.Fatalf(
			"local pressure target = %g/%d/%t, want ingress below %g at epoch %d",
			reducedRate,
			reducedEpoch,
			ok,
			rate,
			epoch,
		)
	}
	if got := writer.(pathShaperReporter).Snapshot().RateBytesPerSecond; got != 850_000 {
		t.Fatalf("local ingress pressure changed outer shaper rate to %g", got)
	}
}

var _ pathShaperRetargeter = (*shaper.Shaper)(nil)

func pathCongestionRTTVariation(m *Multipath) time.Duration {
	return m.paths[0].congestion.Snapshot().Actual.RTTVariation
}

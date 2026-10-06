package bond_test

import (
	"math"
	"testing"
	"time"
)

func TestTCPModelLossRecoveryRetainsRenoFriendlyGrowth(t *testing.T) {
	const (
		initialWindow          = 20
		rounds                 = 10
		minimumRecoveredWindow = 18
	)
	start := time.Unix(100, 0)
	sender := tcpSender{cwnd: initialWindow}
	sender.reduce(start)
	sender.cwnd = sender.ssthresh
	for round := 1; round <= rounds; round++ {
		sender.grow(start.Add(time.Duration(round)*20*time.Millisecond), sender.cwnd)
	}
	t.Logf("after %d complete acknowledged flights following loss: window %.3f", rounds, sender.cwnd)
	if sender.cwnd < minimumRecoveredWindow {
		t.Fatalf("post-loss window %.3f is below the Reno-friendly recovery bound %d", sender.cwnd, minimumRecoveredWindow)
	}
}

func TestTCPModelCubicGrowthMayExceedFriendlyGrowth(t *testing.T) {
	start := time.Unix(100, 0)
	sender := tcpSender{cwnd: 20}
	sender.reduce(start)
	sender.cwnd = sender.ssthresh
	for round := 1; round <= 30; round++ {
		sender.grow(start.Add(time.Duration(round)*500*time.Millisecond), sender.cwnd)
	}
	if sender.cwnd < 100 {
		t.Fatalf("CUBIC's elapsed-time growth was limited to additive recovery: window %.3f", sender.cwnd)
	}
}

func TestTCPModelCongestionAvoidanceRequiresAcknowledgedData(t *testing.T) {
	start := time.Unix(100, 0)
	sender := tcpSender{cwnd: 20}
	sender.reduce(start)
	sender.cwnd = sender.ssthresh
	before := sender.cwnd
	sender.grow(start.Add(10*time.Second), 0)
	if sender.cwnd != before {
		t.Fatalf("without acknowledged data the window grew from %.3f to %.3f", before, sender.cwnd)
	}
}

func TestTCPModelSlowStartCreditsAcknowledgedSegments(t *testing.T) {
	sender := tcpSender{cwnd: tcpInitialCwnd, ssthresh: math.Inf(1)}
	sender.grow(time.Unix(100, 0), 7)
	if sender.cwnd != tcpInitialCwnd+7 {
		t.Fatalf("seven newly acknowledged segments produce window %.3f", sender.cwnd)
	}
}

func TestTCPModelNewLossPreservesTheCongestionReduction(t *testing.T) {
	start := time.Unix(100, 0)
	sender := tcpSender{cwnd: 20}
	sender.reduce(start)
	sender.cwnd = sender.ssthresh
	for round := 1; round <= 10; round++ {
		sender.grow(start.Add(time.Duration(round)*20*time.Millisecond), sender.cwnd)
	}
	before := sender.cwnd
	now := start.Add(250 * time.Millisecond)
	sender.reduce(now)
	sender.cwnd = sender.ssthresh
	sender.grow(now.Add(20*time.Millisecond), sender.cwnd)
	if sender.cwnd >= before {
		t.Fatalf("first acknowledged flight undid the new congestion reduction: %.3f to %.3f", before, sender.cwnd)
	}
}

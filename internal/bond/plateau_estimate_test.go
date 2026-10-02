package bond

import (
	"testing"
	"time"
)

// A discovery that ends because delivery stopped growing took the best
// delivery of that discovery as the lane's capacity. That best is one sample
// of a control interval, and what a stall of the path held arrives within
// one. Production, 2026-10-02: the mobile downlink lane ended a discovery on
// a plateau with an estimate of 10.5 MB/s; the highest delivery sampled on it
// once a second in that capture is 5.8 MB/s. An estimate at twice the
// capacity bounds nothing.
func TestPlateauEstimateIsWhatTheLaneSustained(t *testing.T) {
	now := time.Unix(100, 0)
	p := &lane{startup: true, discoveryGain: rediscoveryGain, rate: 8e6, sendRate: 7e6, deliveryRate: 5.2e6, deliverySample: 5.0e6,
		previousDelivery: 5.4e6, rtt: 60 * time.Millisecond, sustained: sustainedDelivery{best: peak{bucket: sustainedMemory}}}
	// One interval delivered a stall's backlog.
	p.startupBest = 10.5e6
	p.sustained.best.record(now, 5.8e6)
	p.startupFlatRounds = startupPlateauRounds - 1
	p.roundDone = true
	p.discover(now, false, false, true)
	if p.startup {
		t.Fatal("discovery did not end on the plateau")
	}
	if got := p.control.capacity; got > 5.8e6 || got < 5.2e6 {
		t.Errorf("estimate after the plateau %.0f B/s; the lane sustained 5800000 and delivers 5200000", got)
	}
	if p.rate > capacityHold*5.8e6 {
		t.Errorf("target after the plateau %.0f B/s, above the hold below what the lane sustained", p.rate)
	}
}

//go:build model

package bond_test

import (
	"testing"
	"time"
)

// The production satellite link, measured with the mobile one: it moves
// between levels 10-20 ms apart that last for seconds, with a round-trip
// standard deviation of 10 ms.
var measuredSatellite = modelLane{delay: 20 * time.Millisecond, jitter: 6 * time.Millisecond, drift: 50 * time.Millisecond, shift: 8 * time.Millisecond, shiftEvery: 15 * time.Second}

// Reports a TCP transfer through the bond under several link models. It
// asserts nothing and takes minutes: run with -tags model.
func TestTCPTransferModelReports(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		lanes []modelLane
	}{
		{"300+300 Mbit/s, measured latency, no loss", []modelLane{withRate(measuredSatellite, 37.5e6, 0), withRate(measuredMobile, 37.5e6, 0)}},
		{"300+300 Mbit/s, measured latency, 0.1% loss on the satellite lane", []modelLane{withRate(measuredSatellite, 37.5e6, 0.001), withRate(measuredMobile, 37.5e6, 0)}},
		{"300+300 Mbit/s, steady latency, no loss", []modelLane{
			{rate: 37.5e6, delay: 20 * time.Millisecond},
			{rate: 37.5e6, delay: 12 * time.Millisecond}}},
		{"satellite lane alone at 300 Mbit/s, measured latency", []modelLane{withRate(measuredSatellite, 37.5e6, 0)}},
		{"satellite lane alone at 300 Mbit/s, scatter but no level shifts", []modelLane{
			{rate: 37.5e6, delay: 20 * time.Millisecond, jitter: 6 * time.Millisecond, drift: 50 * time.Millisecond}}},
		{"mobile lane alone at 300 Mbit/s, measured latency", []modelLane{withRate(measuredMobile, 37.5e6, 0)}},
		{"today's uplink, 0.4+1.25 Mbit/s, measured latency", []modelLane{withRate(measuredSatellite, 50e3, 0), withRate(measuredMobile, 156.25e3, 0)}},
		{"today's downlink, 0.5+100 Mbit/s, measured latency", []modelLane{withRate(measuredSatellite, 62.5e3, 0), withRate(measuredMobile, 12.5e6, 0)}},
	} {
		outcome := tcpTransfer{lanes: scenario.lanes, seed: 1, seconds: 60}.run(t)
		var last float64
		for _, bytes := range outcome.delivered[30:] {
			last += bytes
		}
		t.Logf("%s: last 30 s %.0f Mbit/s; every 4th second %v", scenario.name, last*8/30e6, everyFourth(megabits(outcome.delivered)))
		t.Logf("    lane targets %.1f MB/s; tcp retransmits %d timeouts %d; abandoned %d; tunnel queue drops %d (schedule %d)",
			outcome.targetMB, outcome.retransmits, outcome.timeouts, outcome.abandoned, outcome.queueDrops, outcome.aqmDrops)
	}
}

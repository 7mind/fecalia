package metrics

import (
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/prometheus/client_golang/prometheus"
)

func TestSmallQueueResidenceMeasuresOnlyFirstTransmission(t *testing.T) {
	start := time.Unix(100, 0)
	transport := bond.New(bond.Epoch{Boot: 1, Generation: 1})
	transport.SetRemote(bond.Epoch{Boot: 2, Generation: 1}, true)
	enqueue := func(size int, protocol byte, now time.Time) {
		if err := transport.Enqueue(make([]byte, size), bond.PacketMetadata{Flow: bond.FlowID{4, protocol}}, now); err != nil {
			t.Fatal(err)
		}
	}
	poll := func(now time.Time) []bond.Transmission {
		out, err := transport.Poll(now)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	enqueue(224, 17, start)
	enqueue(96, 6, start.Add(25*time.Millisecond))
	now := start.Add(50 * time.Millisecond)
	if err := transport.Path(0, 0, 40*time.Millisecond, now); err != nil {
		t.Fatal(err)
	}
	originals := poll(now)
	data := 0
	for _, tx := range originals {
		if tx.Frame.ControlType == bond.DataType && len(tx.Frame.Payload) > bond.Overhead-frame.ControlOverhead {
			data++
		}
	}
	if data != 2 {
		t.Fatalf("fixture sent %d originals, want 2", data)
	}
	check := func(now time.Time) {
		t.Helper()
		registry := prometheus.NewRegistry()
		registry.MustRegister(newAdaptiveCollector(fixedAdaptive{{Peer: "test", State: transport.Snapshot(now)}}))
		families, err := registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		expected := map[string]float64{"realtime": .050, "small_tcp": .025}
		for _, family := range families {
			if family.GetName() != "wanbond_adaptive_small_queue_residence_seconds" {
				continue
			}
			for _, metric := range family.Metric {
				class := ""
				for _, label := range metric.Label {
					if label.GetName() == "class" {
						class = label.GetValue()
					}
				}
				want, known := expected[class]
				histogram := metric.GetHistogram()
				if !known || histogram.GetSampleCount() != 1 || histogram.GetSampleSum() != want {
					t.Fatalf("%s queue residence count=%d sum=%v, want count=1 sum=%v", class, histogram.GetSampleCount(), histogram.GetSampleSum(), want)
				}
				if len(histogram.Bucket) != 6 {
					t.Fatalf("%s has %d residence buckets, want 6", class, len(histogram.Bucket))
				}
				for _, bucket := range histogram.Bucket {
					count := uint64(0)
					if bucket.GetUpperBound() >= want {
						count = 1
					}
					if bucket.GetCumulativeCount() != count {
						t.Errorf("%s bucket %v count=%d, want %d", class, bucket.GetUpperBound(), bucket.GetCumulativeCount(), count)
					}
				}
				delete(expected, class)
			}
			if len(expected) != 0 {
				t.Fatalf("missing queue residence classes: %v", expected)
			}
			return
		}
		t.Fatal("no exported queue residence despite two observed first transmissions")
	}
	check(now)
	transport.Disable(0)
	now = start.Add(140 * time.Millisecond)
	if err := transport.Path(1, 1, 40*time.Millisecond, now); err != nil {
		t.Fatal(err)
	}
	repairs := 0
	for _, tx := range poll(now) {
		if tx.Frame.ControlType == bond.DataType && len(tx.Frame.Payload) == bond.Overhead-frame.ControlOverhead+224 {
			repairs++
		}
	}
	if repairs == 0 {
		t.Fatal("fixture did not retransmit the real-time datagram")
	}
	check(now)
	enqueue(224, 17, start.Add(145*time.Millisecond))
	transport.Disable(1)
	now = start.Add(246 * time.Millisecond)
	poll(now)
	if transport.Snapshot(now).RealtimeQueueDrops.Deadline != 1 {
		t.Fatal("fixture did not expire the queued real-time datagram")
	}
	check(now)
}

package metrics

import (
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
	"github.com/prometheus/client_golang/prometheus"
)

func TestSmallQueueDropsIdentifyClassAndCause(t *testing.T) {
	type key struct{ class, cause string }
	for _, scenario := range []string{"deadline", "stale", "admission"} {
		t.Run(scenario, func(t *testing.T) {
			start := time.Unix(100, 0)
			transport := bond.New(bond.Epoch{Boot: 1, Generation: 1})
			transport.SetRemote(bond.Epoch{Boot: 2, Generation: 1}, true)
			enqueue := func(protocol byte, now time.Time) {
				if err := transport.Enqueue(make([]byte, 224), bond.PacketMetadata{Flow: bond.FlowID{4, protocol}}, now); err != nil {
					t.Fatal(err)
				}
			}
			poll := func(now time.Time) {
				if _, err := transport.Poll(now); err != nil {
					t.Fatal(err)
				}
			}
			expected := map[key]float64{}
			now := start
			switch scenario {
			case "deadline":
				enqueue(17, start)
				enqueue(6, start)
				now = start.Add(101 * time.Millisecond)
				poll(now)
				expected[key{"realtime", "deadline"}], expected[key{"small_tcp", "deadline"}] = 1, 1
			case "stale":
				enqueue(17, start)
				poll(start.Add(30 * time.Millisecond))
				enqueue(17, start.Add(50*time.Millisecond))
				now = start.Add(130 * time.Millisecond)
				poll(now)
				expected[key{"realtime", "deadline"}], expected[key{"realtime", "stale"}] = 1, 1
			case "admission":
				const fixtureLimit = 65536
				for attempt := 0; attempt < fixtureLimit && transport.Snapshot(start).InteractiveQueueDrops == 0; attempt++ {
					enqueue(17, start)
				}
				enqueue(6, start)
				expected[key{"realtime", "admission"}], expected[key{"small_tcp", "admission"}] = 1, 1
			}
			state := transport.Snapshot(now)
			if state.InteractiveQueueDrops != 2 {
				t.Fatalf("fixture dropped %d small datagrams, want 2", state.InteractiveQueueDrops)
			}
			registry := prometheus.NewRegistry()
			registry.MustRegister(newAdaptiveCollector(fixedAdaptive{{Peer: "test", State: state}}))
			families, err := registry.Gather()
			if err != nil {
				t.Fatal(err)
			}
			for _, family := range families {
				if family.GetName() != "wanbond_adaptive_small_queue_drops_total" {
					continue
				}
				var sum float64
				for _, metric := range family.Metric {
					var labels key
					for _, label := range metric.Label {
						switch label.GetName() {
						case "class":
							labels.class = label.GetValue()
						case "cause":
							labels.cause = label.GetValue()
						}
					}
					got := metric.GetCounter().GetValue()
					if got != expected[labels] {
						t.Errorf("%s/%s drops %v, want %v", labels.class, labels.cause, got, expected[labels])
					}
					sum += got
					delete(expected, labels)
				}
				if len(expected) != 0 || sum != float64(state.InteractiveQueueDrops) {
					t.Fatalf("missing counters %v or classified sum %v differs from aggregate %d", expected, sum, state.InteractiveQueueDrops)
				}
				return
			}
			t.Fatal("no exported small-queue class/cause counters despite two observed drops")
		})
	}
}

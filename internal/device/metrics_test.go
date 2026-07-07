package device

import (
	"sync"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/telemetry"
)

// fakeProvider is a trafficProvider whose snapshot the test controls, standing in for
// the live Bind so the adapter's mapping and rate derivation are exercised without an
// engine.
type fakeProvider struct {
	mu    sync.Mutex
	paths []bind.PathTraffic
}

func (f *fakeProvider) set(paths []bind.PathTraffic) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = paths
}

func (f *fakeProvider) PathSnapshots() []bind.PathTraffic {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bind.PathTraffic(nil), f.paths...)
}

// fakeClock is a manually-advanced Clock so the throughput derivation is deterministic.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var _ telemetry.Clock = (*fakeClock)(nil)

// TestMetricsSourceMapsFields asserts the adapter copies the per-path byte counters and
// telemetry verbatim into the metrics.PathSnapshot, preserving order.
func TestMetricsSourceMapsFields(t *testing.T) {
	prov := &fakeProvider{}
	prov.set([]bind.PathTraffic{
		{Name: "starlink", TxBytes: 100, RxBytes: 200, Estimate: telemetry.Estimate{RTT: 45 * time.Millisecond, Loss: 0.01}, State: telemetry.StateUp},
		{Name: "cellular", TxBytes: 5, RxBytes: 7, State: telemetry.StateDown},
	})
	src := newMetricsSource(prov, &fakeClock{now: time.Unix(1000, 0)})

	got := src.Paths()
	if len(got) != 2 {
		t.Fatalf("Paths len = %d, want 2", len(got))
	}
	if got[0].Name != "starlink" || got[0].TxBytes != 100 || got[0].RxBytes != 200 {
		t.Errorf("path 0 = %+v, want starlink tx=100 rx=200", got[0])
	}
	if got[0].Estimate.RTT != 45*time.Millisecond || got[0].State != telemetry.StateUp {
		t.Errorf("path 0 telemetry = %+v/%v, want RTT 45ms/StateUp", got[0].Estimate, got[0].State)
	}
	if got[1].Name != "cellular" || got[1].State != telemetry.StateDown {
		t.Errorf("path 1 = %+v, want cellular/StateDown", got[1])
	}
	// First scrape of each path has no prior sample, so throughput is zero.
	if got[0].ThroughputBitsPerSecond != 0 || got[1].ThroughputBitsPerSecond != 0 {
		t.Errorf("first-scrape throughput = %g/%g, want 0/0", got[0].ThroughputBitsPerSecond, got[1].ThroughputBitsPerSecond)
	}
}

// TestMetricsSourceDerivesThroughput asserts the second scrape reports throughput equal
// to the (tx+rx) byte-counter delta times 8, divided by the elapsed seconds.
func TestMetricsSourceDerivesThroughput(t *testing.T) {
	prov := &fakeProvider{}
	clock := &fakeClock{now: time.Unix(0, 0)}
	src := newMetricsSource(prov, clock)

	prov.set([]bind.PathTraffic{{Name: "starlink", TxBytes: 1000, RxBytes: 0}})
	if got := src.Paths()[0].ThroughputBitsPerSecond; got != 0 {
		t.Fatalf("first scrape throughput = %g, want 0", got)
	}

	// 2 seconds later, +2_000_000 total bytes → 2_000_000*8/2 = 8_000_000 bit/s.
	clock.advance(2 * time.Second)
	prov.set([]bind.PathTraffic{{Name: "starlink", TxBytes: 1000, RxBytes: 2_000_000}})
	got := src.Paths()[0].ThroughputBitsPerSecond
	const want = 8_000_000.0
	if got != want {
		t.Errorf("derived throughput = %g bit/s, want %g", got, want)
	}
}

// TestMetricsSourceThroughputNonNegativeOnReset asserts a backward-moving counter (only
// possible across a Close→Open reset) yields zero rather than a negative throughput.
func TestMetricsSourceThroughputNonNegativeOnReset(t *testing.T) {
	prov := &fakeProvider{}
	clock := &fakeClock{now: time.Unix(0, 0)}
	src := newMetricsSource(prov, clock)

	prov.set([]bind.PathTraffic{{Name: "starlink", TxBytes: 9_000_000, RxBytes: 0}})
	_ = src.Paths()

	clock.advance(time.Second)
	prov.set([]bind.PathTraffic{{Name: "starlink", TxBytes: 10, RxBytes: 0}}) // reset
	if got := src.Paths()[0].ThroughputBitsPerSecond; got != 0 {
		t.Errorf("throughput after counter reset = %g, want 0 (no negative rate)", got)
	}
}

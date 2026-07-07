package telemetry

import (
	"encoding/base64"
	"io"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/log"
)

// fakeClock is a hand-advanced Clock for deterministic, instant liveness tests.
type fakeClock struct {
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// testPSK builds a config.Key from 32 deterministic bytes seeded by seed.
func testPSK(t testing.TB, seed byte) config.Key {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = seed ^ byte(i*31+7)
	}
	var k config.Key
	if err := k.UnmarshalText([]byte(base64.StdEncoding.EncodeToString(raw))); err != nil {
		t.Fatalf("build PSK: %v", err)
	}
	return k
}

// testSessionID is a fixed non-zero session id for tests that need one distinct
// from a default-constructed (zero) probe's SessionID.
const testSessionID uint64 = 0x1122334455667788

// discardLogger is a Logger writing to io.Discard, for tests that do not inspect
// log output.
func discardLogger(t testing.TB) log.Logger {
	t.Helper()
	l, err := log.New("debug", io.Discard)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	return l
}

// testRand is a deterministic io.Reader standing in for the CSPRNG in tests: it
// yields reproducible, distinct, non-zero 64-bit draws so a Reflector's issued
// challenges and any drawn session ids are stable across runs. It is a splitmix64
// stream. It is NOT safe for concurrent use, which is fine: the Reflector reads it
// only under its own mutex.
type testRand struct{ state uint64 }

func newTestRand() *testRand { return &testRand{state: 0x9E3779B97F4A7C15} }

func (r *testRand) Read(p []byte) (int, error) {
	for i := range p {
		r.state += 0x9E3779B97F4A7C15
		z := r.state
		z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
		z = (z ^ (z >> 27)) * 0x94D049BB133111EB
		z ^= z >> 31
		p[i] = byte(z)
	}
	return len(p), nil
}

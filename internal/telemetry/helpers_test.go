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

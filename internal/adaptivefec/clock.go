package adaptivefec

import "time"

// Clock is the injectable time source the controller reads for its rate-limit
// and dwell timing. Production code passes SystemClock; the simulation tests
// pass a hand-advanced fake so every trajectory is deterministic and instant
// (no real sleeps). Mirrors internal/fec.Clock and internal/telemetry.Clock —
// the packages do not import one another, so the tiny interface is restated
// here rather than coupling.
type Clock interface {
	Now() time.Time
}

// SystemClock is the wall-clock Clock.
type SystemClock struct{}

// Now returns the current wall-clock time.
func (SystemClock) Now() time.Time { return time.Now() }

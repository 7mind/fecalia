package fec

import "time"

// Clock is the injectable time source the encoder reads to enforce the grouping
// deadline. Production code passes SystemClock; tests pass a hand-advanced fake
// so the deadline flush is deterministic and instant (no real sleeps). The FEC
// layer never reads the wall clock directly.
type Clock interface {
	Now() time.Time
}

// SystemClock is the wall-clock Clock.
type SystemClock struct{}

// Now returns the current wall-clock time.
func (SystemClock) Now() time.Time { return time.Now() }

package fec

import (
	"math/rand"
	"testing"
	"time"
)

// fakeClock is a hand-advanced Clock for deterministic, instant deadline tests —
// no real sleeps ever run.
type fakeClock struct {
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// randomPayloads returns n opaque payloads of varied length, including empty and
// non-empty, to exercise the per-group shard-length padding.
func randomPayloads(rng *rand.Rand, n int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		l := rng.Intn(64) // includes 0 -> empty payload
		b := make([]byte, l)
		rng.Read(b)
		out[i] = b
	}
	return out
}

// admitAll admits every payload into enc and returns the emitted data shards plus
// the parity shards produced when the group filled to N. It requires len(payloads)
// == N so the fill path (not the deadline path) closes the group.
func admitAll(t *testing.T, enc *Encoder, payloads [][]byte) ([]DataShard, []ParityShard) {
	t.Helper()
	var data []DataShard
	var parity []ParityShard
	for i, p := range payloads {
		ds, par, err := enc.Admit(p)
		if err != nil {
			t.Fatalf("admit %d: %v", i, err)
		}
		data = append(data, ds)
		if par != nil {
			parity = par
		}
	}
	return data, parity
}

// allShards concatenates data and parity shards into one Shard slice in shard
// order (data indices first, then parity).
func allShards(data []DataShard, parity []ParityShard) []Shard {
	shards := make([]Shard, 0, len(data)+len(parity))
	for _, d := range data {
		shards = append(shards, d)
	}
	for _, p := range parity {
		shards = append(shards, p)
	}
	return shards
}

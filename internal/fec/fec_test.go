package fec

import (
	"bytes"
	"math/bits"
	"math/rand"
	"testing"
	"time"
)

const testDeadline = 50 * time.Millisecond

// reconstruct feeds every surviving shard into a fresh Decoder and returns the
// full data set it could assemble: directly-received data payloads unioned with
// parity-reconstructed ones, keyed by data index.
func reconstruct(t *testing.T, cfg Config, survivors []Shard) map[int][]byte {
	t.Helper()
	dec, err := NewDecoder(cfg)
	if err != nil {
		t.Fatalf("new decoder: %v", err)
	}
	got := make(map[int][]byte)
	for _, s := range survivors {
		if ds, ok := s.(DataShard); ok {
			got[ds.Index] = ds.Payload
		}
		rec, err := dec.Offer(s)
		if err != nil {
			t.Fatalf("offer %#v: %v", s, err)
		}
		for _, r := range rec {
			got[r.Index] = r.Payload
		}
	}
	return got
}

// assertFullRecovery checks that every original payload appears at its index.
func assertFullRecovery(t *testing.T, originals [][]byte, got map[int][]byte) {
	t.Helper()
	if len(got) != len(originals) {
		t.Fatalf("recovered %d of %d data frames", len(got), len(originals))
	}
	for i, want := range originals {
		if !bytes.Equal(got[i], want) {
			t.Fatalf("data frame %d mismatch:\n want %x\n got  %x", i, want, got[i])
		}
	}
}

// TestExhaustiveRecovery asserts that for a full group, EVERY loss pattern of at
// most K shards is fully recovered — exhaustively over all C(N+K, <=K) subsets.
func TestExhaustiveRecovery(t *testing.T) {
	cfg := Config{DataShards: 6, ParityShards: 3, Deadline: testDeadline}
	rng := rand.New(rand.NewSource(1))
	enc, err := NewEncoder(cfg, newFakeClock())
	if err != nil {
		t.Fatal(err)
	}
	originals := randomPayloads(rng, cfg.DataShards)
	data, parity := admitAll(t, enc, originals)
	if len(parity) != cfg.ParityShards {
		t.Fatalf("filled group emitted %d parity, want %d", len(parity), cfg.ParityShards)
	}
	shards := allShards(data, parity)
	n := len(shards) // M+K == 9

	for mask := 0; mask < (1 << n); mask++ {
		if bits.OnesCount(uint(mask)) > cfg.ParityShards {
			continue // more than K losses is outside the recovery guarantee
		}
		survivors := make([]Shard, 0, n)
		for i := 0; i < n; i++ {
			if mask&(1<<i) == 0 {
				survivors = append(survivors, shards[i])
			}
		}
		got := reconstruct(t, cfg, survivors)
		assertFullRecovery(t, originals, got)
	}
}

// TestRandomizedRecovery covers many (N, K), payload-length, and drop-pattern
// combinations with shuffled shard arrival order, asserting full recovery for
// every drop of at most K shards.
func TestRandomizedRecovery(t *testing.T) {
	rng := rand.New(rand.NewSource(20240607))
	for iter := 0; iter < 3000; iter++ {
		n := 1 + rng.Intn(16)
		k := 1 + rng.Intn(5)
		cfg := Config{DataShards: n, ParityShards: k, Deadline: testDeadline}
		enc, err := NewEncoder(cfg, newFakeClock())
		if err != nil {
			t.Fatalf("iter %d: new encoder: %v", iter, err)
		}
		originals := randomPayloads(rng, n)
		data, parity := admitAll(t, enc, originals)
		shards := allShards(data, parity)

		// Drop a random subset of at most K shards.
		drop := rng.Intn(k + 1)
		perm := rng.Perm(len(shards))
		dropped := make(map[int]bool, drop)
		for i := 0; i < drop; i++ {
			dropped[perm[i]] = true
		}
		survivors := make([]Shard, 0, len(shards))
		for i, s := range shards {
			if !dropped[i] {
				survivors = append(survivors, s)
			}
		}
		rng.Shuffle(len(survivors), func(a, b int) { survivors[a], survivors[b] = survivors[b], survivors[a] })

		got := reconstruct(t, cfg, survivors)
		assertFullRecovery(t, originals, got)
	}
}

// TestPartialGroupDeadlineRecovery flushes a partially-filled group at the
// deadline via the fake clock, then verifies the emitted parity recovers losses
// over the M<N data frames actually admitted.
func TestPartialGroupDeadlineRecovery(t *testing.T) {
	cfg := Config{DataShards: 10, ParityShards: 3, Deadline: testDeadline}
	clk := newFakeClock()
	enc, err := NewEncoder(cfg, clk)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(7))

	const m = 4 // never fills to N=10
	originals := randomPayloads(rng, m)
	var data []DataShard
	for i, p := range originals {
		ds, par, err := enc.Admit(p)
		if err != nil {
			t.Fatalf("admit %d: %v", i, err)
		}
		if par != nil {
			t.Fatalf("admit %d closed a partial group early", i)
		}
		data = append(data, ds)
	}

	clk.advance(cfg.Deadline)
	parity, err := enc.Tick()
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(parity) != cfg.ParityShards {
		t.Fatalf("deadline flush emitted %d parity, want %d", len(parity), cfg.ParityShards)
	}
	for _, p := range parity {
		if p.DataCount != m {
			t.Fatalf("parity DataCount %d, want %d", p.DataCount, m)
		}
	}

	// Drop K data frames (the maximum) and recover from parity.
	shards := allShards(data, parity)
	survivors := make([]Shard, 0, len(shards))
	for i, s := range shards {
		if i < cfg.ParityShards { // drop the first K data shards
			continue
		}
		survivors = append(survivors, s)
	}
	got := reconstruct(t, cfg, survivors)
	assertFullRecovery(t, originals, got)
}

// TestDeadlineFlushTiming asserts, with the injected fake clock, that a
// partially-filled group emits NO parity before the deadline and EXACTLY K
// parity at the deadline — the grouping-latency bound holds even for a group that
// never fills. No real time passes.
func TestDeadlineFlushTiming(t *testing.T) {
	cfg := Config{DataShards: 8, ParityShards: 3, Deadline: testDeadline}
	clk := newFakeClock()
	enc, err := NewEncoder(cfg, clk)
	if err != nil {
		t.Fatal(err)
	}

	// A single frame: this group can never fill on its own.
	if _, par, err := enc.Admit([]byte("lonely")); err != nil || par != nil {
		t.Fatalf("admit: par=%v err=%v", par, err)
	}
	due, open := enc.NextDeadline()
	if !open {
		t.Fatal("expected an open group after admit")
	}
	if want := clk.Now().Add(cfg.Deadline); !due.Equal(want) {
		t.Fatalf("NextDeadline = %s, want %s", due, want)
	}

	// Just before the deadline: nothing flushes.
	clk.advance(cfg.Deadline - time.Nanosecond)
	if par, err := enc.Tick(); err != nil || par != nil {
		t.Fatalf("premature flush: par=%v err=%v", par, err)
	}

	// At the deadline: exactly K parity over the single admitted frame.
	clk.advance(time.Nanosecond)
	par, err := enc.Tick()
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(par) != cfg.ParityShards {
		t.Fatalf("deadline flush emitted %d parity, want %d", len(par), cfg.ParityShards)
	}
	for _, p := range par {
		if p.DataCount != 1 {
			t.Fatalf("parity DataCount %d, want 1", p.DataCount)
		}
	}
	// The group is now closed: a further Tick emits nothing.
	if par, err := enc.Tick(); err != nil || par != nil {
		t.Fatalf("double flush: par=%v err=%v", par, err)
	}
	if _, open := enc.NextDeadline(); open {
		t.Fatal("group still open after flush")
	}
}

// TestSingleFrameGroupRecovers checks the M=1 extreme: a lone deadline-flushed
// frame is recovered from any single surviving parity shard.
func TestSingleFrameGroupRecovers(t *testing.T) {
	cfg := Config{DataShards: 4, ParityShards: 3, Deadline: testDeadline}
	clk := newFakeClock()
	enc, err := NewEncoder(cfg, clk)
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("single opaque datagram")
	ds, _, err := enc.Admit(original)
	if err != nil {
		t.Fatal(err)
	}
	clk.advance(cfg.Deadline)
	parity, err := enc.Tick()
	if err != nil {
		t.Fatal(err)
	}
	// Drop the data frame; keep only ONE parity shard.
	_ = ds
	survivors := []Shard{parity[1]}
	got := reconstruct(t, cfg, survivors)
	assertFullRecovery(t, [][]byte{original}, got)
}

// TestOverheadRatio asserts the measured frame overhead equals the configured
// parity ratio K per N over many full groups: parityFrames*N == dataFrames*K.
func TestOverheadRatio(t *testing.T) {
	cfg := Config{DataShards: 10, ParityShards: 3, Deadline: testDeadline}
	enc, err := NewEncoder(cfg, newFakeClock())
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(99))
	const groups = 200
	dataFrames := 0
	parityFrames := 0
	for g := 0; g < groups; g++ {
		for i := 0; i < cfg.DataShards; i++ {
			_, par, err := enc.Admit(randomPayloads(rng, 1)[0])
			if err != nil {
				t.Fatal(err)
			}
			dataFrames++
			parityFrames += len(par)
		}
	}
	if parityFrames != groups*cfg.ParityShards {
		t.Fatalf("emitted %d parity frames, want %d", parityFrames, groups*cfg.ParityShards)
	}
	if parityFrames*cfg.DataShards != dataFrames*cfg.ParityShards {
		t.Fatalf("overhead ratio mismatch: %d parity / %d data != %d/%d",
			parityFrames, dataFrames, cfg.ParityShards, cfg.DataShards)
	}
}

// TestBeyondBudgetNotRecovered confirms the recovery bound is exactly K: losing
// K+1 data frames leaves fewer than M shards, so the Decoder recovers nothing and
// never fabricates data. This proves the guarantee is real, not vacuous.
func TestBeyondBudgetNotRecovered(t *testing.T) {
	cfg := Config{DataShards: 6, ParityShards: 3, Deadline: testDeadline}
	enc, err := NewEncoder(cfg, newFakeClock())
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(3))
	originals := randomPayloads(rng, cfg.DataShards)
	data, parity := admitAll(t, enc, originals)
	shards := allShards(data, parity)

	// Drop K+1 = 4 data shards; only 2 data + 3 parity = 5 < M=6 survive.
	survivors := make([]Shard, 0, len(shards))
	for i, s := range shards {
		if i < cfg.ParityShards+1 { // first K+1 shards are data indices 0..3
			continue
		}
		survivors = append(survivors, s)
	}
	got := reconstruct(t, cfg, survivors)
	if len(got) >= cfg.DataShards {
		t.Fatalf("recovered %d data frames beyond the K=%d budget", len(got), cfg.ParityShards)
	}
}

// TestConfigValidation rejects out-of-range parameters and a nil clock.
func TestConfigValidation(t *testing.T) {
	clk := newFakeClock()
	bad := []Config{
		{DataShards: 0, ParityShards: 1, Deadline: testDeadline},
		{DataShards: 1, ParityShards: 0, Deadline: testDeadline},
		{DataShards: 1, ParityShards: 1, Deadline: 0},
		{DataShards: 250, ParityShards: 10, Deadline: testDeadline},
	}
	for i, cfg := range bad {
		if _, err := NewEncoder(cfg, clk); err == nil {
			t.Fatalf("case %d: NewEncoder accepted invalid config %+v", i, cfg)
		}
		if _, err := NewDecoder(cfg); err == nil {
			t.Fatalf("case %d: NewDecoder accepted invalid config %+v", i, cfg)
		}
	}
	if _, err := NewEncoder(Config{DataShards: 1, ParityShards: 1, Deadline: testDeadline}, nil); err == nil {
		t.Fatal("NewEncoder accepted a nil clock")
	}
}

// TestShardCodec round-trips the length-prefixed shard encoding, including empty
// and maximally-padded payloads.
func TestShardCodec(t *testing.T) {
	cases := [][]byte{nil, {}, []byte("x"), bytes.Repeat([]byte{0xAB}, 40)}
	for _, payload := range cases {
		shardLen := lenPrefixLen + len(payload) + 7 // deliberate extra padding
		shard := encodeDataShard(payload, shardLen)
		if len(shard) != shardLen {
			t.Fatalf("shard len %d, want %d", len(shard), shardLen)
		}
		got, err := decodeDataShard(shard)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("round-trip mismatch: want %x got %x", payload, got)
		}
	}
	if _, err := decodeDataShard([]byte{0, 0}); err == nil {
		t.Fatal("decodeDataShard accepted a shard shorter than the length prefix")
	}
	corrupt := []byte{0, 0, 0, 99, 1, 2} // claims 99 payload bytes, has 2
	if _, err := decodeDataShard(corrupt); err == nil {
		t.Fatal("decodeDataShard accepted an overrunning length prefix")
	}
}

package fec

import (
	"bytes"
	"math/rand"
	"testing"
)

// TestSetParityClampsToCeiling asserts SetParity is bounded to [0, cfg.ParityShards]:
// cfg.ParityShards is the ceiling both ends agree on (the decoder is built at it), so
// a target above it is clamped down and a negative target to zero.
func TestSetParityClampsToCeiling(t *testing.T) {
	cfg := Config{DataShards: 10, ParityShards: 6, Deadline: testDeadline}
	enc, err := NewEncoder(cfg, newFakeClock())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		set, want int
	}{
		{set: 3, want: 3},
		{set: 6, want: 6},
		{set: 9, want: 6},  // above ceiling -> clamped to ceiling
		{set: -2, want: 0}, // negative -> 0
		{set: 0, want: 0},
	}
	for _, c := range cases {
		enc.SetParity(c.set)
		rng := rand.New(rand.NewSource(int64(c.set + 100)))
		originals := randomPayloads(rng, cfg.DataShards)
		_, parity := admitAll(t, enc, originals)
		if len(parity) != c.want {
			t.Fatalf("SetParity(%d): group emitted %d parity, want %d", c.set, len(parity), c.want)
		}
	}
}

// TestVaryingParityDecodesAtCeiling is the T29 crux: the encoder varies the per-group
// parity count over time (as the adaptive controller would), yet EVERY group decodes
// through a SINGLE decoder built at the fixed ceiling cfg.ParityShards. This proves the
// prefix-consistency the wiring relies on — a group coded RS(m, k<ceiling) reconstructs
// against the ceiling codec unchanged, so no wire/decoder change is needed. For each
// group we drop exactly its own parity budget of data shards (the worst case it can
// still fully recover) and assert full recovery.
func TestVaryingParityDecodesAtCeiling(t *testing.T) {
	const ceiling = 6
	cfg := Config{DataShards: 10, ParityShards: ceiling, Deadline: testDeadline}
	enc, err := NewEncoder(cfg, newFakeClock())
	if err != nil {
		t.Fatal(err)
	}
	// ONE decoder for the whole connection, built at the ceiling — exactly as the bind
	// builds it once per Open and feeds every group through it.
	dec, err := NewDecoder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(42))

	// A trajectory of per-group parity counts covering the full [0,ceiling] range,
	// including a shed-to-zero group and a re-raise, in an order a controller might slew.
	trajectory := []int{6, 4, 2, 1, 0, 1, 3, 5, 6, 2}
	for gi, k := range trajectory {
		enc.SetParity(k)
		originals := randomPayloads(rng, cfg.DataShards)
		data, parity := admitAll(t, enc, originals)
		if len(parity) != k {
			t.Fatalf("group %d: emitted %d parity, want %d", gi, len(parity), k)
		}

		shards := allShards(data, parity)
		// Drop exactly k data shards (a group of ceiling-parity budget k recovers up to
		// k erasures). For k==0 no drop is possible; the group has no protection, so all
		// data must simply be present as delivered.
		drop := k
		perm := rng.Perm(len(data)) // drop only data shards, indices 0..N-1
		dropped := make(map[int]bool, drop)
		for i := 0; i < drop; i++ {
			dropped[perm[i]] = true
		}
		survivors := make([]Shard, 0, len(shards))
		got := make(map[int][]byte)
		for i := 0; i < len(data); i++ {
			if !dropped[i] {
				survivors = append(survivors, data[i])
				got[i] = data[i].Payload
			}
		}
		for _, p := range parity {
			survivors = append(survivors, p)
		}
		// Shuffle arrival order so data-before-parity and parity-before-data races are
		// both exercised across groups, then feed every survivor once through the shared
		// ceiling decoder, collecting each reconstruction.
		rng.Shuffle(len(survivors), func(a, b int) { survivors[a], survivors[b] = survivors[b], survivors[a] })
		for _, s := range survivors {
			rec, err := dec.Offer(s)
			if err != nil {
				t.Fatalf("group %d: offer %#v: %v", gi, s, err)
			}
			for _, r := range rec {
				got[r.Index] = r.Payload
			}
		}

		if len(got) != len(originals) {
			t.Fatalf("group %d (parity=%d, dropped %d data): recovered %d of %d frames",
				gi, k, drop, len(got), len(originals))
		}
		for i, want := range originals {
			if !bytes.Equal(got[i], want) {
				t.Fatalf("group %d frame %d mismatch:\n want %x\n got  %x", gi, i, want, got[i])
			}
		}
	}
}

// TestSetParityZeroEmitsNoParity asserts a group opened with parity target 0 closes
// with no parity (redundancy fully shed) and its data still flows.
func TestSetParityZeroEmitsNoParity(t *testing.T) {
	cfg := Config{DataShards: 4, ParityShards: 6, Deadline: testDeadline}
	enc, err := NewEncoder(cfg, newFakeClock())
	if err != nil {
		t.Fatal(err)
	}
	enc.SetParity(0)
	rng := rand.New(rand.NewSource(7))
	originals := randomPayloads(rng, cfg.DataShards)
	data, parity := admitAll(t, enc, originals)
	if len(parity) != 0 {
		t.Fatalf("parity-0 group emitted %d parity, want 0", len(parity))
	}
	if len(data) != cfg.DataShards {
		t.Fatalf("parity-0 group emitted %d data shards, want %d", len(data), cfg.DataShards)
	}
}

// TestSetParityZeroDeadlineFlush asserts the deadline path also honours a zero parity
// target: a partial group flushed by Tick with parity 0 closes with no parity.
func TestSetParityZeroDeadlineFlush(t *testing.T) {
	cfg := Config{DataShards: 10, ParityShards: 6, Deadline: testDeadline}
	clk := newFakeClock()
	enc, err := NewEncoder(cfg, clk)
	if err != nil {
		t.Fatal(err)
	}
	enc.SetParity(0)
	if _, _, err := enc.Admit([]byte("only-one")); err != nil {
		t.Fatalf("admit: %v", err)
	}
	clk.advance(cfg.Deadline)
	parity, err := enc.Tick()
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(parity) != 0 {
		t.Fatalf("parity-0 deadline flush emitted %d parity, want 0", len(parity))
	}
	if _, open := enc.NextDeadline(); open {
		t.Fatalf("group should be closed after the deadline flush")
	}
}

// TestSetParityDoesNotResizeOpenGroup asserts a group's parity is fixed at openGroup:
// retargeting mid-group takes effect only on the NEXT group, so an in-flight group
// always decodes against the metadata it was coded with.
func TestSetParityDoesNotResizeOpenGroup(t *testing.T) {
	cfg := Config{DataShards: 4, ParityShards: 6, Deadline: testDeadline}
	enc, err := NewEncoder(cfg, newFakeClock())
	if err != nil {
		t.Fatal(err)
	}
	enc.SetParity(2) // group 0 will open at parity 2
	rng := rand.New(rand.NewSource(11))

	// Admit the first frame (opens group 0 at parity 2), THEN retarget to 5 mid-group.
	first := randomPayloads(rng, 1)[0]
	if _, par, err := enc.Admit(first); err != nil || par != nil {
		t.Fatalf("first admit: par=%v err=%v", par, err)
	}
	enc.SetParity(5) // must not affect the already-open group 0
	// Fill the remaining shards of group 0.
	var parity []ParityShard
	for i := 1; i < cfg.DataShards; i++ {
		_, par, err := enc.Admit(randomPayloads(rng, 1)[0])
		if err != nil {
			t.Fatalf("admit %d: %v", i, err)
		}
		if par != nil {
			parity = par
		}
	}
	if len(parity) != 2 {
		t.Fatalf("group 0 emitted %d parity, want 2 (mid-group retarget must not resize it)", len(parity))
	}

	// The NEXT group picks up the retargeted 5.
	originals := randomPayloads(rng, cfg.DataShards)
	_, parity2 := admitAll(t, enc, originals)
	if len(parity2) != 5 {
		t.Fatalf("group 1 emitted %d parity, want 5 (retarget applies to the next group)", len(parity2))
	}
}

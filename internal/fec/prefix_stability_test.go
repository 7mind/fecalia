package fec

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/klauspost/reedsolomon"
)

// emitGroup codes payloads into ONE group at parity target k and returns its data
// shards plus the parity emitted at close. It admits every payload (each emits a data
// shard) and, when the group did not fill to DataShards, flushes it so a PARTIAL
// group (m < DataShards) still closes and carries its parity — the deadline-flushed
// case the adaptive datapath actually produces. len(payloads) is the group
// cardinality m; it must be in [1, cfg.DataShards].
func emitGroup(t *testing.T, cfg Config, k int, payloads [][]byte) ([]DataShard, []ParityShard) {
	t.Helper()
	if len(payloads) < 1 || len(payloads) > cfg.DataShards {
		t.Fatalf("emitGroup: m=%d out of [1,%d]", len(payloads), cfg.DataShards)
	}
	enc, err := NewEncoder(cfg, newFakeClock())
	if err != nil {
		t.Fatal(err)
	}
	enc.SetParity(k)
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
	// A partial group (m < DataShards) is still open; flush it so its parity is coded
	// over the m shards admitted so far, exactly as the deadline path would.
	if _, open := enc.NextDeadline(); open {
		par, err := enc.Flush()
		if err != nil {
			t.Fatalf("flush partial group (m=%d k=%d): %v", len(payloads), k, err)
		}
		parity = par
	}
	return data, parity
}

// TestPartialGroupByteExactAtCeiling is the D25 partial-group property test: it
// round-trips the FULL (partial-m x partial-k) space the adaptive encoder actually
// produces — every m in [1,DataShards] crossed with every k in [0,ceiling] — through a
// SINGLE decoder built at the fixed ceiling parity, asserting BYTE-EXACT recovery of
// every payload. The pre-existing TestVaryingParityDecodesAtCeiling only covered full
// groups (m == DataShards); a deadline-flushed group with m < DataShards AND
// k < ceiling simultaneously was never round-tripped.
//
// This is a genuine witness of the klauspost prefix-stability the datapath rests on:
// the encoder codes each group RS(m, k) while the decoder reconstructs against
// RS(m, ceiling). Recovery is byte-exact ONLY IF parity shard j is identical for
// RS(m, k) and RS(m, ceiling). Were a future reedsolomon default to break that (Cauchy/
// PAR1/Jerasure/leopard, or a k==1 XOR fast path), the decoder would reconstruct WRONG
// bytes from the mismatched parity and this test's byte-exact assertion would fail.
func TestPartialGroupByteExactAtCeiling(t *testing.T) {
	const (
		ceiling = 6
		N       = 10
	)
	cfg := Config{DataShards: N, ParityShards: ceiling, Deadline: testDeadline}
	rng := rand.New(rand.NewSource(20260708))

	for m := 1; m <= N; m++ {
		for k := 0; k <= ceiling; k++ {
			originals := randomPayloads(rng, m)
			data, parity := emitGroup(t, cfg, k, originals)
			if len(data) != m {
				t.Fatalf("m=%d k=%d: emitted %d data shards, want %d", m, k, len(data), m)
			}
			if len(parity) != k {
				t.Fatalf("m=%d k=%d: emitted %d parity, want %d", m, k, len(parity), k)
			}

			// ONE decoder built at the fixed ceiling — exactly as the bind builds it once
			// per Open and feeds every varying-M group through it.
			dec, err := NewDecoder(cfg)
			if err != nil {
				t.Fatal(err)
			}

			// Drop the maximal recoverable number of data shards: min(k, m). Survivors
			// (m-drop) data + k parity >= m iff k >= drop, which holds. k == 0 drops
			// nothing (no protection): every data frame must simply arrive intact.
			drop := k
			if drop > m {
				drop = m
			}
			perm := rng.Perm(m)
			dropped := make(map[int]bool, drop)
			for i := 0; i < drop; i++ {
				dropped[perm[i]] = true
			}

			got := make(map[int][]byte)
			survivors := make([]Shard, 0, m+k)
			for i := 0; i < m; i++ {
				if dropped[i] {
					continue
				}
				survivors = append(survivors, data[i])
				got[i] = data[i].Payload
			}
			for _, p := range parity {
				survivors = append(survivors, p)
			}
			// Shuffle arrival order so data-before-parity and parity-before-data are both
			// exercised across the (m,k) grid.
			rng.Shuffle(len(survivors), func(a, b int) { survivors[a], survivors[b] = survivors[b], survivors[a] })
			for _, s := range survivors {
				rec, err := dec.Offer(s)
				if err != nil {
					t.Fatalf("m=%d k=%d: offer %#v: %v", m, k, s, err)
				}
				for _, r := range rec {
					got[r.Index] = r.Payload
				}
			}

			if len(got) != m {
				t.Fatalf("m=%d k=%d (dropped %d data): recovered %d of %d frames", m, k, drop, len(got), m)
			}
			for i, want := range originals {
				if !bytes.Equal(got[i], want) {
					t.Fatalf("m=%d k=%d frame %d NOT byte-exact:\n want %x\n got  %x", m, k, i, want, got[i])
				}
			}
		}
	}
}

// encodeParityDefault codes m data shards into k parity shards through the EXACT call
// the FEC codec uses — reedsolomon.New(m, k) with the default options — and returns
// the k parity shard payloads. It is the primitive the prefix-stability invariant
// pins.
func encodeParityDefault(t *testing.T, m, k int, data [][]byte) [][]byte {
	t.Helper()
	c, err := reedsolomon.New(m, k)
	if err != nil {
		t.Fatalf("reedsolomon.New(%d,%d): %v", m, k, err)
	}
	shardLen := len(data[0])
	shards := make([][]byte, m+k)
	for i := 0; i < m; i++ {
		shards[i] = append([]byte(nil), data[i]...)
	}
	for j := 0; j < k; j++ {
		shards[m+j] = make([]byte, shardLen)
	}
	if err := c.Encode(shards); err != nil {
		t.Fatalf("encode RS(%d,%d): %v", m, k, err)
	}
	out := make([][]byte, k)
	for j := 0; j < k; j++ {
		out[j] = shards[m+j]
	}
	return out
}

// TestKlauspostParityPrefixStableInvariant is the D25 build/test-time invariant that
// PINS the undocumented reedsolomon guarantee the fixed-ceiling FEC decoder silently
// relies on: for a fixed data-shard count m, the generator-matrix parity rows form a
// STABLE PREFIX as the total parity count varies. Operationally, parity shard j coded
// by RS(m, k) is byte-identical to parity shard j coded by RS(m, ceiling) for every
// j < k. This is why a group the encoder codes RS(m, k<ceiling) decodes unchanged
// against the decoder's RS(m, ceiling) codec.
//
// reedsolomon.New's DEFAULT matrix (v1.14.1) is buildMatrix = Vandermonde x top-inverse:
// parity row (m+j) is vandermonde row (m+j) multiplied by the inverse of the top mxm
// square, and BOTH factors depend only on (m, j) — never on the total shard count — so
// the prefix is stable. This test fails LOUDLY if a reedsolomon upgrade flips that
// default (Cauchy/PAR1/Jerasure/leopard, or a k==1 XOR fast path), which would otherwise
// SILENTLY corrupt every reconstructed inner datagram with no other test catching it.
// See the go.mod pin note next to github.com/klauspost/reedsolomon.
func TestKlauspostParityPrefixStableInvariant(t *testing.T) {
	const (
		ceiling  = 8
		maxData  = 20
		shardLen = 64
	)
	rng := rand.New(rand.NewSource(0x0FEC))
	for m := 1; m <= maxData; m++ {
		data := make([][]byte, m)
		for i := range data {
			data[i] = make([]byte, shardLen)
			rng.Read(data[i])
		}
		ref := encodeParityDefault(t, m, ceiling, data) // RS(m, ceiling) reference
		for k := 1; k <= ceiling; k++ {
			got := encodeParityDefault(t, m, k, data)
			for j := 0; j < k; j++ {
				if !bytes.Equal(got[j], ref[j]) {
					t.Fatalf("reedsolomon parity-prefix stability VIOLATED: RS(%d,%d) parity[%d] != RS(%d,%d) parity[%d].\n"+
						"The fixed-ceiling FEC decoder reconstructs varying-M groups against RS(m,ceiling) and would now SILENTLY corrupt every recovered payload.\n"+
						"A reedsolomon upgrade likely changed the default New() matrix away from Vandermonde x top-inverse; re-verify and re-pin (see go.mod note).",
						m, k, j, m, ceiling, j)
				}
			}
		}
	}
}

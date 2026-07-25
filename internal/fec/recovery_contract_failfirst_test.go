package fec

import (
	"testing"
	"time"
)

func TestDiscardIncompletePreservesCompletedGroupsAndHighWater(t *testing.T) {
	dec, err := NewDecoder(Config{DataShards: 3, ParityShards: 1, Deadline: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	dec.highWater = 11
	dec.haveHighWater = true
	dec.groups[9] = &groupState{dataCount: 3, data: map[int][]byte{0: {1}}, parity: map[int][]byte{}}
	dec.groups[10] = &groupState{dataCount: 1, data: map[int][]byte{0: {2}}, parity: map[int][]byte{}, done: true}

	if cleared := dec.DiscardIncompletePreserveHighWater(); cleared != 1 {
		t.Fatalf("cleared = %d, want 1", cleared)
	}
	if _, ok := dec.groups[9]; ok {
		t.Fatal("incomplete old-contract group retained")
	}
	if _, ok := dec.groups[10]; !ok {
		t.Fatal("completed old-contract group discarded")
	}
	if !dec.haveHighWater || dec.highWater != 11 {
		t.Fatalf("high-water = %d,%v, want 11,true", dec.highWater, dec.haveHighWater)
	}
}

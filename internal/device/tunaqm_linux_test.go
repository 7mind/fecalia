//go:build linux

package device

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func TestLinuxTUNAQMRateOnlyChangePreservesLeaf(t *testing.T) {
	currentRateBits := int64(5_440_000)
	classChanges := 0
	qdiscChanges := 0
	txQueueChanges := 0
	kernel := &linuxTUNAQMKernel{name: "wanbond-test0"}
	kernel.readTxQueueLen = func() (int, error) {
		return tunAQMTxQueueLen, nil
	}
	kernel.writeTxQueueLen = func(int) error {
		txQueueChanges++
		return nil
	}
	kernel.command = func(args ...string) ([]byte, error) {
		switch {
		case len(args) >= 2 && args[0] == "-j" && args[1] == "qdisc":
			return []byte(`[
				{"kind":"htb","root":true,"handle":"1:"},
				{"kind":"fq_codel","parent":"1:1","handle":"10:","options":{
					"limit":64,"flows":64,"quantum":1395,"target":5000,
					"interval":100000,"memory_limit":4194304,"ecn":true,
					"drop_batch":16
				}}
			]`), nil
		case len(args) >= 2 && args[0] == "class" && args[1] == "show":
			return []byte(fmt.Sprintf(
				"class htb 1:1 root rate %dbit ceil %dbit",
				currentRateBits, currentRateBits,
			)), nil
		case len(args) >= 2 && args[0] == "class" && args[1] == "replace":
			classChanges++
			for index, arg := range args {
				if arg != "rate" || index+1 >= len(args) {
					continue
				}
				value := strings.TrimSuffix(args[index+1], "bit")
				rateBits, err := strconv.ParseInt(value, 10, 64)
				if err != nil {
					return nil, err
				}
				currentRateBits = rateBits
				return nil, nil
			}
			return nil, fmt.Errorf("class replacement omitted rate: %v", args)
		case len(args) > 0 && args[0] == "qdisc":
			qdiscChanges++
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected tc command: %v", args)
		}
	}

	target := tunAQMTargetState{
		RateBytesPerSecond: 400_000,
		TxQueueLen:         tunAQMTxQueueLen,
		MTU:                1395,
	}
	if err := kernel.Apply(target); err != nil {
		t.Fatal(err)
	}
	if classChanges != 1 {
		t.Fatalf("rate-only apply class changes = %d, want 1", classChanges)
	}
	if qdiscChanges != 0 {
		t.Fatalf("rate-only apply qdisc changes = %d, want 0 (leaf preserved)", qdiscChanges)
	}
	if txQueueChanges != 0 {
		t.Fatalf("rate-only apply tx queue changes = %d, want 0", txQueueChanges)
	}
	actual, err := kernel.Read()
	if err != nil {
		t.Fatal(err)
	}
	if actual.LeafKind != "fq_codel" ||
		actual.RateBytesPerSecond != target.RateBytesPerSecond {
		t.Fatalf("rate-only readback = %+v", actual)
	}
}

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
	gsoChanges := 0
	kernel := &linuxTUNAQMKernel{name: "wanbond-test0"}
	kernel.readTxQueueLen = func() (int, error) {
		return tunAQMTxQueueLen, nil
	}
	kernel.writeTxQueueLen = func(int) error {
		txQueueChanges++
		return nil
	}
	kernel.readGSOLimits = func() (linkGSOLimits, error) {
		return linkGSOLimits{MaxSize: 13_950, MaxSegments: 10}, nil
	}
	kernel.writeGSOLimits = func(linkGSOLimits) error {
		gsoChanges++
		return nil
	}
	kernel.command = func(args ...string) ([]byte, error) {
		switch {
		case len(args) >= 3 && args[0] == "-j" &&
			args[1] == "-s" && args[2] == "qdisc":
			return []byte(`[
				{"kind":"htb","root":true,"handle":"1:"},
				{"kind":"fq","parent":"1:1","handle":"10:","options":{
					"limit":65,"flow_limit":65,"quantum":1395,
					"initial_quantum":1395
				}}
			]`), nil
		case len(args) >= 2 && args[0] == "class" && args[1] == "show":
			return []byte(fmt.Sprintf(
				"class htb 1:1 root rate %dbit ceil %dbit burst 13950b cburst 13950b",
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
		RateBytesPerSecond:  400_000,
		BurstBytes:          13_950,
		TxQueueLen:          tunAQMTxQueueLen,
		MTU:                 1395,
		QueueLimit:          65,
		GSOMaxSize:          13_950,
		GSOMaxSegments:      10,
		AdmissionLimitBytes: 14_270,
	}
	if _, err := kernel.Apply(target); err != nil {
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
	if gsoChanges != 0 {
		t.Fatalf("rate-only apply GSO changes = %d, want 0", gsoChanges)
	}
	actual, err := kernel.Read()
	if err != nil {
		t.Fatal(err)
	}
	if actual.LeafKind != "fq" ||
		actual.RateBytesPerSecond != target.RateBytesPerSecond {
		t.Fatalf("rate-only readback = %+v", actual)
	}
}

func TestLinuxTUNAQMParameterChangePreservesLiveLeaf(t *testing.T) {
	var qdiscCommands [][]string
	kernel := testLinuxTUNAQMKernel(t, `[
		{"kind":"htb","root":true,"handle":"1:","drops":37,"qlen":5,"backlog":6840},
		{"kind":"fq","parent":"1:1","handle":"10:","drops":41,"qlen":5,"backlog":6840,
		 "options":{"limit":65,"flow_limit":65,"quantum":1395,
		 "initial_quantum":1395}}
	]`, &qdiscCommands)
	target := tunAQMTargetState{
		RateBytesPerSecond:  680_000,
		BurstBytes:          13_950,
		TxQueueLen:          tunAQMTxQueueLen,
		MTU:                 1395,
		QueueLimit:          60,
		GSOMaxSize:          13_950,
		GSOMaxSegments:      10,
		AdmissionLimitBytes: 14_270,
	}
	if _, err := kernel.Apply(target); err != nil {
		t.Fatal(err)
	}
	if len(qdiscCommands) != 1 ||
		len(qdiscCommands[0]) < 2 ||
		qdiscCommands[0][1] != "change" {
		t.Fatalf("live leaf parameter commands = %v, want one in-place qdisc change",
			qdiscCommands)
	}
	actual, err := kernel.Read()
	if err != nil {
		t.Fatal(err)
	}
	if actual.Drops != 41 ||
		actual.QueueLength != 5 ||
		actual.BacklogBytes != 6840 {
		t.Fatalf("post-change queue counters = drops %d qlen %d backlog %d",
			actual.Drops, actual.QueueLength, actual.BacklogBytes)
	}
}

func TestLinuxTUNAQMDefersQueueLimitShrinkBelowBacklog(t *testing.T) {
	var qdiscCommands [][]string
	kernel := testLinuxTUNAQMKernel(t, `[
		{"kind":"htb","root":true,"handle":"1:"},
		{"kind":"fq","parent":"1:1","handle":"10:","qlen":61,"backlog":83645,
		 "options":{"limit":65,"flow_limit":65,"quantum":1395,
		 "initial_quantum":1395}}
	]`, &qdiscCommands)
	target := tunAQMTargetState{
		RateBytesPerSecond:  680_000,
		BurstBytes:          13_950,
		TxQueueLen:          tunAQMTxQueueLen,
		MTU:                 1395,
		QueueLimit:          60,
		GSOMaxSize:          13_950,
		GSOMaxSegments:      10,
		AdmissionLimitBytes: 14_270,
	}
	if _, err := kernel.Apply(target); err != nil {
		t.Fatal(err)
	}
	if len(qdiscCommands) != 0 {
		t.Fatalf("unsafe queue-limit shrink commands = %v, want deferred with qlen 61 > limit 60",
			qdiscCommands)
	}
}

func TestLinuxTUNAQMDefersGSOShrinkWithBacklog(t *testing.T) {
	var qdiscCommands [][]string
	kernel := testLinuxTUNAQMKernel(t, `[
		{"kind":"htb","root":true,"handle":"1:","qlen":1,"backlog":1395},
		{"kind":"fq","parent":"1:1","handle":"10:","qlen":1,"backlog":1395,
		 "options":{"limit":65,"flow_limit":65,"quantum":1395,
		 "initial_quantum":1395}}
	]`, &qdiscCommands)
	target := tunAQMTargetState{
		RateBytesPerSecond:  680_000,
		BurstBytes:          13_950,
		TxQueueLen:          tunAQMTxQueueLen,
		MTU:                 1395,
		QueueLimit:          65,
		GSOMaxSize:          12_555,
		GSOMaxSegments:      9,
		AdmissionLimitBytes: 14_270,
	}
	result, err := kernel.Apply(target)
	if err != nil {
		t.Fatal(err)
	}
	if !result.GSOLimitsDeferred {
		t.Fatal("GSO shrink applied while one packet remained in the TUN qdisc")
	}
	if len(qdiscCommands) != 0 {
		t.Fatalf("GSO-only shrink changed qdisc: %v", qdiscCommands)
	}
}

func TestLinuxTUNAQMDefersRingShrinkUntilDriverRingDrains(t *testing.T) {
	const targetRingSlots = 64
	var qdiscCommands [][]string
	kernel := testLinuxTUNAQMKernel(t, `[
		{"kind":"htb","root":true,"handle":"1:"},
		{"kind":"fq","parent":"1:1","handle":"10:",
		 "options":{"limit":65,"flow_limit":65,"quantum":1395,
		 "initial_quantum":1395}}
	]`, &qdiscCommands)
	currentRingSlots := 128
	pending := true
	kernel.readTxQueueLen = func() (int, error) {
		return currentRingSlots, nil
	}
	kernel.writeTxQueueLen = func(slots int) error {
		currentRingSlots = slots
		return nil
	}
	kernel.readRingPending = func() (bool, error) {
		return pending, nil
	}
	target := tunAQMTargetState{
		RateBytesPerSecond:  680_000,
		BurstBytes:          13_950,
		TxQueueLen:          targetRingSlots,
		MTU:                 1395,
		QueueLimit:          65,
		GSOMaxSize:          13_950,
		GSOMaxSegments:      10,
		AdmissionLimitBytes: 14_270,
	}
	deferred, err := kernel.Apply(target)
	if err != nil {
		t.Fatal(err)
	}
	if !deferred.RingSizeDeferred ||
		currentRingSlots != 128 {
		t.Fatalf(
			"occupied ring shrink = %+v, installed slots %d; want deferred at 128",
			deferred,
			currentRingSlots,
		)
	}

	pending = false
	applied, err := kernel.Apply(target)
	if err != nil {
		t.Fatal(err)
	}
	if applied.RingSizeDeferred ||
		currentRingSlots != targetRingSlots {
		t.Fatalf(
			"drained ring shrink = %+v, installed slots %d; want applied at %d",
			applied,
			currentRingSlots,
			targetRingSlots,
		)
	}
}

func testLinuxTUNAQMKernel(
	t testing.TB,
	qdiscJSON string,
	qdiscCommands *[][]string,
) *linuxTUNAQMKernel {
	t.Helper()
	kernel := &linuxTUNAQMKernel{name: "wanbond-test0"}
	kernel.readTxQueueLen = func() (int, error) {
		return tunAQMTxQueueLen, nil
	}
	kernel.writeTxQueueLen = func(int) error {
		return nil
	}
	kernel.readGSOLimits = func() (linkGSOLimits, error) {
		return linkGSOLimits{MaxSize: 13_950, MaxSegments: 10}, nil
	}
	kernel.writeGSOLimits = func(linkGSOLimits) error {
		return nil
	}
	kernel.command = func(args ...string) ([]byte, error) {
		switch {
		case len(args) >= 3 && args[0] == "-j" &&
			args[1] == "-s" && args[2] == "qdisc":
			return []byte(qdiscJSON), nil
		case len(args) >= 2 && args[0] == "class" && args[1] == "show":
			return []byte("class htb 1:1 root rate 5440000bit ceil 5440000bit burst 13950b cburst 13950b"), nil
		case len(args) > 0 && args[0] == "qdisc":
			*qdiscCommands = append(*qdiscCommands, append([]string(nil), args...))
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected tc command: %v", args)
		}
	}
	return kernel
}

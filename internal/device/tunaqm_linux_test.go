//go:build linux

package device

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTUNRingPendingDoesNotContendWithEngineRead(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	raw, err := reader.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	readLocked := make(chan struct{})
	releaseRead := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- raw.Read(func(uintptr) bool {
			close(readLocked)
			<-releaseRead
			return true
		})
	}()
	<-readLocked

	pollDone := make(chan error, 1)
	go func() {
		_, err := tunRingPending(reader)
		pollDone <- err
	}()
	var pollErr error
	timedOut := false
	select {
	case pollErr = <-pollDone:
	case <-time.After(250 * time.Millisecond):
		timedOut = true
	}
	close(releaseRead)
	if err := <-holderDone; err != nil {
		t.Fatal(err)
	}
	if timedOut {
		pollErr = <-pollDone
		t.Fatalf(
			"TUN ring poll waited for the engine's blocking read lock: %v",
			pollErr,
		)
	}
	if pollErr != nil {
		t.Fatal(pollErr)
	}
}

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
				{"kind":"bfifo","parent":"1:1","handle":"10:","options":{
					"limit":65000
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
		QueueLimitBytes:     65_000,
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
	if actual.LeafKind != "bfifo" ||
		actual.RateBytesPerSecond != target.RateBytesPerSecond {
		t.Fatalf("rate-only readback = %+v", actual)
	}
}

func TestLinuxTUNAQMParameterChangePreservesLiveLeaf(t *testing.T) {
	var qdiscCommands [][]string
	kernel := testLinuxTUNAQMKernel(t, `[
		{"kind":"htb","root":true,"handle":"1:","drops":37,"qlen":5,"backlog":6840},
		{"kind":"bfifo","parent":"1:1","handle":"10:","drops":41,"qlen":5,"backlog":6840,
		 "options":{"limit":65000}}
	]`, &qdiscCommands)
	target := tunAQMTargetState{
		RateBytesPerSecond:  680_000,
		BurstBytes:          13_950,
		TxQueueLen:          tunAQMTxQueueLen,
		MTU:                 1395,
		QueueLimitBytes:     60_000,
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
		{"kind":"bfifo","parent":"1:1","handle":"10:","qlen":61,"backlog":83645,
		 "options":{"limit":65000}}
	]`, &qdiscCommands)
	target := tunAQMTargetState{
		RateBytesPerSecond:  680_000,
		BurstBytes:          13_950,
		TxQueueLen:          tunAQMTxQueueLen,
		MTU:                 1395,
		QueueLimitBytes:     60_000,
		GSOMaxSize:          13_950,
		GSOMaxSegments:      10,
		AdmissionLimitBytes: 14_270,
	}
	if _, err := kernel.Apply(target); err != nil {
		t.Fatal(err)
	}
	if len(qdiscCommands) != 0 {
		t.Fatalf("unsafe queue-limit shrink commands = %v, want deferred with backlog 83645 > limit 60000 bytes",
			qdiscCommands)
	}
}

func TestLinuxTUNAQMDefersGSOShrinkWithBacklog(t *testing.T) {
	var qdiscCommands [][]string
	kernel := testLinuxTUNAQMKernel(t, `[
		{"kind":"htb","root":true,"handle":"1:","qlen":1,"backlog":1395},
		{"kind":"bfifo","parent":"1:1","handle":"10:","qlen":1,"backlog":1395,
		 "options":{"limit":65000}}
	]`, &qdiscCommands)
	target := tunAQMTargetState{
		RateBytesPerSecond:  680_000,
		BurstBytes:          13_950,
		TxQueueLen:          tunAQMTxQueueLen,
		MTU:                 1395,
		QueueLimitBytes:     65_000,
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

// Regression: D131 review found that an occupied TUN qdisc deferred GSO shrink
// after the class and leaf had already shrunk below the installed atomic skb.
func TestLinuxTUNAQMDefersLeafAndBurstShrinkWithOccupiedGSO(t *testing.T) {
	testLinuxTUNAQMDeferredAtomicCapacityShrink(t, 13_950, 1395, 1000)
}

// Regression: the installed leaf contains one old atomic GSO quantum per peer;
// retaining only one link GSO maximum during a two-peer shrink loses that bound.
func TestLinuxTUNAQMDefersAggregateLeafShrinkWithOccupiedGSO(t *testing.T) {
	testLinuxTUNAQMDeferredAtomicCapacityShrink(t, 27_900, 2790, 2000)
}

func testLinuxTUNAQMDeferredAtomicCapacityShrink(
	t testing.TB,
	installedLeafBytes int,
	desiredLeafBytes int,
	backlogBytes int,
) {
	t.Helper()
	var classCommands [][]string
	var qdiscCommands [][]string
	gsoWrites := 0
	occupied := true
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
		gsoWrites++
		return nil
	}
	kernel.command = func(args ...string) ([]byte, error) {
		switch {
		case len(args) >= 3 && args[0] == "-j" &&
			args[1] == "-s" && args[2] == "qdisc":
			if !occupied {
				return []byte(fmt.Sprintf(`[
					{"kind":"htb","root":true,"handle":"1:"},
					{"kind":"bfifo","parent":"1:1","handle":"10:",
					 "options":{"limit":%d}}
				]`, installedLeafBytes)), nil
			}
			return []byte(fmt.Sprintf(`[
				{"kind":"htb","root":true,"handle":"1:"},
				{"kind":"bfifo","parent":"1:1","handle":"10:","qlen":1,"backlog":%d,
				 "options":{"limit":%d}}
			]`, backlogBytes, installedLeafBytes)), nil
		case len(args) >= 2 && args[0] == "class" && args[1] == "show":
			return []byte("class htb 1:1 root rate 5440000bit ceil 5440000bit burst 13950b cburst 13950b"), nil
		case len(args) >= 2 && args[0] == "class" && args[1] == "replace":
			classCommands = append(classCommands, append([]string(nil), args...))
			return nil, nil
		case len(args) > 0 && args[0] == "qdisc":
			qdiscCommands = append(qdiscCommands, append([]string(nil), args...))
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected tc command: %v", args)
		}
	}
	target := tunAQMTargetState{
		RateBytesPerSecond:  680_000,
		BurstBytes:          1395,
		TxQueueLen:          tunAQMTxQueueLen,
		MTU:                 1395,
		QueueLimitBytes:     desiredLeafBytes,
		GSOMaxSize:          1395,
		GSOMaxSegments:      1,
		AdmissionLimitBytes: 1715,
	}
	result, err := kernel.Apply(target)
	if err != nil {
		t.Fatal(err)
	}
	if !result.GSOLimitsDeferred {
		t.Fatal("occupied queue did not defer GSO shrink")
	}
	if len(classCommands) != 0 || len(qdiscCommands) != 0 {
		t.Fatalf(
			"occupied GSO shrink changed class/leaf below installed %d-byte aggregate quantum: class=%v qdisc=%v",
			installedLeafBytes,
			classCommands,
			qdiscCommands,
		)
	}
	if gsoWrites != 0 {
		t.Fatalf("occupied GSO shrink writes = %d, want 0", gsoWrites)
	}
	actual, err := kernel.Read()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDeferredTUNAQMReadback(target, actual, result); err != nil {
		t.Fatalf("held atomic-capacity readback: %v", err)
	}

	occupied = false
	result, err = kernel.Apply(target)
	if err != nil {
		t.Fatal(err)
	}
	if result.GSOLimitsDeferred || result.QueueLimitDeferred ||
		len(classCommands) != 1 || len(qdiscCommands) != 1 ||
		gsoWrites != 1 {
		t.Fatalf(
			"post-drain shrink = %+v class=%v qdisc=%v GSO writes=%d, want one complete shrink",
			result,
			classCommands,
			qdiscCommands,
			gsoWrites,
		)
	}
}

func TestLinuxTUNAQMRetainsRingHighWaterAfterDriverRingDrains(t *testing.T) {
	const targetRingSlots = 64
	var qdiscCommands [][]string
	kernel := testLinuxTUNAQMKernel(t, `[
		{"kind":"htb","root":true,"handle":"1:"},
		{"kind":"bfifo","parent":"1:1","handle":"10:",
		 "options":{"limit":65000}}
	]`, &qdiscCommands)
	currentRingSlots := 128
	ringWrites := 0
	pending := true
	kernel.readTxQueueLen = func() (int, error) {
		return currentRingSlots, nil
	}
	kernel.writeTxQueueLen = func(slots int) error {
		ringWrites++
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
		QueueLimitBytes:     65_000,
		GSOMaxSize:          13_950,
		GSOMaxSegments:      10,
		AdmissionLimitBytes: 14_270,
	}
	occupied, err := kernel.Apply(target)
	if err != nil {
		t.Fatal(err)
	}
	if occupied.RingSizeDeferred ||
		currentRingSlots != 128 ||
		ringWrites != 0 {
		t.Fatalf(
			"occupied ring reconcile = %+v, installed slots/writes %d/%d; want retained at 128 with no write",
			occupied,
			currentRingSlots,
			ringWrites,
		)
	}

	pending = false
	drained, err := kernel.Apply(target)
	if err != nil {
		t.Fatal(err)
	}
	if drained.RingSizeDeferred ||
		currentRingSlots != 128 ||
		ringWrites != 0 {
		t.Fatalf(
			"drained ring reconcile = %+v, installed slots/writes %d/%d; want retained at 128 with no write",
			drained,
			currentRingSlots,
			ringWrites,
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

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

type linuxTUNAQMShrinkRaceState struct {
	events           []string
	gso              linkGSOLimits
	backlog          bool
	injectOnGSOWrite bool
}

func newLinuxTUNAQMShrinkRaceKernel(
	injectOnGSOWrite bool,
) (*linuxTUNAQMKernel, *linuxTUNAQMShrinkRaceState) {
	state := &linuxTUNAQMShrinkRaceState{
		gso:              linkGSOLimits{MaxSize: 13_950, MaxSegments: 10},
		injectOnGSOWrite: injectOnGSOWrite,
	}
	kernel := &linuxTUNAQMKernel{name: "wanbond-test0"}
	kernel.readTxQueueLen = func() (int, error) {
		return tunAQMTxQueueLen, nil
	}
	kernel.writeTxQueueLen = func(int) error {
		return nil
	}
	kernel.readGSOLimits = func() (linkGSOLimits, error) {
		return state.gso, nil
	}
	kernel.writeGSOLimits = func(limits linkGSOLimits) error {
		state.events = append(state.events, "gso")
		state.gso = limits
		if state.injectOnGSOWrite {
			state.backlog = true
		}
		return nil
	}
	kernel.command = func(args ...string) ([]byte, error) {
		switch {
		case len(args) >= 3 && args[0] == "-j" &&
			args[1] == "-s" && args[2] == "qdisc":
			if state.backlog {
				return []byte(`[
					{"kind":"htb","root":true,"handle":"1:"},
					{"kind":"bfifo","parent":"1:1","handle":"10:","qlen":1,"backlog":1000,
					 "options":{"limit":13950}}
				]`), nil
			}
			return []byte(`[
				{"kind":"htb","root":true,"handle":"1:"},
				{"kind":"bfifo","parent":"1:1","handle":"10:",
				 "options":{"limit":13950}}
			]`), nil
		case len(args) >= 2 && args[0] == "class" && args[1] == "show":
			return []byte("class htb 1:1 root rate 5440000bit ceil 5440000bit burst 13950b cburst 13950b"), nil
		case len(args) >= 2 && args[0] == "class" && args[1] == "replace":
			state.events = append(state.events, "class")
			return nil, nil
		case len(args) > 0 && args[0] == "qdisc":
			state.events = append(state.events, "leaf")
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected tc command: %v", args)
		}
	}
	return kernel, state
}

func linuxTUNAQMShrinkRaceTarget() tunAQMTargetState {
	return tunAQMTargetState{
		RateBytesPerSecond:  680_000,
		BurstBytes:          1395,
		TxQueueLen:          tunAQMTxQueueLen,
		MTU:                 1395,
		QueueLimitBytes:     1395,
		GSOMaxSize:          1395,
		GSOMaxSegments:      1,
		AdmissionLimitBytes: 1715,
	}
}

// Regression: D131 final review found that a drained shrink reduced the class
// and leaf before the GSO write, exposing old-size arrivals to smaller capacity.
func TestLinuxTUNAQMGSOShrinksBeforeLeafAndBurst(t *testing.T) {
	kernel, state := newLinuxTUNAQMShrinkRaceKernel(false)
	result, err := kernel.Apply(linuxTUNAQMShrinkRaceTarget())
	if err != nil {
		t.Fatal(err)
	}
	if result.GSOLimitsDeferred || result.QueueLimitDeferred {
		t.Fatalf("drained shrink unexpectedly deferred: %+v", result)
	}
	if got := strings.Join(state.events, ","); got != "gso,class,leaf" {
		t.Fatalf("drained shrink mutation order = %q, want %q", got, "gso,class,leaf")
	}
}

func TestLinuxTUNAQMPostGSOWriteBacklogDefersLeafAndBurst(t *testing.T) {
	kernel, state := newLinuxTUNAQMShrinkRaceKernel(true)
	target := linuxTUNAQMShrinkRaceTarget()
	result, err := kernel.Apply(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(state.events, ","); got != "gso" {
		t.Fatalf("injected-backlog shrink mutations = %q, want only GSO", got)
	}
	if !result.QueueLimitDeferred || result.GSOLimitsDeferred {
		t.Fatalf("injected-backlog deferral = %+v, want leaf/burst held after applied GSO", result)
	}
	actual, err := kernel.Read()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDeferredTUNAQMReadback(target, actual, result); err != nil {
		t.Fatalf("injected-backlog held readback: %v", err)
	}

	state.events = nil
	state.backlog = false
	state.injectOnGSOWrite = false
	result, err = kernel.Apply(target)
	if err != nil {
		t.Fatal(err)
	}
	if result.QueueLimitDeferred || result.GSOLimitsDeferred {
		t.Fatalf("post-drain reconciliation remained deferred: %+v", result)
	}
	if got := strings.Join(state.events, ","); got != "class,leaf" {
		t.Fatalf("post-drain mutations = %q, want %q", got, "class,leaf")
	}
}

// Regression: a segment-count decrease can coincide with a larger atomic byte
// quantum. Ordering that mixed transition as a shrink exposes the larger skb
// before the leaf and burst have grown.
func TestLinuxTUNAQMMixedTransitionGrowsCapacityBeforeGSOSize(t *testing.T) {
	target := tunAQMTargetState{
		RateBytesPerSecond:  680_000,
		BurstBytes:          64_170,
		TxQueueLen:          tunAQMTxQueueLen,
		MTU:                 1395,
		QueueLimitBytes:     64_170,
		GSOMaxSize:          64_170,
		GSOMaxSegments:      46,
		AdmissionLimitBytes: 64_490,
	}
	installedBurst := 64_000
	installedLeaf := 64_000
	installedGSO := linkGSOLimits{MaxSize: 64_000, MaxSegments: 64}
	var events []string
	unsafeArrival := false

	kernel := &linuxTUNAQMKernel{name: "wanbond-test0"}
	kernel.readTxQueueLen = func() (int, error) {
		return tunAQMTxQueueLen, nil
	}
	kernel.writeTxQueueLen = func(int) error {
		return nil
	}
	kernel.readGSOLimits = func() (linkGSOLimits, error) {
		return installedGSO, nil
	}
	kernel.writeGSOLimits = func(limits linkGSOLimits) error {
		events = append(events, "gso")
		if installedBurst < int(limits.MaxSize) ||
			installedLeaf < int(limits.MaxSize) {
			unsafeArrival = true
		}
		installedGSO = limits
		return nil
	}
	kernel.command = func(args ...string) ([]byte, error) {
		switch {
		case len(args) >= 3 && args[0] == "-j" &&
			args[1] == "-s" && args[2] == "qdisc":
			return []byte(fmt.Sprintf(`[
				{"kind":"htb","root":true,"handle":"1:"},
				{"kind":"bfifo","parent":"1:1","handle":"10:",
				 "options":{"limit":%d}}
			]`, installedLeaf)), nil
		case len(args) >= 2 && args[0] == "class" && args[1] == "show":
			return []byte(fmt.Sprintf(
				"class htb 1:1 root rate 5440000bit ceil 5440000bit burst %db cburst %db",
				installedBurst,
				installedBurst,
			)), nil
		case len(args) >= 2 && args[0] == "class" && args[1] == "replace":
			events = append(events, "class")
			installedBurst = target.BurstBytes
			return nil, nil
		case len(args) > 0 && args[0] == "qdisc":
			events = append(events, "leaf")
			installedLeaf = target.QueueLimitBytes
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected tc command: %v", args)
		}
	}

	result, err := kernel.Apply(target)
	if err != nil {
		t.Fatal(err)
	}
	if result.QueueLimitDeferred || result.GSOLimitsDeferred {
		t.Fatalf("mixed capacity growth unexpectedly deferred: %+v", result)
	}
	if unsafeArrival {
		t.Error("larger GSO quantum became visible before leaf and burst growth")
	}
	if got := strings.Join(events, ","); got != "class,leaf,gso" {
		t.Fatalf("mixed GSO transition order = %q, want %q", got, "class,leaf,gso")
	}
	actual, err := kernel.Read()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTUNAQMReadback(target, actual); err != nil {
		t.Fatalf("mixed GSO transition readback: %v", err)
	}
}

func TestLinuxTUNAQMSegmentOnlyChangeDoesNotDeferAtomicCapacity(t *testing.T) {
	var qdiscCommands [][]string
	kernel := testLinuxTUNAQMKernel(t, `[
		{"kind":"htb","root":true,"handle":"1:"},
		{"kind":"bfifo","parent":"1:1","handle":"10:","qlen":1,"backlog":1000,
		 "options":{"limit":13950}}
	]`, &qdiscCommands)
	gsoWrites := 0
	kernel.writeGSOLimits = func(limits linkGSOLimits) error {
		gsoWrites++
		if limits.MaxSize != 13_950 || limits.MaxSegments != 9 {
			t.Fatalf("segment-only GSO write = %+v", limits)
		}
		return nil
	}
	target := tunAQMTargetState{
		RateBytesPerSecond:  680_000,
		BurstBytes:          13_950,
		TxQueueLen:          tunAQMTxQueueLen,
		MTU:                 1395,
		QueueLimitBytes:     13_950,
		GSOMaxSize:          13_950,
		GSOMaxSegments:      9,
		AdmissionLimitBytes: 14_270,
	}
	result, err := kernel.Apply(target)
	if err != nil {
		t.Fatal(err)
	}
	if result.GSOLimitsDeferred || result.QueueLimitDeferred ||
		gsoWrites != 1 || len(qdiscCommands) != 0 {
		t.Fatalf(
			"segment-only transition = %+v GSO writes=%d qdisc=%v, want immediate isolated GSO write",
			result,
			gsoWrites,
			qdiscCommands,
		)
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
	gsoLimits := linkGSOLimits{MaxSize: 13_950, MaxSegments: 10}
	kernel := &linuxTUNAQMKernel{name: "wanbond-test0"}
	kernel.readTxQueueLen = func() (int, error) {
		return tunAQMTxQueueLen, nil
	}
	kernel.writeTxQueueLen = func(int) error {
		return nil
	}
	kernel.readGSOLimits = func() (linkGSOLimits, error) {
		return gsoLimits, nil
	}
	kernel.writeGSOLimits = func(limits linkGSOLimits) error {
		gsoWrites++
		gsoLimits = limits
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

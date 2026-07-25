package bind

import (
	"bytes"
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/shaper"
)

func TestRecoveryServiceFirstMiddleLastLossCompletesBeforeA(t *testing.T) {
	const (
		kdata = 3
		mmax  = 1
		lmax  = 100
		rate  = 10_000.0
		rp    = 1_000.0
	)
	clock := newPriorityDebtClock()
	fecConfig := fec.Config{
		DataShards:   kdata,
		ParityShards: mmax,
		Deadline:     50 * time.Millisecond,
	}
	encoder, err := fec.NewEncoder(fecConfig, clock)
	if err != nil {
		t.Fatal(err)
	}
	var data []fec.DataShard
	var parity []fec.ParityShard
	maxFECInnerDatagram := lmax - frame.DataOverhead - FECParityMTUPenalty
	lc := 8 + maxFECInnerDatagram
	ls := 4 + lc
	codedInputOwnership := kdata * lc
	workspaceOwnership := (kdata + mmax) * ls
	encodedWireOwnership := kdata*(frame.DataOverhead+maxFECInnerDatagram) + mmax*lmax
	fgroup := codedInputOwnership + workspaceOwnership + encodedWireOwnership
	original := make([][]byte, kdata)
	for i := range original {
		original[i] = bytes.Repeat([]byte{byte(i + 1)}, lc)
		shard, produced, err := encoder.Admit(original[i])
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, shard)
		if len(produced) > 0 {
			parity = produced
		}
	}
	if len(parity) != mmax {
		t.Fatalf("encoder parity = %d, want %d", len(parity), mmax)
	}
	measuredCodedInput := 0
	for _, shard := range data {
		measuredCodedInput += len(shard.Payload)
	}
	if measuredCodedInput != codedInputOwnership {
		t.Fatalf("coded-input ownership = %d, want K*Lc=%d", measuredCodedInput, codedInputOwnership)
	}
	if len(parity[0].Payload) != ls {
		t.Fatalf("Reed-Solomon shard length = %d, want Ls=%d", len(parity[0].Payload), ls)
	}
	measuredWorkspace := (len(data) + len(parity)) * len(parity[0].Payload)
	if measuredWorkspace != workspaceOwnership {
		t.Fatalf("Reed-Solomon workspace = %d, want (K+M)*Ls=%d", measuredWorkspace, workspaceOwnership)
	}

	config := shaper.Config{
		RateBytesPerSecond:         rate,
		PriorityRateBytesPerSecond: rp,
		DataBudgetBytes:            3 * lmax,
		ControlReserveBytes:        lmax,
		MaxDatagramBytes:           lmax,
		PriorityBurstBytes:         lmax,
		PriorityReserveBytes:       lmax,
		FECGroupReserveBytes:       fgroup,
		RecoveryWriteSlack:         10 * time.Millisecond,
	}
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var firstOnce sync.Once
	var releaseOnce sync.Once
	writer, err := shaper.New(config, clock, func([]byte) error {
		firstOnce.Do(func() {
			close(firstStarted)
			<-releaseFirst
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		releaseOnce.Do(func() { close(releaseFirst) })
		_ = writer.Close()
	}()

	dataPrefixDone := make(chan error, 1)
	go func() {
		dataPrefixDone <- writer.WriteBatch(context.Background(), shaper.ClassData, [][]byte{
			bytes.Repeat([]byte{0xA1}, lmax),
			bytes.Repeat([]byte{0xA2}, lmax),
			bytes.Repeat([]byte{0xA3}, lmax),
			bytes.Repeat([]byte{0xA4}, lmax),
		})
	}()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("Lio prefix did not reach the writer")
	}
	controlDone := make(chan error, 1)
	go func() {
		controlDone <- writer.WriteBatch(context.Background(), shaper.ClassControl, [][]byte{
			bytes.Repeat([]byte{0xC1}, lmax),
		})
	}()
	waitForRecoveryCondition(t, func() bool {
		return writer.Snapshot().QueueControlBytes == lmax
	})
	admitted, priorityDone, err := writer.TryWritePriority(bytes.Repeat([]byte{0xE1}, lmax), nil)
	if err != nil || !admitted {
		t.Fatalf("priority admission = %v, %v", admitted, err)
	}
	waitForRecoveryCondition(t, func() bool {
		return writer.Snapshot().AcceptedBytes == uint64(
			4*lmax+lmax+lmax,
		)
	})

	var emittedMu sync.Mutex
	var emitted []fec.Shard
	var trancheComplete time.Time
	groupDatagrams := make([]shaper.Datagram, 0, kdata+mmax)
	for _, shard := range data {
		shard := shard
		groupDatagrams = append(groupDatagrams, shaper.Datagram{
			Class: shaper.ClassData,
			Payload: bytes.Repeat(
				[]byte{byte(0x10 + shard.Index)},
				frame.DataOverhead+maxFECInnerDatagram,
			),
			Write: func([]byte) error {
				emittedMu.Lock()
				emitted = append(emitted, shard)
				trancheComplete = clock.Now()
				emittedMu.Unlock()
				return nil
			},
		})
	}
	for _, shard := range parity {
		shard := shard
		groupDatagrams = append(groupDatagrams, shaper.Datagram{
			Class:   shaper.ClassData,
			Payload: bytes.Repeat([]byte{byte(0x20 + shard.Index)}, lmax),
			Write: func([]byte) error {
				emittedMu.Lock()
				emitted = append(emitted, shard)
				trancheComplete = clock.Now()
				emittedMu.Unlock()
				return nil
			},
		})
	}
	measuredEncodedWire := 0
	for _, datagram := range groupDatagrams {
		measuredEncodedWire += len(datagram.Payload)
	}
	if measuredEncodedWire != encodedWireOwnership {
		t.Fatalf("encoded-wire ownership = %d, want K*Ldata+M*Lparity=%d",
			measuredEncodedWire, encodedWireOwnership)
	}
	deadlineInstalled := make(chan time.Time, 1)
	groupDone := make(chan struct {
		result shaper.BatchResult
		err    error
	}, 1)
	go func() {
		result, err := writer.WriteRecovery(context.Background(), groupDatagrams, shaper.RecoveryControl{
			Enabled: true,
			InstallDeadline: func(deadline time.Time) error {
				deadlineInstalled <- deadline
				return nil
			},
			ClearDeadline: func() error { return nil },
			Abort:         func(error) {},
		})
		groupDone <- struct {
			result shaper.BatchResult
			err    error
		}{result: result, err: err}
	}()
	var installed time.Time
	select {
	case installed = <-deadlineInstalled:
	case <-time.After(time.Second):
		t.Fatal("recovery cut did not install its absolute deadline")
	}

	start := clock.Now()
	cutStart := start.Add(6 * 10 * time.Millisecond)
	if want := cutStart.Add(config.RecoveryWriteSlack); !installed.Equal(want) {
		t.Fatalf("installed deadline = %s, want cutStart+I = %s", installed, want)
	}
	releaseOnce.Do(func() { close(releaseFirst) })
	clock.advanceWithoutFiring(cutStart.Sub(start))
	clock.fireDue()
	var outcome struct {
		result shaper.BatchResult
		err    error
	}
	select {
	case outcome = <-groupDone:
	case <-time.After(time.Second):
		t.Fatal("recovery tranche did not complete")
	}
	if outcome.err != nil || outcome.result.Accepted != kdata+mmax || outcome.result.Emitted != kdata+mmax {
		t.Fatalf("recovery result = %+v, err=%v", outcome.result, outcome.err)
	}
	for _, done := range []<-chan error{dataPrefixDone, controlDone, priorityDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("saturated prefix did not drain")
		}
	}

	recoveryBytes := config.DataBudgetBytes +
		config.ControlReserveBytes +
		config.PriorityReserveBytes +
		(kdata+mmax+1)*config.MaxDatagramBytes
	boundA := time.Duration(math.Ceil(
		float64(recoveryBytes)/(rate-rp)*float64(time.Second),
	)) + config.RecoveryWriteSlack
	emittedMu.Lock()
	service := trancheComplete.Sub(start)
	captured := append([]fec.Shard(nil), emitted...)
	emittedMu.Unlock()
	if service > boundA {
		t.Fatalf("tranche service = %s, want <= A=%s", service, boundA)
	}
	if service >= 250*time.Millisecond {
		t.Fatalf("tranche service = %s reached the conservative 250ms receiver fallback", service)
	}

	for _, lost := range []int{0, 1, 2} {
		decoder, err := fec.NewDecoder(fecConfig)
		if err != nil {
			t.Fatal(err)
		}
		var recovered []fec.Recovered
		for _, shard := range captured {
			if native, ok := shard.(fec.DataShard); ok && native.Index == lost {
				continue
			}
			got, err := decoder.Offer(shard)
			if err != nil {
				t.Fatalf("loss index %d: decoder offer: %v", lost, err)
			}
			recovered = append(recovered, got...)
		}
		if len(recovered) != 1 ||
			recovered[0].Index != lost ||
			!bytes.Equal(recovered[0].Payload, original[lost]) {
			t.Fatalf("loss index %d recovered = %+v, want exact native shard", lost, recovered)
		}
	}
}

func waitForRecoveryCondition(t testing.TB, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("recovery fixture condition did not become true")
		}
		time.Sleep(time.Millisecond)
	}
}

package bind

import (
	"bytes"
	"fmt"
	"io"
	"net/netip"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/log"
)

func TestProductionBatch128CappedShapedSerialAdmissionFillsGroups(t *testing.T) {
	const (
		dataShards   = 4
		parityShards = 1
		rateBytes    = 100_000
	)
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	psk := testKey(t, 0xDA)
	shaperConfig := config.PathShaperConfig{
		RateBytesPerSecond:      rateBytes,
		DataBurstBytes:          1472,
		ControlReserveBytes:     1472,
		MaxEncodedDatagramBytes: 1472,
		ProbeRateBytesPerSecond: 1,
		ProbeBurstBytes:         2944,
		PriorityReserveBytes:    2944,
		FECGroupReserveBytes:    (dataShards + parityShards) * 1472,
		RecoveryWriteSlack:      100 * time.Millisecond,
	}
	m, err := NewMultipathWithShapers(
		loopbackPaths(1),
		psk,
		&unpacedSelectionRecorder{},
		nil,
		nil,
		&fec.Config{DataShards: dataShards, ParityShards: parityShards, Deadline: testFECDeadline},
		nil,
		config.Amnezia{},
		[]config.PathShaperConfig{shaperConfig},
		lg,
	)
	if err != nil {
		t.Fatal(err)
	}
	m.clock = newFakeClock()
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if got := m.BatchSize(); got != 128 {
		t.Fatalf("production BatchSize = %d, want 128", got)
	}
	path := m.paths[0]
	path.setRemote(netip.MustParseAddrPort("127.0.0.1:9"))

	codec := mustFrameCodec(t, psk)
	var writeMu sync.Mutex
	var wire []frame.Frame
	var writeTimes []time.Time
	var writeSizes []int
	path.writeUDP = func(payload []byte, _ netip.AddrPort) (int, error) {
		decoded, err := codec.Decode(payload)
		if err != nil {
			return 0, err
		}
		writeMu.Lock()
		wire = append(wire, decoded)
		writeTimes = append(writeTimes, time.Now())
		writeSizes = append(writeSizes, len(payload))
		writeMu.Unlock()
		return len(payload), nil
	}

	owner := m.fecSend.Load().owner
	admitted := make(chan fecOwnerAdmission, dataShards)
	owner.afterAdmit = func(got fecOwnerAdmission, _ *fecOwnerBatch, _ int) {
		select {
		case admitted <- got:
		default:
		}
	}
	serialPayloads := [][]byte{
		[]byte("serial-shaped-1"),
		[]byte("serial-shaped-2"),
		[]byte("serial-shaped-3"),
		[]byte("serial-shaped-4"),
	}
	wantSerial := make([][]byte, len(serialPayloads))
	for i := range serialPayloads {
		wantSerial[i] = append([]byte(nil), serialPayloads[i]...)
	}
	returned := make(chan int, dataShards)
	continueSerial := make(chan struct{})
	serialDone := make(chan error, 1)
	go func() {
		for i := range serialPayloads {
			if err := m.Send([][]byte{serialPayloads[i]}, m.virt); err != nil {
				serialDone <- fmt.Errorf("serial shaped Send #%d: %w", i+1, err)
				return
			}
			for j := range serialPayloads[i] {
				serialPayloads[i][j] = byte('A' + i)
			}
			returned <- i + 1
			if i < len(serialPayloads)-1 {
				<-continueSerial
			}
		}
		serialDone <- nil
	}()
	var serialGroup fec.GroupID
	var serialDue time.Time
	for i := 0; i < dataShards; i++ {
		var admission fecOwnerAdmission
		select {
		case admission = <-admitted:
		case <-time.After(5 * time.Second):
			t.Fatalf("serial shaped Send #%d did not reach owned admission", i+1)
		}
		if i == 0 {
			serialGroup = admission.group
			serialDue = admission.due
		} else if admission.group != serialGroup {
			t.Fatalf("serial admission #%d group = %d, want %d", i+1, admission.group, serialGroup)
		}
		if admission.index != i {
			t.Fatalf("serial admission #%d FEC index = %d, want %d", i+1, admission.index, i)
		}
		if i < dataShards-1 && (admission.due.IsZero() || admission.due != serialDue) {
			t.Fatalf("serial admission #%d deadline = %v, want shared nonzero %v", i+1, admission.due, serialDue)
		}
		if i == dataShards-1 && !admission.due.IsZero() {
			t.Fatalf("size-closing serial admission deadline = %v, want zero", admission.due)
		}
		select {
		case got := <-returned:
			if got != i+1 {
				t.Fatalf("serial shaped Send return order = %d, want %d", got, i+1)
			}
		case <-time.After(250 * time.Millisecond):
			t.Fatalf("serial shaped Send #%d still waits for terminal group completion after owned admission", i+1)
		}
		if i < dataShards-1 {
			writeMu.Lock()
			gotWire := len(wire)
			writeMu.Unlock()
			if gotWire != 0 {
				t.Fatalf("serial shaped Send #%d exposed %d wire frames before its group decision", i+1, gotWire)
			}
			continueSerial <- struct{}{}
		}
	}
	if err := <-serialDone; err != nil {
		t.Fatal(err)
	}

	waitForProductionBatchDrain(t, m, dataShards, parityShards, 1)
	serialStats := m.PeerSnapshots()[0].FEC
	if serialStats.GroupDecisions != 1 || serialStats.DeadlineDecisions != 0 {
		t.Fatalf("serial group decisions/deadline decisions = %d/%d, want 1/0",
			serialStats.GroupDecisions, serialStats.DeadlineDecisions)
	}
	writeMu.Lock()
	for i := 0; i < dataShards; i++ {
		decoded, ok := wire[i].(frame.Data)
		if !ok || !bytes.Equal(decoded.Payload, wantSerial[i]) {
			writeMu.Unlock()
			t.Fatalf("serial wire DATA #%d = %#v, want caller-owned payload %q", i+1, wire[i], wantSerial[i])
		}
	}
	writeMu.Unlock()

	batch := payloadStream(m.BatchSize())
	if err := m.Send(batch, m.virt); err != nil {
		t.Fatalf("production shaped batch-128 Send: %v", err)
	}
	totalGroups := 1 + m.BatchSize()/dataShards
	waitForProductionBatchDrain(t, m, dataShards, parityShards, totalGroups)

	snapshot := m.PeerSnapshots()[0]
	wantData := dataShards + m.BatchSize()
	wantParity := totalGroups * parityShards
	wantFrames := wantData + wantParity
	if snapshot.FEC.DataFrames != uint64(wantData) ||
		snapshot.FEC.ParityFrames != uint64(wantParity) ||
		snapshot.FEC.GroupDecisions != uint64(totalGroups) ||
		snapshot.FEC.DeadlineDecisions != 0 ||
		snapshot.FEC.DeadlineMisses != 0 {
		t.Fatalf("production batch FEC DATA/PARITY/groups/deadline/misses = %d/%d/%d/%d/%d, want %d/%d/%d/0/0",
			snapshot.FEC.DataFrames,
			snapshot.FEC.ParityFrames,
			snapshot.FEC.GroupDecisions,
			snapshot.FEC.DeadlineDecisions,
			snapshot.FEC.DeadlineMisses,
			wantData,
			wantParity,
			totalGroups,
		)
	}
	pathSnapshot := snapshot.Paths[0]
	if pathSnapshot.ShaperAcceptedDatagrams != uint64(wantFrames) ||
		pathSnapshot.ShaperEmittedDatagrams != uint64(wantFrames) ||
		pathSnapshot.ShaperWriteErrors != 0 ||
		pathSnapshot.SocketWriteErrors != 0 ||
		path.emsgsizeDrops.Load() != 0 {
		t.Fatalf("production shaped conservation accepted/emitted/shaper-errors/socket-errors/EMSGSIZE = %d/%d/%d/%d/%d, want %d/%d/0/0/0",
			pathSnapshot.ShaperAcceptedDatagrams,
			pathSnapshot.ShaperEmittedDatagrams,
			pathSnapshot.ShaperWriteErrors,
			pathSnapshot.SocketWriteErrors,
			path.emsgsizeDrops.Load(),
			wantFrames,
			wantFrames,
		)
	}
	if pathSnapshot.Shaper == nil {
		t.Fatal("production path omitted exact-byte shaper snapshot")
	}
	if pathSnapshot.Shaper.AcceptedBytes != pathSnapshot.Shaper.EmittedBytes ||
		pathSnapshot.Shaper.AdmissionCanceledDatagrams != 0 ||
		pathSnapshot.Shaper.AsyncWriteErrors != 0 ||
		pathSnapshot.Shaper.AsyncWriteEMSGSIZEErrors != 0 {
		t.Fatalf("production shaper bytes/cancellations/async-errors = %d/%d/%d/%d/%d, want accepted=emitted and zero terminal errors",
			pathSnapshot.Shaper.AcceptedBytes,
			pathSnapshot.Shaper.EmittedBytes,
			pathSnapshot.Shaper.AdmissionCanceledDatagrams,
			pathSnapshot.Shaper.AsyncWriteErrors,
			pathSnapshot.Shaper.AsyncWriteEMSGSIZEErrors,
		)
	}

	writeMu.Lock()
	defer writeMu.Unlock()
	if len(wire) != wantFrames || len(writeTimes) != wantFrames || len(writeSizes) != wantFrames {
		t.Fatalf("wire/count samples = %d/%d/%d, want %d", len(wire), len(writeTimes), len(writeSizes), wantFrames)
	}
	for group := 0; group < totalGroups; group++ {
		if err := checkProductionGroup(wire, uint32(group), dataShards, parityShards); err != nil {
			t.Fatal(err)
		}
	}
	scheduledBytes := 0
	for _, size := range writeSizes[:len(writeSizes)-1] {
		scheduledBytes += size
	}
	elapsed := writeTimes[len(writeTimes)-1].Sub(writeTimes[0])
	minElapsed := time.Duration(float64(scheduledBytes) / rateBytes * float64(time.Second))
	if elapsed < minElapsed*4/5 {
		t.Fatalf("capped writer elapsed = %v for %d scheduled bytes at %d B/s, want at least %v",
			elapsed, scheduledBytes, rateBytes, minElapsed*4/5)
	}
	observedRate := float64(scheduledBytes) / elapsed.Seconds()
	t.Logf("production batch-128 shaped evidence: serial_calls=%d batch_buffers=%d DATA=%d PARITY=%d groups=%d deadline_decisions=0 accepted=emitted=%d wire_bytes=%d elapsed=%v observed_rate=%.0fB/s configured_rate=%dB/s",
		dataShards,
		m.BatchSize(),
		wantData,
		wantParity,
		totalGroups,
		wantFrames,
		pathSnapshot.Shaper.EmittedBytes,
		elapsed,
		observedRate,
		rateBytes,
	)
}

func waitForProductionBatchDrain(
	t testing.TB,
	m *Multipath,
	dataShards int,
	parityShards int,
	groups int,
) {
	t.Helper()
	wantData := groups * dataShards
	wantParity := groups * parityShards
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := m.PeerSnapshots()[0]
		if snapshot.FEC.DataFrames == uint64(wantData) &&
			snapshot.FEC.ParityFrames == uint64(wantParity) &&
			snapshot.FEC.StagedGroups == 0 &&
			snapshot.FEC.StagedDataFrames == 0 {
			return
		}
		runtime.Gosched()
	}
	snapshot := m.PeerSnapshots()[0].FEC
	t.Fatalf("production shaped drain DATA/PARITY/staged = %d/%d/%d:%d, want %d/%d/0:0",
		snapshot.DataFrames,
		snapshot.ParityFrames,
		snapshot.StagedGroups,
		snapshot.StagedDataFrames,
		wantData,
		wantParity,
	)
}

func checkProductionGroup(wire []frame.Frame, group uint32, wantData, wantParity int) error {
	data := 0
	parity := 0
	for _, decoded := range wire {
		switch decoded := decoded.(type) {
		case frame.Data:
			if decoded.FECGroup == group {
				data++
			}
		case frame.Parity:
			if decoded.FECGroup == group {
				if int(decoded.DataCount) != wantData {
					return fmt.Errorf("group %d parity DataCount = %d, want %d", group, decoded.DataCount, wantData)
				}
				parity++
			}
		}
	}
	if data != wantData || parity != wantParity {
		return fmt.Errorf("group %d DATA/PARITY = %d/%d, want %d/%d", group, data, parity, wantData, wantParity)
	}
	return nil
}

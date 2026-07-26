package bind

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/shaper"
	"github.com/7mind/wanbond/internal/telemetry"
)

func TestUnshapedSingleOwnerFECAdvertisesDirectRecoveryCut(t *testing.T) {
	clock := newFakeClock()
	m, _ := newProbingMultipathFEC(
		t,
		loopbackPaths(1),
		testKey(t, 0xE8),
		&fec.Config{
			DataShards:   3,
			ParityShards: 1,
			Deadline:     5 * time.Millisecond,
		},
		clock,
	)
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	message, recognized, err := telemetry.DecodeRecoveryContract(m.contracts.payload())
	if err != nil || !recognized {
		t.Fatalf("decode unshaped recovery contract = recognized %v err %v", recognized, err)
	}
	if !message.Enabled {
		t.Fatalf("single-owner unshaped FEC contract = %+v, want deadline-bounded direct recovery offer", message)
	}
	if message.ServiceBound != 10*time.Millisecond {
		t.Fatalf("direct recovery A = %s, want absolute socket-cut bound 10ms", message.ServiceBound)
	}
}

func openDirectRecoveryReviewBind(t *testing.T) *Multipath {
	t.Helper()
	clock := newFakeClock()
	m, _ := newProbingMultipathFEC(
		t,
		loopbackPaths(1),
		testKey(t, 0xE9),
		&fec.Config{
			DataShards:   3,
			ParityShards: 1,
			Deadline:     5 * time.Millisecond,
		},
		clock,
	)
	m.clock = clock
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	acknowledgeCurrentContract(t, m.contracts, m.paths[0].id, 1)
	m.paths[0].setRemote(netip.MustParseAddrPort("127.0.0.1:9"))
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func directRecoveryReviewWrites(path *peerPathState, payloads ...string) []fecPreparedWrite {
	remote, _ := path.getRemote()
	writes := make([]fecPreparedWrite, len(payloads))
	for index, payload := range payloads {
		writes[index] = fecPreparedWrite{
			path:   path,
			remote: remote,
			class:  shaper.ClassData,
			wire:   fecWire{b: []byte(payload), parity: index == len(payloads)-1},
		}
	}
	return writes
}

func TestDirectRecoveryCutOwnsSocketDeadlineAndWriteOrder(t *testing.T) {
	m := openDirectRecoveryReviewBind(t)
	path := m.paths[0]
	owner := recoveryReviewOwner(m)

	var mu sync.Mutex
	var deadlines []time.Time
	var order []string
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	path.setWriteDeadline = func(deadline time.Time) error {
		mu.Lock()
		deadlines = append(deadlines, deadline)
		mu.Unlock()
		return nil
	}
	path.writeUDP = func(payload []byte, _ netip.AddrPort) (int, error) {
		if string(payload) == "data-0" {
			close(firstStarted)
			<-releaseFirst
		}
		mu.Lock()
		order = append(order, string(payload))
		mu.Unlock()
		return len(payload), nil
	}

	cutDone := make(chan error, 1)
	go func() {
		cutDone <- owner.emit(7, directRecoveryReviewWrites(path, "data-0", "data-1", "parity"))
	}()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("direct recovery cut did not reach its first write")
	}

	ordinaryDone := make(chan error, 1)
	go func() {
		_, err := path.writeToUDPAddrPort([]byte("ordinary"), netip.MustParseAddrPort("127.0.0.1:9"))
		ordinaryDone <- err
	}()
	select {
	case err := <-ordinaryDone:
		t.Fatalf("ordinary write interleaved with active recovery cut: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseFirst)
	if err := <-cutDone; err != nil {
		t.Fatalf("direct recovery cut: %v", err)
	}
	if err := <-ordinaryDone; err != nil {
		t.Fatalf("ordinary write after cut: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	wantOrder := []string{"data-0", "data-1", "parity", "ordinary"}
	if len(order) != len(wantOrder) {
		t.Fatalf("socket write order = %v, want %v", order, wantOrder)
	}
	for index := range wantOrder {
		if order[index] != wantOrder[index] {
			t.Fatalf("socket write order = %v, want %v", order, wantOrder)
		}
	}
	if len(deadlines) != 2 ||
		deadlines[0] != m.clock.Now().Add(10*time.Millisecond) ||
		!deadlines[1].IsZero() {
		t.Fatalf("socket deadlines = %v, want [cutStart+10ms, zero]", deadlines)
	}
}

func TestDirectRecoveryCutFailureClearsDeadlineAndRetiresGeneration(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		failInstall bool
		failWrite   bool
		failClear   bool
	}{
		{name: "install", failInstall: true},
		{name: "write", failWrite: true},
		{name: "clear", failClear: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			m := openDirectRecoveryReviewBind(t)
			path := m.paths[0]
			owner := recoveryReviewOwner(m)
			sentinel := errors.New("direct recovery failure")

			var mu sync.Mutex
			var deadlines []time.Time
			writes := 0
			path.setWriteDeadline = func(deadline time.Time) error {
				mu.Lock()
				deadlines = append(deadlines, deadline)
				mu.Unlock()
				if testCase.failInstall && !deadline.IsZero() {
					return sentinel
				}
				if testCase.failClear && deadline.IsZero() {
					return sentinel
				}
				return nil
			}
			path.writeUDP = func(payload []byte, _ netip.AddrPort) (int, error) {
				mu.Lock()
				defer mu.Unlock()
				writes++
				if testCase.failWrite && writes == 2 {
					return 0, sentinel
				}
				return len(payload), nil
			}

			err := owner.emit(8, directRecoveryReviewWrites(path, "data-0", "data-1", "parity"))
			if !errors.Is(err, sentinel) {
				t.Fatalf("direct recovery error = %v, want %v", err, sentinel)
			}
			waitForReviewCondition(t, func() bool {
				return path.recoveryFailed.Load()
			})
			clock := m.clock.(*fakeClock)
			timerArmed := false
			waitForRecoveryCondition(t, func() bool {
				m.mu.Lock()
				retired := len(m.paths) == 0
				m.mu.Unlock()
				clock.mu.Lock()
				defer clock.mu.Unlock()
				for timer := range clock.timers {
					if timer.armed {
						timerArmed = true
						return true
					}
				}
				return retired
			})
			if timerArmed {
				clock.advance(conservativeRecoveryService)
			}
			waitForRecoveryCondition(t, func() bool {
				m.mu.Lock()
				defer m.mu.Unlock()
				return len(m.paths) == 0
			})
			if _, err := path.writeToUDPAddrPort(
				[]byte("after-failure"),
				netip.MustParseAddrPort("127.0.0.1:9"),
			); !errors.Is(err, net.ErrClosed) {
				t.Fatalf("write after recovery retirement = %v, want net.ErrClosed", err)
			}

			mu.Lock()
			defer mu.Unlock()
			if testCase.failInstall {
				if len(deadlines) != 1 || deadlines[0].IsZero() || writes != 0 {
					t.Fatalf("install failure deadlines=%v writes=%d, want one nonzero deadline and no writes", deadlines, writes)
				}
			} else if len(deadlines) != 2 || deadlines[0].IsZero() || !deadlines[1].IsZero() {
				t.Fatalf("terminal deadlines = %v, want [nonzero, zero]", deadlines)
			}
		})
	}
}

func TestSharedUnshapedSocketDoesNotAdvertiseDirectRecovery(t *testing.T) {
	shared := &sharedPathState{name: "shared"}
	first := &peerPathState{
		sharedPathState: shared,
		directRecovery:  true,
		recoveryBound:   10 * time.Millisecond,
	}
	second := &peerPathState{sharedPathState: shared, directRecovery: true}
	shared.addViewLocked(first)
	shared.addViewLocked(second)
	if contract := first.localRecoveryContract(); contract.Enabled {
		t.Fatalf("shared unshaped socket contract = %+v, want disabled", contract)
	}
}

func TestFieldRecoveryGeometryUsesFastWindow(t *testing.T) {
	const fieldSRTT = 42*time.Millisecond + 957*time.Microsecond + 109*time.Nanosecond
	headroom := recoveryRTTHeadroom(fieldSRTT)
	got := recoveryWindow(10*time.Millisecond, headroom)
	want := 181*time.Millisecond + 828*time.Microsecond + 436*time.Nanosecond
	if got != want {
		t.Fatalf("field recovery window = %s, want A+4*SRTT = %s", got, want)
	}
	if got >= conservativeRecoveryService {
		t.Fatalf("field recovery window = %s, want below conservative fallback %s",
			got, conservativeRecoveryService)
	}
}

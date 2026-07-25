//go:build failfirst

package bind

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/shaper"
	"go.uber.org/goleak"
)

const failFirstDispatchSLO = 10 * time.Millisecond

type failFirstWriter struct {
	mu               sync.Mutex
	writes           int
	calls            int
	callSizes        []int
	groupOpenAtWrite bool
	encoder          *fec.Encoder
	blockCall        int
	failCall         int
	entered          chan struct{}
	release          chan struct{}
	enterOnce        sync.Once
	releaseOnce      sync.Once
}

func (w *failFirstWriter) WriteDatagrams(_ context.Context, datagrams []shaper.Datagram) (shaper.BatchResult, error) {
	w.mu.Lock()
	w.calls++
	call := w.calls
	w.callSizes = append(w.callSizes, len(datagrams))
	firstWrite := w.writes == 0
	w.writes += len(datagrams)
	_, open := w.encoder.NextDeadline()
	if firstWrite {
		w.groupOpenAtWrite = open
	}
	block := call == w.blockCall
	fail := call == w.failCall
	w.mu.Unlock()
	if block {
		w.enterOnce.Do(func() { close(w.entered) })
		<-w.release
	}
	if fail {
		return shaper.BatchResult{Accepted: len(datagrams), FailedIndex: 0}, errors.New("failfirst terminal writer error")
	}
	return shaper.BatchResult{Accepted: len(datagrams), Emitted: len(datagrams), FailedIndex: -1}, nil
}

func (*failFirstWriter) AccountPriority(int) error { return nil }
func (w *failFirstWriter) Close() error {
	if w.release != nil {
		w.releaseOnce.Do(func() { close(w.release) })
	}
	return nil
}

func (w *failFirstWriter) releaseBlocked() {
	w.releaseOnce.Do(func() { close(w.release) })
}

func (w *failFirstWriter) snapshot() (calls, writes int, callSizes []int, openAtFirstWrite bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls, w.writes, append([]int(nil), w.callSizes...), w.groupOpenAtWrite
}

func newFailFirstSender(t testing.TB, cfg fec.Config, clk *fakeClock, writer pathShaper) *Multipath {
	t.Helper()
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMultipathWithShapers(
		loopbackPaths(1),
		testKey(t, 0x35),
		&unpacedSelectionRecorder{},
		nil,
		nil,
		&cfg,
		nil,
		config.Amnezia{},
		[]config.PathShaperConfig{{
			RateBytesPerSecond:      10_000_000,
			DataBurstBytes:          1472,
			ControlReserveBytes:     1472,
			MaxEncodedDatagramBytes: 1472,
			ProbeRateBytesPerSecond: 1,
			ProbeBurstBytes:         2944,
		}},
		lg,
	)
	if err != nil {
		t.Fatal(err)
	}
	m.newPathShaper = func(shaper.Config, shaper.WriteFunc) (pathShaper, error) {
		return writer, nil
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	m.paths[0].setRemote(netip.MustParseAddrPort("127.0.0.1:9"))

	enc, err := fec.NewEncoder(cfg, clk)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.fecSend.Load().enc = enc
	m.mu.Unlock()
	return m
}

func TestFailFirstFECDataStagedUntilParityDecision(t *testing.T) {
	cfg := fec.Config{DataShards: 4, ParityShards: 1, Deadline: 80 * time.Millisecond}
	for _, parity := range []int{1, 0} {
		t.Run("parity-"+string(rune('0'+parity)), func(t *testing.T) {
			clk := newFakeClock()
			writer := &failFirstWriter{}
			m := newFailFirstSender(t, cfg, clk, writer)
			writer.encoder = m.fecSend.Load().enc
			writer.encoder.SetParity(parity)

			if err := m.Send([][]byte{[]byte("partial-group-data")}, m.virt); err != nil {
				t.Fatalf("Send: %v", err)
			}
			due, open := writer.encoder.NextDeadline()
			writer.mu.Lock()
			writes, openAtWrite := writer.writes, writer.groupOpenAtWrite
			writer.mu.Unlock()
			t.Logf("events: group-open=%v due=%s writer-visible=%d decision-ready=%v parity=%d",
				open, due.Sub(clk.Now()), writes, !openAtWrite, parity)
			if writes == 0 || !open {
				t.Fatalf("fixture did not observe an open partial group and DATA write: writes=%d open=%v", writes, open)
			}
			if openAtWrite {
				t.Fatalf("DATA became writer-visible while its FEC group remained open: immutable parity decision (M=%d) did not exist", parity)
			}
		})
	}
}

func TestFailFirstFECDeadlineDispatchBound(t *testing.T) {
	cfg := fec.Config{DataShards: 4, ParityShards: 1, Deadline: 80 * time.Millisecond}

	t.Run("periodic-phase-can-close-near-2D", func(t *testing.T) {
		clk := newFakeClock()
		writer := &failFirstWriter{}
		m := newFailFirstSender(t, cfg, clk, writer)
		enc := m.fecSend.Load().enc
		writer.encoder = enc

		clk.advance(time.Millisecond)
		if _, parity, err := enc.Admit([]byte("phase-offset")); err != nil || parity != nil {
			t.Fatalf("Admit: parity=%v err=%v", parity, err)
		}
		due, open := enc.NextDeadline()
		if !open {
			t.Fatal("fixture did not open an FEC group")
		}

		clk.advance(cfg.Deadline - time.Millisecond)
		m.fecFlushDeadline()
		if _, stillOpen := enc.NextDeadline(); !stillOpen {
			t.Fatal("phase fixture closed at the first periodic tick before Encoder.NextDeadline")
		}

		clk.advance(cfg.Deadline)
		m.fecFlushDeadline()
		_, stillOpen := enc.NextDeadline()
		decisionAt := clk.Now()
		t.Logf("events: group-open=true deadline-fire=2 decision-ready=%v opened-to-decision=%s bound=%s",
			!stillOpen, decisionAt.Sub(due.Add(-cfg.Deadline)), cfg.Deadline+failFirstDispatchSLO)
		if stillOpen {
			t.Fatal("fixture did not close the group at the second periodic tick")
		}
		if decisionAt.After(due.Add(failFirstDispatchSLO)) {
			t.Fatalf("periodic deadline dispatch produced the immutable parity decision at %s, after openedAt+D+G=%s",
				decisionAt.Sub(due.Add(-cfg.Deadline)), cfg.Deadline+failFirstDispatchSLO)
		}
	})

	t.Run("TryLock-contention-loses-due-close", func(t *testing.T) {
		clk := newFakeClock()
		writer := &failFirstWriter{}
		m := newFailFirstSender(t, cfg, clk, writer)
		enc := m.fecSend.Load().enc
		writer.encoder = enc

		if _, parity, err := enc.Admit([]byte("contended")); err != nil || parity != nil {
			t.Fatalf("Admit: parity=%v err=%v", parity, err)
		}
		due, open := enc.NextDeadline()
		if !open {
			t.Fatal("fixture did not open an FEC group")
		}
		clk.advance(cfg.Deadline)

		m.mu.Lock()
		m.fecFlushDeadline()
		m.mu.Unlock()
		clk.advance(failFirstDispatchSLO)

		_, stillOpen := enc.NextDeadline()
		t.Logf("events: group-open=true deadline-fire=1 contention=true retry=false decision-ready=%v now-minus-due=%s",
			!stillOpen, clk.Now().Sub(due))
		if stillOpen {
			t.Fatalf("contended deadline fire was skipped without retry; group remained open at openedAt+D+G (%s late)", clk.Now().Sub(due))
		}
	})
}

func TestFailFirstFECClosureAndLifecycleMatrix(t *testing.T) {
	t.Run("Kdata-size-close-orders-decision-before-group-write", func(t *testing.T) {
		cfg := fec.Config{DataShards: 2, ParityShards: 1, Deadline: 80 * time.Millisecond}
		clk := newFakeClock()
		writer := &failFirstWriter{}
		m := newFailFirstSender(t, cfg, clk, writer)
		writer.encoder = m.fecSend.Load().enc

		if err := m.Send([][]byte{[]byte("first"), []byte("second")}, m.virt); err != nil {
			t.Fatal(err)
		}
		calls, writes, sizes, openAtFirst := writer.snapshot()
		_, open := writer.encoder.NextDeadline()
		t.Logf("events: t=%s group-open=true first-writer-visible=true size-close=true decision-ready=%v calls=%d sizes=%v",
			clk.Now(), !open, calls, sizes)
		if calls != 2 || writes != 3 || len(sizes) != 2 || sizes[0] != 1 || sizes[1] != 2 || open {
			t.Fatalf("size-close fixture calls/writes/sizes/open = %d/%d/%v/%v, want 2/3/[1 2]/false", calls, writes, sizes, open)
		}
		if openAtFirst {
			t.Errorf("first DATA reached the writer before the Kdata size-close parity decision")
		}
	})

	t.Run("partial-deadline-close-egresses-through-bind-writer", func(t *testing.T) {
		cfg := fec.Config{DataShards: 4, ParityShards: 1, Deadline: 80 * time.Millisecond}
		clk := newFakeClock()
		writer := &failFirstWriter{}
		m := newFailFirstSender(t, cfg, clk, writer)
		writer.encoder = m.fecSend.Load().enc

		if err := m.Send([][]byte{[]byte("partial")}, m.virt); err != nil {
			t.Fatal(err)
		}
		due, open := writer.encoder.NextDeadline()
		if !open {
			t.Fatal("partial group did not open")
		}
		clk.advance(cfg.Deadline)
		t.Logf("event: t=%s deadline-fire due=%s", clk.Now(), due)
		m.fecFlushDeadline()
		calls, writes, sizes, _ := writer.snapshot()
		_, open = writer.encoder.NextDeadline()
		t.Logf("event: t=%s decision-ready=%v writer-visible calls=%d sizes=%v", clk.Now(), !open, calls, sizes)
		if calls != 2 || writes != 2 || len(sizes) != 2 || sizes[0] != 1 || sizes[1] != 1 || open {
			t.Fatalf("deadline-close fixture calls/writes/sizes/open = %d/%d/%v/%v, want 2/2/[1 1]/false", calls, writes, sizes, open)
		}
	})

	t.Run("contended-due-close-does-not-outrank-queued-Send", func(t *testing.T) {
		cfg := fec.Config{DataShards: 4, ParityShards: 1, Deadline: 80 * time.Millisecond}
		clk := newFakeClock()
		writer := &failFirstWriter{
			blockCall: 1,
			entered:   make(chan struct{}),
			release:   make(chan struct{}),
		}
		m := newFailFirstSender(t, cfg, clk, writer)
		writer.encoder = m.fecSend.Load().enc

		firstDone := make(chan error, 1)
		go func() { firstDone <- m.Send([][]byte{[]byte("executing")}, m.virt) }()
		<-writer.entered

		m.mu.Lock()
		clk.advance(cfg.Deadline)
		t.Logf("event: t=%s deadline-fire contention=true executing-Send=true", clk.Now())
		m.fecFlushDeadline()
		secondDone := make(chan error, 1)
		go func() { secondDone <- m.Send([][]byte{[]byte("queued")}, m.virt) }()
		select {
		case err := <-secondDone:
			m.mu.Unlock()
			t.Fatalf("queued Send completed while first Send and m.mu were blocked: %v", err)
		default:
		}
		m.mu.Unlock()

		writer.releaseBlocked()
		if err := <-firstDone; err != nil {
			t.Fatalf("first Send: %v", err)
		}
		if err := <-secondDone; err != nil {
			t.Fatalf("queued Send: %v", err)
		}
		clk.advance(failFirstDispatchSLO)
		calls, _, sizes, _ := writer.snapshot()
		_, open := writer.encoder.NextDeadline()
		t.Logf("event: t=%s retry=false queued-admission-writer-visible=true decision-ready=%v calls=%d sizes=%v",
			clk.Now(), !open, calls, sizes)
		if calls < 2 || !open {
			t.Fatalf("fixture did not expose queued admission after skipped close: calls=%d open=%v", calls, open)
		}
		t.Errorf("due close lost TryLock and failed to outrank a queued Send; group remained open at openedAt+D+G")
	})

	t.Run("terminal-writer-error-is-at-most-once", func(t *testing.T) {
		cfg := fec.Config{DataShards: 4, ParityShards: 1, Deadline: 80 * time.Millisecond}
		clk := newFakeClock()
		writer := &failFirstWriter{failCall: 2}
		m := newFailFirstSender(t, cfg, clk, writer)
		writer.encoder = m.fecSend.Load().enc
		if err := m.Send([][]byte{[]byte("data")}, m.virt); err != nil {
			t.Fatal(err)
		}
		clk.advance(cfg.Deadline)
		m.fecFlushDeadline()
		m.fecFlushDeadline()
		calls, _, sizes, _ := writer.snapshot()
		_, open := writer.encoder.NextDeadline()
		t.Logf("events: t=%s terminal-writer-error=true retry=false decision-ready=%v calls=%d sizes=%v", clk.Now(), !open, calls, sizes)
		if calls != 2 || open {
			t.Fatalf("terminal parity writer error calls/open = %d/%v, want 2/false (no duplicate retry)", calls, open)
		}
	})

	t.Run("Close-before-and-after-due-produce-no-late-parity", func(t *testing.T) {
		for _, closeAfterDue := range []bool{false, true} {
			t.Run(map[bool]string{false: "before", true: "after"}[closeAfterDue], func(t *testing.T) {
				cfg := fec.Config{DataShards: 4, ParityShards: 1, Deadline: 80 * time.Millisecond}
				clk := newFakeClock()
				writer := &failFirstWriter{}
				m := newFailFirstSender(t, cfg, clk, writer)
				writer.encoder = m.fecSend.Load().enc
				if err := m.Send([][]byte{[]byte("data")}, m.virt); err != nil {
					t.Fatal(err)
				}
				if closeAfterDue {
					clk.advance(cfg.Deadline)
					m.fecFlushDeadline()
				}
				if err := m.Close(); err != nil {
					t.Fatal(err)
				}
				clk.advance(cfg.Deadline)
				m.fecFlushDeadline()
				calls, _, sizes, _ := writer.snapshot()
				wantCalls := 1
				if closeAfterDue {
					wantCalls = 2
				}
				t.Logf("events: t=%s close-after-due=%v late-parity=false calls=%d sizes=%v", clk.Now(), closeAfterDue, calls, sizes)
				if calls != wantCalls {
					t.Fatalf("writer calls after Close = %d, want %d (no duplicate/late parity)", calls, wantCalls)
				}
			})
		}
	})
}

func TestFailFirstFECBlockedWaitersRetireWithoutLeak(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	cfg := fec.Config{DataShards: 4, ParityShards: 1, Deadline: 80 * time.Millisecond}
	clk := newFakeClock()
	writer := &failFirstWriter{
		blockCall: 1,
		entered:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	m := newFailFirstSender(t, cfg, clk, writer)
	writer.encoder = m.fecSend.Load().enc

	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() { firstDone <- m.Send([][]byte{[]byte("blocked")}, m.virt) }()
	<-writer.entered
	go func() { secondDone <- m.Send([][]byte{[]byte("waiter")}, m.virt) }()
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-firstDone; err != nil {
		t.Fatalf("in-flight Send after Close = %v, want completed admitted write", err)
	}
	if err := <-secondDone; !errors.Is(err, errClosed) {
		t.Fatalf("blocked waiter after Close = %v, want %v", err, errClosed)
	}
	t.Logf("events: t=%s close=true blocked-writer-resolved=true queued-waiter-resolved=true", clk.Now())
}

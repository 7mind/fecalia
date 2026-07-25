//go:build failfirst

package bind

import (
	"context"
	"io"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/shaper"
)

const failFirstDispatchSLO = 10 * time.Millisecond

type failFirstWriter struct {
	mu               sync.Mutex
	writes           int
	groupOpenAtWrite bool
	encoder          *fec.Encoder
}

func (w *failFirstWriter) WriteDatagrams(_ context.Context, datagrams []shaper.Datagram) (shaper.BatchResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes += len(datagrams)
	_, w.groupOpenAtWrite = w.encoder.NextDeadline()
	return shaper.BatchResult{Accepted: len(datagrams), Emitted: len(datagrams), FailedIndex: -1}, nil
}

func (*failFirstWriter) AccountPriority(int) error { return nil }
func (*failFirstWriter) Close() error              { return nil }

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

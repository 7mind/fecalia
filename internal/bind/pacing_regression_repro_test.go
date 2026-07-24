//go:build pacing_repro

package bind

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/sched"
)

// These opt-in tests preserve the executable RED evidence for D112 and D108 on
// main@040256256470eec5af976d5477c4deb24652d731, now as GREEN regressions against
// the replacement exact-byte shaper.

const (
	reproCapacityFPS = 100.0
	reproWindow      = 100 * time.Millisecond
)

type reproWireRecorder struct {
	mu         sync.Mutex
	frames     int
	bytes      int
	timestamps []time.Time
}

func (r *reproWireRecorder) add(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames++
	r.bytes += n
	r.timestamps = append(r.timestamps, time.Now())
}

func (r *reproWireRecorder) snapshot() (frames, bytes int, timestamps []time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.frames, r.bytes, append([]time.Time(nil), r.timestamps...)
}

func newPacingReproMultipath(t testing.TB, pacing bool, burst float64, fecCfg *fec.Config) (*Multipath, *sched.ActiveBackup, *fakeClock, *reproWireRecorder) {
	t.Helper()
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	clk := newFakeClock()
	schedulerCfg := sched.Config{FailbackAfter: time.Hour}
	scheduler, err := sched.NewActiveBackup([]sched.PathHealth{sched.AlwaysUp{}}, schedulerCfg, clk, lg)
	if err != nil {
		t.Fatalf("build active-backup scheduler: %v", err)
	}
	var m *Multipath
	if pacing {
		const lmax = 1472
		m, err = NewMultipathWithShapers(
			loopbackPaths(1),
			testKey(t, 0xB2),
			scheduler,
			nil,
			nil,
			fecCfg,
			nil,
			config.Amnezia{},
			[]config.PathShaperConfig{{
				RateBytesPerSecond:      reproCapacityFPS * lmax,
				DataBurstBytes:          int(burst * lmax),
				ControlReserveBytes:     lmax,
				MaxEncodedDatagramBytes: lmax,
				ProbeRateBytesPerSecond: 1,
				ProbeBurstBytes:         2 * lmax,
			}},
			lg,
		)
	} else {
		m, err = NewMultipath(loopbackPaths(1), testKey(t, 0xB2), scheduler, nil, nil, fecCfg, nil, config.Amnezia{}, lg)
	}
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen fake wire peer: %v", err)
	}
	recorder := &reproWireRecorder{}
	done := make(chan struct{})
	go func() {
		buf := make([]byte, maxDatagram)
		for {
			_ = peer.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
			n, _, err := peer.ReadFromUDPAddrPort(buf)
			if err == nil {
				recorder.add(n)
				continue
			}
			select {
			case <-done:
				return
			default:
			}
		}
	}()
	t.Cleanup(func() {
		close(done)
		_ = peer.Close()
	})
	m.paths[0].setRemote(peer.LocalAddr().(*net.UDPAddr).AddrPort())
	return m, scheduler, clk, recorder
}

func waitForReproFrames(t testing.TB, recorder *reproWireRecorder, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		frames, _, _ := recorder.snapshot()
		if frames >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	frames, bytes, timestamps := recorder.snapshot()
	t.Fatalf("fake wire recorder observed %d frames/%d bytes at %v, want at least %d frames", frames, bytes, timestamps, want)
}

func wgInitiationForRepro() []byte {
	b := make([]byte, wgTypeLen)
	binary.LittleEndian.PutUint32(b, wgMessageInitiationType)
	return b
}

func TestPacingLossPolicerRepro(t *testing.T) {
	t.Run("pacing-off-control", func(t *testing.T) {
		m, _, _, recorder := newPacingReproMultipath(t, false, 0, nil)
		if err := m.Send(payloadStream(3), m.virt); err != nil {
			t.Fatalf("pacing-off first batch: %v", err)
		}
		if err := m.Send(payloadStream(1), m.virt); err != nil {
			t.Fatalf("pacing-off second batch: %v", err)
		}
		waitForReproFrames(t, recorder, 4)
		frames, bytes, timestamps := recorder.snapshot()
		t.Logf("CONTROL pacing=off offered_frames=4 emitted_frames=%d emitted_wire_bytes=%d write_timestamps=%v", frames, bytes, timestamps)
	})

	t.Run("class-control-control", func(t *testing.T) {
		m, _, _, recorder := newPacingReproMultipath(t, true, 1, nil)
		if err := m.Send([][]byte{payloadStream(1)[0], wgInitiationForRepro()}, m.virt); err != nil {
			t.Fatalf("mixed DATA/ClassControl batch: %v", err)
		}
		waitForReproFrames(t, recorder, 2)
		frames, bytes, timestamps := recorder.snapshot()
		t.Logf("CONTROL ClassControl offered_frames=1 emitted_frames=%d emitted_wire_bytes=%d write_timestamps=%v", frames, bytes, timestamps)
	})

	t.Run("D112-sub-BDP-burst-waits-instead-of-shedding", func(t *testing.T) {
		const burstFrames = 3.0
		m, _, clk, recorder := newPacingReproMultipath(t, true, burstFrames, nil)
		if err := m.Send(payloadStream(3), m.virt); err != nil {
			t.Fatalf("seed three-frame batch: %v", err)
		}
		waitForReproFrames(t, recorder, 3)

		err := m.Send(payloadStream(1), m.virt)
		if err != nil {
			t.Fatalf("GREEN D112 fourth frame: %v", err)
		}
		waitForReproFrames(t, recorder, 4)
		frames, wireBytes, timestamps := recorder.snapshot()
		offeredFrames := 4
		averageOfferedFPS := float64(offeredFrames) / reproWindow.Seconds()
		t.Logf("GREEN D112 red_base=040256256470eec5af976d5477c4deb24652d731 fake_time=%s max_batch=3 "+
			"burst_frames=%.0f offered_frames=%d observation_window=%s average_offered_fps=%.1f capacity_fps=%.1f "+
			"emitted_frames=%d emitted_wire_bytes=%d write_timestamps=%v send_error=%v",
			clk.Now().Format(time.RFC3339Nano), burstFrames, offeredFrames, reproWindow, averageOfferedFPS,
			reproCapacityFPS, frames, wireBytes, timestamps, err)
		if averageOfferedFPS >= reproCapacityFPS {
			t.Fatalf("fixture error: average offered rate %.1f fps must stay below capacity %.1f fps", averageOfferedFPS, reproCapacityFPS)
		}
		if frames != offeredFrames {
			t.Fatalf("GREEN D112: emitted=%d, want all offered=%d retained and paced", frames, offeredFrames)
		}
	})

	t.Run("D108-size-parity-charges-at-egress", func(t *testing.T) {
		const burstFrames = float64(fecDataShards + 1)
		fecCfg := &fec.Config{DataShards: fecDataShards, ParityShards: fecParityShards, Deadline: testFECDeadline}
		m, scheduler, clk, recorder := newPacingReproMultipath(t, true, burstFrames, fecCfg)
		if err := m.Send(payloadStream(fecDataShards), m.virt); err != nil {
			t.Fatalf("send one size-closed FEC group: %v", err)
		}
		waitForReproFrames(t, recorder, fecDataShards+fecParityShards)
		fs := m.fecSend.Load()
		if fs == nil {
			t.Fatal("fixture error: FEC sender was not initialized")
		}
		if fs.parityFrames.Load() != fecParityShards {
			t.Fatalf("fixture error: size close wrote %d parity frames, want %d", fs.parityFrames.Load(), fecParityShards)
		}
		_ = scheduler
		frames, wireBytes, timestamps := recorder.snapshot()
		path := m.PeerSnapshots()[0].Paths[0]
		t.Logf("GREEN D108-size red_base=040256256470eec5af976d5477c4deb24652d731 fake_time=%s "+
			"data_frames=%d parity_frames=%d parity_bytes=%d parity_carry=%d emitted_frames=%d emitted_wire_bytes=%d "+
			"write_timestamps=%v shaper_accepted=%d shaper_emitted=%d",
			clk.Now().Format(time.RFC3339Nano), fs.dataFrames.Load(), fs.parityFrames.Load(), fs.parityBytes.Load(),
			m.parityCarry.Load(), frames, wireBytes, timestamps, path.ShaperAcceptedDatagrams, path.ShaperEmittedDatagrams)
		want := uint64(fecDataShards + fecParityShards)
		if path.ShaperAcceptedDatagrams != want || path.ShaperEmittedDatagrams != want {
			t.Fatalf("GREEN D108 size-close: shaper accepted/emitted=%d/%d, want all DATA+parity=%d", path.ShaperAcceptedDatagrams, path.ShaperEmittedDatagrams, want)
		}
	})

	t.Run("D108-deadline-parity-charges-at-egress", func(t *testing.T) {
		const (
			partialFrames = 2
			burstFrames   = float64(partialFrames + 1)
			deadline      = 5 * time.Millisecond
		)
		fecCfg := &fec.Config{DataShards: fecDataShards, ParityShards: fecParityShards, Deadline: deadline}
		m, scheduler, clk, recorder := newPacingReproMultipath(t, true, burstFrames, fecCfg)
		if err := m.Send(payloadStream(partialFrames), m.virt); err != nil {
			t.Fatalf("send partial FEC group: %v", err)
		}
		waitForReproFrames(t, recorder, partialFrames+fecParityShards)
		fs := m.fecSend.Load()
		if fs == nil {
			t.Fatal("fixture error: FEC sender was not initialized")
		}
		if fs.parityFrames.Load() != fecParityShards {
			t.Fatalf("fixture error: deadline close wrote %d parity frames, want %d", fs.parityFrames.Load(), fecParityShards)
		}
		_ = scheduler
		frames, wireBytes, timestamps := recorder.snapshot()
		path := m.PeerSnapshots()[0].Paths[0]
		t.Logf("GREEN D108-deadline red_base=040256256470eec5af976d5477c4deb24652d731 fake_time=%s deadline=%s "+
			"data_frames=%d parity_frames=%d parity_bytes=%d parity_carry=%d emitted_frames=%d emitted_wire_bytes=%d "+
			"write_timestamps=%v shaper_accepted=%d shaper_emitted=%d",
			clk.Now().Format(time.RFC3339Nano), deadline, fs.dataFrames.Load(), fs.parityFrames.Load(), fs.parityBytes.Load(),
			m.parityCarry.Load(), frames, wireBytes, timestamps, path.ShaperAcceptedDatagrams, path.ShaperEmittedDatagrams)
		want := uint64(partialFrames + fecParityShards)
		if path.ShaperAcceptedDatagrams != want || path.ShaperEmittedDatagrams != want {
			t.Fatalf("GREEN D108 deadline-close: shaper accepted/emitted=%d/%d, want all DATA+parity=%d", path.ShaperAcceptedDatagrams, path.ShaperEmittedDatagrams, want)
		}
	})
}

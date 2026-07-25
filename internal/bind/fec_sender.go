package bind

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/shaper"
)

const fecDeadlineDispatchGrace = 10 * time.Millisecond

type fecDeadlineTimer interface {
	C() <-chan time.Time
	ResetAt(time.Time)
	Stop()
}

type fecOwnerClock interface {
	Now() time.Time
	NewTimerAt(time.Time) fecDeadlineTimer
}

type systemFECClock struct{}

func (systemFECClock) Now() time.Time { return time.Now() }

func (systemFECClock) NewTimerAt(due time.Time) fecDeadlineTimer {
	delay := time.Until(due)
	if delay < 0 {
		delay = 0
	}
	return &systemFECTimer{timer: time.NewTimer(delay)}
}

type systemFECTimer struct {
	timer *time.Timer
}

func (t *systemFECTimer) C() <-chan time.Time { return t.timer.C }

func (t *systemFECTimer) ResetAt(due time.Time) {
	if !t.timer.Stop() {
		select {
		case <-t.timer.C:
		default:
		}
	}
	delay := time.Until(due)
	if delay < 0 {
		delay = 0
	}
	t.timer.Reset(delay)
}

func (t *systemFECTimer) Stop() {
	if !t.timer.Stop() {
		select {
		case <-t.timer.C:
		default:
		}
	}
}

type fecDeadlineMiss struct {
	Peer      string
	Group     fec.GroupID
	Due       time.Time
	DecidedAt time.Time
	Overshoot time.Duration
}

type fecDeadlineInvalidator func(fecDeadlineMiss)

type fecOwnerAdmission struct {
	group fec.GroupID
	index int
	due   time.Time
	err   error
}

type fecOwnerBatch struct {
	bufs    [][]byte
	classes []shaper.Class
	path    *peerPathState
	remote  netip.AddrPort
	shaped  bool
	done    chan error

	pending   int
	exhausted bool
	completed bool
	err       error
}

type fecStagedData struct {
	class  shaper.Class
	path   *peerPathState
	remote netip.AddrPort
	shaped bool
	group  fec.GroupID
	index  int
	batch  *fecOwnerBatch
}

type fecPreparedWrite struct {
	path   *peerPathState
	remote netip.AddrPort
	shaped bool
	class  shaper.Class
	wire   fecWire
}

type fecOwnerControl struct {
	sample     *fecAdaptiveSample
	readTarget chan int
	done       chan error
}

type fecAdaptiveSample struct {
	now   time.Time
	loss  float64
	count int
}

type fecSendOwner struct {
	m      *Multipath
	peer   *peerState
	fs     *fecSender
	clock  fecOwnerClock
	ctx    context.Context
	cancel context.CancelFunc

	admit   chan *fecOwnerBatch
	space   chan struct{}
	wake    chan struct{}
	control chan fecOwnerControl
	stop    chan struct{}
	done    chan struct{}

	submitMu sync.Mutex
	stopping bool
	stopOnce sync.Once
	stopErr  error
	staged   []fecStagedData

	// Test-only scheduling seams. Tests install them before the first batch.
	afterPublish       func(int, int)
	beforeBatch        func(*fecOwnerBatch)
	beforeCopy         func(*fecOwnerBatch, int)
	afterAdmit         func(fecOwnerAdmission, *fecOwnerBatch, int)
	beforeAdaptiveLock func()
}

func newFECSendOwner(m *Multipath, peer *peerState, fs *fecSender) *fecSendOwner {
	ctx, cancel := context.WithCancel(context.Background())
	o := &fecSendOwner{
		m:       m,
		peer:    peer,
		fs:      fs,
		clock:   m.clock,
		ctx:     ctx,
		cancel:  cancel,
		admit:   make(chan *fecOwnerBatch, 128),
		space:   make(chan struct{}, 1),
		wake:    make(chan struct{}, 1),
		control: make(chan fecOwnerControl, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go o.run()
	return o
}

func (o *fecSendOwner) publish(batch *fecOwnerBatch) error {
	bufferCount := len(batch.bufs)
	completionCapacity := cap(batch.done)
	for {
		// The nonblocking send and stopping transition share submitMu. A full
		// mailbox never holds the mutex: publishers wait on space/stop and retry.
		// Once the send succeeds, only the owner completes batch.done.
		o.submitMu.Lock()
		if o.stopping {
			err := o.terminalError()
			o.submitMu.Unlock()
			return err
		}
		select {
		case o.admit <- batch:
			o.submitMu.Unlock()
			if o.afterPublish != nil {
				o.afterPublish(bufferCount, completionCapacity)
			}
			return nil
		default:
			o.submitMu.Unlock()
		}

		select {
		case <-o.space:
		case <-o.stop:
			return o.terminalError()
		}
	}
}

func (o *fecSendOwner) wait(batch *fecOwnerBatch) error {
	return <-batch.done
}

func (o *fecSendOwner) signalDeadline() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

func (o *fecSendOwner) submitAdaptiveSample(sample fecAdaptiveSample) error {
	done := make(chan error, 1)
	control := fecOwnerControl{sample: &sample, done: done}
	select {
	case o.control <- control:
	case <-o.stop:
		return o.terminalError()
	}
	select {
	case err := <-done:
		return err
	case <-o.stop:
		return o.terminalError()
	}
}

func (o *fecSendOwner) targetParityForTest() (int, error) {
	done := make(chan error, 1)
	target := make(chan int, 1)
	control := fecOwnerControl{readTarget: target, done: done}
	select {
	case o.control <- control:
	case <-o.stop:
		return 0, o.terminalError()
	}
	select {
	case err := <-done:
		return <-target, err
	case <-o.stop:
		return 0, o.terminalError()
	}
}

func (o *fecSendOwner) Stop(err error) {
	if err == nil {
		err = errClosed
	}
	o.submitMu.Lock()
	o.stopOnce.Do(func() {
		o.stopErr = err
		o.stopping = true
		o.cancel()
		close(o.stop)
	})
	o.submitMu.Unlock()
}

func (o *fecSendOwner) Wait() {
	<-o.done
}

func (o *fecSendOwner) terminalError() error {
	if o.stopErr != nil {
		return o.stopErr
	}
	return errClosed
}

func (o *fecSendOwner) run() {
	defer close(o.done)
	var timer fecDeadlineTimer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		if closed, _ := o.closeDueGroup(); closed {
			continue
		}

		var timerC <-chan time.Time
		if due, open := o.fs.enc.NextDeadline(); open {
			if timer == nil {
				timer = o.clock.NewTimerAt(due)
			} else {
				timer.ResetAt(due)
			}
			timerC = timer.C()
		} else if timer != nil {
			timer.Stop()
		}

		select {
		case <-o.stop:
			o.shutdown()
			return
		case <-timerC:
			continue
		case <-o.wake:
			continue
		case control := <-o.control:
			o.handleControl(control)
		case batch := <-o.admit:
			o.signalSpace()
			if o.beforeBatch != nil {
				o.beforeBatch(batch)
			}
			if o.isStopping() {
				o.finishBatch(batch, o.terminalError())
				o.shutdown()
				return
			}
			o.handleBatch(batch)
		}
	}
}

func (o *fecSendOwner) handleControl(control fecOwnerControl) {
	var err error
	if control.sample != nil {
		if o.fs.ctrl == nil {
			err = errors.New("bind: adaptive FEC controller is disabled")
		} else {
			o.applyAdaptiveSample(*control.sample)
		}
	}
	if control.readTarget != nil {
		control.readTarget <- o.fs.enc.TargetParity()
	}
	control.done <- err
}

func (o *fecSendOwner) handleBatch(batch *fecOwnerBatch) {
	for i, inner := range batch.bufs {
		if o.isStopping() {
			o.finishBatch(batch, o.terminalError())
			return
		}
		// A timer can become due while this published batch is executing.
		// Rechecking before every copy gives the expired group priority.
		if _, err := o.closeDueGroup(); err != nil {
			o.finishBatch(batch, err)
			return
		}
		if o.beforeCopy != nil {
			o.beforeCopy(batch, i)
		}
		seq := o.peer.outerSeq.Add(1)
		owned := fecShardPayload(seq, inner)
		ds, decision, err := o.fs.enc.AdmitOwned(owned)
		batch.pending++
		o.staged = append(o.staged, fecStagedData{
			class:  batch.classes[i],
			path:   batch.path,
			remote: batch.remote,
			shaped: batch.shaped,
			group:  ds.Group,
			index:  ds.Index,
			batch:  batch,
		})
		due, open := o.fs.enc.NextDeadline()
		o.publishDeadline(due, open)
		admission := fecOwnerAdmission{group: ds.Group, index: ds.Index, due: due, err: err}
		if o.afterAdmit != nil {
			o.afterAdmit(admission, batch, i)
		}
		if err != nil {
			o.resolveStaged(err)
			o.finishBatch(batch, err)
			return
		}
		switch {
		case !open:
			err = o.decideGroup(decision, time.Time{})
		case !o.clock.Now().Before(due):
			_, err = o.closeDueGroup()
		}
		if err != nil {
			o.finishBatch(batch, err)
			return
		}
	}
	o.finishBatch(batch, nil)
}

func (o *fecSendOwner) closeDueGroup() (bool, error) {
	due, open := o.fs.enc.NextDeadline()
	if !open || o.clock.Now().Before(due) {
		return false, nil
	}
	decision, err := o.fs.enc.TickOwned()
	o.fs.openDeadlineNanos.Store(0)
	if err != nil {
		o.resolveStaged(err)
		return true, err
	}
	err = o.decideGroup(decision, due)
	return true, err
}

func (o *fecSendOwner) publishDeadline(due time.Time, open bool) {
	if !open {
		o.fs.openDeadlineNanos.Store(0)
		return
	}
	o.fs.openDeadlineNanos.Store(due.UnixNano())
}

func (o *fecSendOwner) decideGroup(decision *fec.GroupDecision, due time.Time) error {
	if len(o.staged) == 0 {
		return errors.New("bind: FEC encoder decided an empty group")
	}
	if decision == nil {
		err := errors.New("bind: FEC encoder closed without a group decision")
		o.resolveStaged(err)
		return err
	}
	group := o.staged[0].group
	if decision.Group != group {
		err := fmt.Errorf("bind: FEC group decision %d does not match staged group %d", decision.Group, group)
		o.resolveStaged(err)
		return err
	}
	if !due.IsZero() {
		decidedAt := o.clock.Now()
		overshoot := decidedAt.Sub(due)
		if overshoot < 0 {
			overshoot = 0
		}
		o.fs.recordDeadlineDecision(overshoot)
		if overshoot > fecDeadlineDispatchGrace && o.m.fecDeadlineInvalidator != nil {
			o.m.fecDeadlineInvalidator(fecDeadlineMiss{
				Peer:      o.peer.name,
				Group:     group,
				Due:       due,
				DecidedAt: decidedAt,
				Overshoot: overshoot,
			})
		}
	}

	writes, err := o.m.prepareFECDecision(o.peer, o.fs, o.staged, decision, !due.IsZero())
	if err == nil {
		err = o.emit(group, writes)
	}
	if o.ctx.Err() != nil {
		err = o.terminalError()
	}
	o.resolveStaged(err)
	o.driveAdaptive()
	return err
}

func (o *fecSendOwner) emit(group fec.GroupID, writes []fecPreparedWrite) error {
	if len(writes) > 0 && writes[0].shaped {
		path := writes[0].path
		remote := writes[0].remote
		singlePath := true
		for _, write := range writes[1:] {
			if !write.shaped || write.path != path || write.remote != remote {
				singlePath = false
				break
			}
		}
		if singlePath {
			if shaped, ok := path.shaper.(recoveryPathShaper); ok {
				views := path.views.Load()
				exclusive := views != nil && len(*views) == 1
				contract := shaped.RecoveryContract()
				if exclusive && contract.Enabled {
					datagrams := make([]shaper.Datagram, len(writes))
					wires := make([]fecWire, len(writes))
					for i, write := range writes {
						selectedPath := write.path
						selectedRemote := write.remote
						datagrams[i] = shaper.Datagram{
							Class:   write.class,
							Payload: write.wire.b,
							Write: func(payload []byte) error {
								if _, err := selectedPath.writeToUDPAddrPort(payload, selectedRemote); err != nil {
									selectedPath.socketWriteErrors.Add(1)
									return o.m.accountSendError(selectedPath, err)
								}
								return nil
							},
						}
						wires[i] = write.wire
					}
					control := shaper.RecoveryControl{
						Enabled:         true,
						InstallDeadline: path.installWriteDeadline,
						ClearDeadline: func() error {
							return path.installWriteDeadline(time.Time{})
						},
						Abort: func(err error) {
							path.abortWriteGeneration()
							if o.m.fecDeadlineInvalidator != nil {
								now := o.clock.Now()
								o.m.fecDeadlineInvalidator(fecDeadlineMiss{
									Peer:      o.peer.name,
									Group:     group,
									Due:       now,
									DecidedAt: now,
								})
							}
						},
					}
					result, err := shaped.WriteRecovery(o.ctx, datagrams, control)
					o.m.recordShapedResult(path, o.peer, o.fs, wires, result, err)
					return err
				}
			}
		}
	}
	for i := 0; i < len(writes); {
		write := writes[i]
		if write.shaped {
			end := i + 1
			for end < len(writes) && writes[end].shaped && writes[end].path == write.path {
				end++
			}
			datagrams := make([]shaper.Datagram, end-i)
			wires := make([]fecWire, end-i)
			for j, shapedWrite := range writes[i:end] {
				datagrams[j] = shaper.Datagram{Class: shapedWrite.class, Payload: shapedWrite.wire.b}
				wires[j] = shapedWrite.wire
			}
			result, err := write.path.shaper.WriteDatagrams(o.ctx, datagrams)
			o.m.recordShapedResult(write.path, o.peer, o.fs, wires, result, err)
			if err != nil {
				return err
			}
			i = end
			continue
		}
		if _, err := write.path.writeToUDPAddrPort(write.wire.b, write.remote); err != nil {
			write.path.socketWriteErrors.Add(1)
			return o.m.accountSendError(write.path, err)
		}
		recordWireEmission(write.path, o.peer, o.fs, write.wire)
		i++
	}
	return nil
}

func (o *fecSendOwner) resolveStaged(err error) {
	staged := o.staged
	o.staged = nil
	for _, item := range staged {
		item.batch.pending--
		if err != nil && item.batch.err == nil {
			item.batch.err = err
		}
		o.maybeCompleteBatch(item.batch)
	}
}

func (o *fecSendOwner) rejectQueued(err error) {
	for {
		select {
		case batch := <-o.admit:
			o.signalSpace()
			o.finishBatch(batch, err)
		default:
			return
		}
	}
}

func (o *fecSendOwner) finishBatch(batch *fecOwnerBatch, err error) {
	batch.exhausted = true
	if err != nil && batch.err == nil {
		batch.err = err
	}
	o.maybeCompleteBatch(batch)
}

func (o *fecSendOwner) maybeCompleteBatch(batch *fecOwnerBatch) {
	if batch.completed || !batch.exhausted || batch.pending != 0 {
		return
	}
	batch.completed = true
	batch.bufs = nil
	batch.classes = nil
	batch.done <- batch.err
}

func (o *fecSendOwner) shutdown() {
	err := o.terminalError()
	o.fs.openDeadlineNanos.Store(0)
	o.resolveStaged(err)
	o.rejectQueued(err)
}

func (o *fecSendOwner) signalSpace() {
	select {
	case o.space <- struct{}{}:
	default:
	}
}

func (o *fecSendOwner) isStopping() bool {
	select {
	case <-o.stop:
		return true
	default:
		return false
	}
}

func (o *fecSendOwner) driveAdaptive() {
	if o.fs.ctrl == nil {
		return
	}

	if o.beforeAdaptiveLock != nil {
		o.beforeAdaptiveLock()
	}
	o.m.mu.Lock()
	owner, sample, ok := o.m.adaptiveSampleLocked(o.peer)
	o.m.mu.Unlock()
	if !ok || owner != o {
		return
	}
	o.applyAdaptiveSample(sample)
}

func (o *fecSendOwner) applyAdaptiveSample(sample fecAdaptiveSample) {
	if o.fs.haveControlTick && sample.now.Sub(o.fs.lastControlTick) < adaptiveControlInterval {
		return
	}
	if sample.count == 0 {
		o.fs.publishAdaptiveEligible(sample.loss, sample.count)
		return
	}
	o.fs.lastControlTick = sample.now
	o.fs.haveControlTick = true
	o.fs.ctrl.Observe(sample.loss)
	o.fs.enc.SetParity(o.fs.ctrl.Parity())
	o.fs.publishAdaptiveDrive(o.fs.ctrl.Parity(), o.fs.ctrl.SmoothedLoss(), sample.loss, sample.count)
}

func (fs *fecSender) recordDeadlineDecision(overshoot time.Duration) {
	fs.deadlineDecisions.Add(1)
	if overshoot > fecDeadlineDispatchGrace {
		fs.deadlineMisses.Add(1)
	}
	nanos := overshoot.Nanoseconds()
	for {
		current := fs.deadlineMaxOvershoot.Load()
		if nanos <= current || fs.deadlineMaxOvershoot.CompareAndSwap(current, nanos) {
			return
		}
	}
}

func (m *Multipath) prepareFECDecision(
	peer *peerState,
	fs *fecSender,
	staged []fecStagedData,
	decision *fec.GroupDecision,
	deadline bool,
) ([]fecPreparedWrite, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if peer.fecSend.Load() != fs || peer.sendCodec == nil {
		return nil, errFECPlaneChanged
	}
	if len(decision.Data) != len(staged) {
		return nil, fmt.Errorf("bind: FEC decision data count %d does not match staged count %d", len(decision.Data), len(staged))
	}
	writes := make([]fecPreparedWrite, 0, len(staged)+len(decision.Parity))
	for i, item := range staged {
		if !peerOwnsPath(peer, item.path) {
			return nil, errClosed
		}
		data := decision.Data[i]
		if data.Group != item.group || data.Index != item.index || len(data.Payload) < fecSeqPrefixLen {
			return nil, fmt.Errorf("bind: invalid FEC DATA decision coordinate group=%d index=%d", data.Group, data.Index)
		}
		seq := binary.BigEndian.Uint64(data.Payload[:fecSeqPrefixLen])
		inner := data.Payload[fecSeqPrefixLen:]
		wire, err := peer.sendCodec.Encode(nil, frame.Data{
			OuterSeq: seq,
			PathID:   item.path.id,
			FECGroup: uint32(item.group),
			FECIndex: uint8(item.index),
			Payload:  inner,
		})
		if err != nil {
			return nil, err
		}
		writes = append(writes, fecPreparedWrite{
			path:   item.path,
			remote: item.remote,
			shaped: item.shaped,
			class:  item.class,
			wire:   fecWire{b: wire},
		})
	}

	if len(decision.Parity) == 0 {
		return writes, nil
	}
	parityPath := staged[len(staged)-1].path
	parityRemote := staged[len(staged)-1].remote
	if deadline {
		idx := peer.scheduler.SelectPath()
		if idx < 0 || idx >= len(peer.paths) {
			return writes, nil
		}
		parityPath = peer.paths[idx]
		remote, ok := parityPath.getRemote()
		if !ok {
			return writes, nil
		}
		parityRemote = remote
	}
	if !peerOwnsPath(peer, parityPath) {
		return writes, nil
	}
	for _, shard := range decision.Parity {
		wire, err := m.encodeParityLocked(peer, shard, parityPath.id)
		if err != nil {
			return nil, err
		}
		writes = append(writes, fecPreparedWrite{
			path:   parityPath,
			remote: parityRemote,
			shaped: staged[len(staged)-1].shaped,
			class:  shaper.ClassData,
			wire:   fecWire{b: wire, parity: true},
		})
	}
	return writes, nil
}

func peerOwnsPath(peer *peerState, path *peerPathState) bool {
	for _, candidate := range peer.paths {
		if candidate == path {
			return true
		}
	}
	return false
}

var _ fecOwnerClock = systemFECClock{}

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

type fecOwnerRequest struct {
	seq     uint64
	inner   []byte
	class   shaper.Class
	path    *peerPathState
	remote  netip.AddrPort
	shaped  bool
	admit   chan fecOwnerAdmission
	decided chan error
}

type fecOwnerAdmission struct {
	group fec.GroupID
	index int
	due   time.Time
	err   error
}

type fecStagedData struct {
	class   shaper.Class
	path    *peerPathState
	remote  netip.AddrPort
	shaped  bool
	group   fec.GroupID
	index   int
	decided chan error
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

	admit   chan *fecOwnerRequest
	wake    chan struct{}
	control chan fecOwnerControl
	stop    chan struct{}
	done    chan struct{}

	stopOnce sync.Once
	stopErr  error
	staged   []fecStagedData

	// Test-only scheduling seam. Tests install it before the first request.
	afterAdmit func(fecOwnerAdmission, *fecOwnerRequest)
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
		admit:   make(chan *fecOwnerRequest, 128),
		wake:    make(chan struct{}, 1),
		control: make(chan fecOwnerControl, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go o.run()
	return o
}

func (o *fecSendOwner) submit(req *fecOwnerRequest) (fecOwnerAdmission, error) {
	select {
	case o.admit <- req:
	case <-o.stop:
		return fecOwnerAdmission{}, o.terminalError()
	}
	select {
	case admission := <-req.admit:
		return admission, admission.err
	case <-o.stop:
		return fecOwnerAdmission{}, o.terminalError()
	}
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
	o.stopOnce.Do(func() {
		o.stopErr = err
		o.cancel()
		close(o.stop)
	})
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
			o.fs.openDeadlineNanos.Store(0)
			o.resolveStaged(o.terminalError())
			o.rejectQueued(o.terminalError())
			return
		case <-timerC:
			continue
		case <-o.wake:
			continue
		case control := <-o.control:
			o.handleControl(control)
		case req := <-o.admit:
			// A ready timer and a ready mailbox are selected nondeterministically.
			// Re-checking the encoder's exact due time after dequeue and before Admit
			// gives the deadline priority without dropping the already-dequeued request.
			if _, err := o.closeDueGroup(); err != nil {
				req.admit <- fecOwnerAdmission{err: err}
				req.decided <- err
				continue
			}
			o.handleAdmission(req)
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

func (o *fecSendOwner) handleAdmission(req *fecOwnerRequest) {
	owned := fecShardPayload(req.seq, req.inner)
	ds, decision, err := o.fs.enc.AdmitOwned(owned)

	staged := fecStagedData{
		class:   req.class,
		path:    req.path,
		remote:  req.remote,
		shaped:  req.shaped,
		group:   ds.Group,
		index:   ds.Index,
		decided: req.decided,
	}
	o.staged = append(o.staged, staged)
	due, open := o.fs.enc.NextDeadline()
	o.publishDeadline(due, open)
	admission := fecOwnerAdmission{group: ds.Group, index: ds.Index, due: due}
	if o.afterAdmit != nil {
		o.afterAdmit(admission, req)
	}
	if err != nil {
		o.resolveStaged(err)
		o.rejectQueued(err)
		admission.err = err
		req.admit <- admission
		return
	}

	switch {
	case !open:
		err = o.decideGroup(decision, time.Time{})
	case !o.clock.Now().Before(due):
		_, err = o.closeDueGroup()
	}
	admission.err = err
	req.admit <- admission
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
		o.rejectQueued(err)
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
		return errors.New("bind: FEC encoder closed without a group decision")
	}
	group := o.staged[0].group
	if decision.Group != group {
		return fmt.Errorf("bind: FEC group decision %d does not match staged group %d", decision.Group, group)
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
		err = o.emit(writes)
	}
	if o.ctx.Err() != nil {
		err = o.terminalError()
	}
	o.resolveStaged(err)
	if err != nil {
		o.rejectQueued(err)
	}
	o.driveAdaptive()
	return err
}

func (o *fecSendOwner) emit(writes []fecPreparedWrite) error {
	for _, write := range writes {
		if write.shaped {
			datagrams := []shaper.Datagram{{Class: write.class, Payload: write.wire.b}}
			result, err := write.path.shaper.WriteDatagrams(o.ctx, datagrams)
			o.m.recordShapedResult(write.path, o.peer, o.fs, []fecWire{write.wire}, result, err)
			if err != nil {
				return err
			}
			continue
		}
		if _, err := write.path.writeToUDPAddrPort(write.wire.b, write.remote); err != nil {
			write.path.socketWriteErrors.Add(1)
			return o.m.accountSendError(write.path, err)
		}
		recordWireEmission(write.path, o.peer, o.fs, write.wire)
	}
	return nil
}

func (o *fecSendOwner) resolveStaged(err error) {
	staged := o.staged
	o.staged = nil
	for _, item := range staged {
		item.decided <- err
	}
}

func (o *fecSendOwner) rejectQueued(err error) {
	for {
		select {
		case req := <-o.admit:
			req.admit <- fecOwnerAdmission{err: err}
			req.decided <- err
		default:
			return
		}
	}
}

func (o *fecSendOwner) driveAdaptive() {
	if o.fs.ctrl == nil {
		return
	}
	now := o.clock.Now()

	o.m.mu.Lock()
	if o.peer.fecSend.Load() != o.fs {
		o.m.mu.Unlock()
		return
	}
	o.peer.scheduler.Recompute()
	loss, count := dataPathLossLocked(o.peer)
	o.m.mu.Unlock()
	o.applyAdaptiveSample(fecAdaptiveSample{now: now, loss: loss, count: count})
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

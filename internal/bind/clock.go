package bind

import "time"

// deadlineTimer is a timer armed at an absolute instant of its clock.
type deadlineTimer interface {
	C() <-chan time.Time
	ResetAt(time.Time)
	Stop()
}

// bindClock is the bind's time source: the resequencers' clock and the receive
// drainer's park timer. Tests inject one they advance by hand.
type bindClock interface {
	Now() time.Time
	NewTimerAt(time.Time) deadlineTimer
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) NewTimerAt(due time.Time) deadlineTimer {
	return &systemTimer{timer: time.NewTimer(max(0, time.Until(due)))}
}

type systemTimer struct {
	timer *time.Timer
}

func (t *systemTimer) C() <-chan time.Time { return t.timer.C }

func (t *systemTimer) ResetAt(due time.Time) {
	t.Stop()
	t.timer.Reset(max(0, time.Until(due)))
}

func (t *systemTimer) Stop() {
	if !t.timer.Stop() {
		select {
		case <-t.timer.C:
		default:
		}
	}
}

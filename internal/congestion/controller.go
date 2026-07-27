package congestion

import (
	"errors"
	"math"
	"sync"
	"time"
)

const (
	initialTargetFraction   = 0.85
	initialOverheadRatio    = 1.25
	minimumTargetFraction   = 0.10
	minimumTargetRate       = 128_000.0
	decreaseFactor          = 0.85
	deliveredHeadroom       = 0.95
	increaseFraction        = 0.02
	overheadEWMAWeight      = 0.25
	minimumQueueThreshold   = 10 * time.Millisecond
	lossCongestionThreshold = 0.005
)

type CarrierEpoch struct {
	PathID     uint8
	Generation uint64
}

type ActualState struct {
	At                time.Time
	Epoch             CarrierEpoch
	OuterWireBytes    uint64
	InnerDataBytes    uint64
	RTT               time.Duration
	AuthenticatedLoss float64
	LossFresh         bool
	FeedbackEverSeen  bool
}

type TargetState struct {
	Epoch                     CarrierEpoch
	OuterRateBytesPerSecond   float64
	IngressRateBytesPerSecond float64
}

type Snapshot struct {
	Actual                      ActualState
	Target                      TargetState
	DeliveredRateBytesPerSecond float64
	BaseRTT                     time.Duration
	QueueDelay                  time.Duration
	OverheadRatio               float64
	Held                        bool
}

type Controller struct {
	mu         sync.Mutex
	ceiling    float64
	snapshot   Snapshot
	haveSample bool
}

func New(ceilingBytesPerSecond float64) (*Controller, error) {
	if math.IsNaN(ceilingBytesPerSecond) ||
		math.IsInf(ceilingBytesPerSecond, 0) ||
		ceilingBytesPerSecond <= 0 {
		return nil, errors.New("congestion ceiling must be finite and positive")
	}
	return &Controller{ceiling: ceilingBytesPerSecond}, nil
}

func ConservativeSeed(ceilingBytesPerSecond float64) (TargetState, error) {
	controller, err := New(ceilingBytesPerSecond)
	if err != nil {
		return TargetState{}, err
	}
	snapshot, err := controller.Observe(ActualState{
		At: time.Unix(1, 0),
	})
	if err != nil {
		return TargetState{}, err
	}
	return snapshot.Target, nil
}

func (c *Controller) Observe(actual ActualState) (Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if actual.At.IsZero() {
		return Snapshot{}, errors.New("congestion observation time is required")
	}
	if actual.RTT < 0 {
		return Snapshot{}, errors.New("congestion RTT must be non-negative")
	}
	if math.IsNaN(actual.AuthenticatedLoss) ||
		actual.AuthenticatedLoss < 0 ||
		actual.AuthenticatedLoss > 1 {
		return Snapshot{}, errors.New("authenticated loss must be in [0,1]")
	}
	if !c.haveSample || c.snapshot.Actual.Epoch != actual.Epoch {
		target := c.ceiling * initialTargetFraction
		c.snapshot = Snapshot{
			Actual: actual,
			Target: TargetState{
				Epoch:                     actual.Epoch,
				OuterRateBytesPerSecond:   target,
				IngressRateBytesPerSecond: target / initialOverheadRatio,
			},
			BaseRTT:       actual.RTT,
			OverheadRatio: initialOverheadRatio,
			Held:          true,
		}
		c.haveSample = true
		return c.snapshot, nil
	}

	previous := c.snapshot.Actual
	if !actual.At.After(previous.At) {
		return Snapshot{}, errors.New("congestion observation time must advance")
	}

	c.snapshot.Actual = actual
	c.snapshot.Held = true
	if actual.RTT > 0 && (c.snapshot.BaseRTT == 0 || actual.RTT < c.snapshot.BaseRTT) {
		c.snapshot.BaseRTT = actual.RTT
	}
	if actual.RTT > c.snapshot.BaseRTT {
		c.snapshot.QueueDelay = actual.RTT - c.snapshot.BaseRTT
	} else {
		c.snapshot.QueueDelay = 0
	}

	if actual.OuterWireBytes < previous.OuterWireBytes ||
		actual.InnerDataBytes < previous.InnerDataBytes {
		return c.snapshot, nil
	}
	elapsed := actual.At.Sub(previous.At).Seconds()
	outerRate := float64(actual.OuterWireBytes-previous.OuterWireBytes) / elapsed
	innerRate := float64(actual.InnerDataBytes-previous.InnerDataBytes) / elapsed
	feedbackStale := actual.FeedbackEverSeen && !actual.LossFresh
	if innerRate > 0 && !feedbackStale {
		observedRatio := outerRate / innerRate
		if observedRatio < 1 {
			observedRatio = 1
		}
		if observedRatio > 3 {
			observedRatio = 3
		}
		c.snapshot.OverheadRatio =
			(1-overheadEWMAWeight)*c.snapshot.OverheadRatio +
				overheadEWMAWeight*observedRatio
	}

	deliveredRate := outerRate
	if actual.LossFresh {
		deliveredRate *= 1 - actual.AuthenticatedLoss
	}
	c.snapshot.DeliveredRateBytesPerSecond = deliveredRate

	target := c.snapshot.Target.OuterRateBytesPerSecond
	loaded := outerRate >= target*0.50
	queueThreshold := c.snapshot.BaseRTT / 2
	if queueThreshold < minimumQueueThreshold {
		queueThreshold = minimumQueueThreshold
	}
	congested := loaded &&
		(c.snapshot.QueueDelay >= queueThreshold ||
			(actual.LossFresh && actual.AuthenticatedLoss >= lossCongestionThreshold))
	switch {
	case feedbackStale:
		// Authenticated feedback adoption is sticky. Stale evidence cannot raise
		// or lower a carrier target.
	case congested:
		next := target * decreaseFactor
		if deliveredRate > 0 && deliveredRate*deliveredHeadroom < next {
			next = deliveredRate * deliveredHeadroom
		}
		floor := c.ceiling * minimumTargetFraction
		if floor < minimumTargetRate {
			floor = minimumTargetRate
		}
		if floor > c.ceiling {
			floor = c.ceiling
		}
		if next < floor {
			next = floor
		}
		target = next
		c.snapshot.Held = false
	case loaded && (!actual.FeedbackEverSeen || actual.LossFresh):
		target += c.ceiling * increaseFraction
		if target > c.ceiling {
			target = c.ceiling
		}
		c.snapshot.Held = target == c.snapshot.Target.OuterRateBytesPerSecond
	}
	c.snapshot.Target = TargetState{
		Epoch:                     actual.Epoch,
		OuterRateBytesPerSecond:   target,
		IngressRateBytesPerSecond: target / c.snapshot.OverheadRatio,
	}
	return c.snapshot, nil
}

func (c *Controller) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshot
}

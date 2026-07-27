package congestion

import (
	"errors"
	"math"
	"sync"
	"time"
)

const (
	initialTargetFraction    = 0.85
	initialOverheadRatio     = 1.25
	minimumTargetFraction    = 0.10
	minimumTargetRate        = 128_000.0
	decreaseFactor           = 0.85
	deliveredHeadroom        = 0.95
	increaseFraction         = 0.10
	overheadEWMAWeight       = 0.25
	minimumQueueThreshold    = 10 * time.Millisecond
	lossCongestionThreshold  = 0.005
	minimumRetargetSettle    = time.Second
	installedRateTolerance   = 0.01
	initialIngressHeadroom   = 0.95
	minimumIngressHeadroom   = 0.50
	ingressPressureFactor    = 0.85
	ingressRecoveryStep      = 0.01
	ingressPressureThreshold = 0.50
	ingressRecoveryIntervals = 3
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

type InstalledIngressState struct {
	At                 time.Time
	Epoch              CarrierEpoch
	RateBytesPerSecond float64
	Fresh              bool
}

type IngressPressureState struct {
	At                    time.Time
	Epoch                 CarrierEpoch
	AdmissionWaitDuration time.Duration
	Interval              time.Duration
	RingPending           bool
	Loaded                bool
}

type Snapshot struct {
	Actual                      ActualState
	Target                      TargetState
	InstalledIngress            InstalledIngressState
	DeliveredRateBytesPerSecond float64
	BaseRTT                     time.Duration
	QueueDelay                  time.Duration
	OverheadRatio               float64
	AwaitingInstalled           bool
	TargetChanges               uint64
	IngressServiceHeadroom      float64
	IngressPressureRatio        float64
	IngressPressure             bool
	IngressHeadroomChanges      uint64
	Held                        bool
}

type Controller struct {
	mu                    sync.Mutex
	seed                  float64
	limit                 float64
	snapshot              Snapshot
	haveSample            bool
	lastIngressPressureAt time.Time
	cleanIngressIntervals int
}

func New(seedBytesPerSecond, limitBytesPerSecond float64) (*Controller, error) {
	if _, err := MinimumTargetRate(seedBytesPerSecond); err != nil {
		return nil, err
	}
	if math.IsNaN(limitBytesPerSecond) ||
		math.IsInf(limitBytesPerSecond, 0) ||
		limitBytesPerSecond < 0 {
		return nil, errors.New("congestion limit must be finite and non-negative")
	}
	if limitBytesPerSecond > 0 && limitBytesPerSecond < seedBytesPerSecond {
		return nil, errors.New("congestion limit must be at least the seed")
	}
	return &Controller{seed: seedBytesPerSecond, limit: limitBytesPerSecond}, nil
}

// MinimumTargetRate returns the lowest rate a controller seeded at seed can
// install after congestion. The fixed absolute floor never raises a low seed.
func MinimumTargetRate(seedBytesPerSecond float64) (float64, error) {
	if math.IsNaN(seedBytesPerSecond) ||
		math.IsInf(seedBytesPerSecond, 0) ||
		seedBytesPerSecond <= 0 {
		return 0, errors.New("congestion seed must be finite and positive")
	}
	floor := seedBytesPerSecond * minimumTargetFraction
	if floor < minimumTargetRate {
		floor = minimumTargetRate
	}
	if floor > seedBytesPerSecond {
		floor = seedBytesPerSecond
	}
	return floor, nil
}

func ConservativeSeed(seedBytesPerSecond, limitBytesPerSecond float64) (TargetState, error) {
	controller, err := New(seedBytesPerSecond, limitBytesPerSecond)
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
		target := c.seed * initialTargetFraction
		c.snapshot = Snapshot{
			Actual: actual,
			Target: TargetState{
				Epoch:                   actual.Epoch,
				OuterRateBytesPerSecond: target,
				IngressRateBytesPerSecond: target /
					initialOverheadRatio *
					initialIngressHeadroom,
			},
			BaseRTT:                actual.RTT,
			OverheadRatio:          initialOverheadRatio,
			IngressServiceHeadroom: initialIngressHeadroom,
			Held:                   true,
		}
		c.lastIngressPressureAt = time.Time{}
		c.cleanIngressIntervals = 0
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
	target := c.snapshot.Target.OuterRateBytesPerSecond
	loaded := outerRate >= target*0.50

	deliveredRate := outerRate
	if actual.LossFresh {
		deliveredRate *= 1 - actual.AuthenticatedLoss
	}
	c.snapshot.DeliveredRateBytesPerSecond = deliveredRate

	queueThreshold := c.snapshot.BaseRTT / 2
	if queueThreshold < minimumQueueThreshold {
		queueThreshold = minimumQueueThreshold
	}
	congested := loaded &&
		(c.snapshot.QueueDelay >= queueThreshold ||
			(actual.LossFresh && actual.AuthenticatedLoss >= lossCongestionThreshold))
	if c.snapshot.AwaitingInstalled {
		installed := c.snapshot.InstalledIngress
		rateMatches := installed.Epoch == actual.Epoch &&
			installed.Fresh &&
			ratesWithinTolerance(
				installed.RateBytesPerSecond,
				c.snapshot.Target.IngressRateBytesPerSecond,
			)
		settle := minimumRetargetSettle
		if c.snapshot.BaseRTT > settle {
			settle = c.snapshot.BaseRTT
		}
		if !rateMatches || actual.At.Before(installed.At.Add(settle)) {
			return c.snapshot, nil
		}
		c.snapshot.AwaitingInstalled = false
	}

	if innerRate > 0 && loaded && !feedbackStale {
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

	previousTarget := c.snapshot.Target
	switch {
	case congested:
		next := target * decreaseFactor
		if deliveredRate > 0 && deliveredRate*deliveredHeadroom < next {
			next = deliveredRate * deliveredHeadroom
		}
		floor, _ := MinimumTargetRate(c.seed)
		if next < floor {
			next = floor
		}
		target = next
	case feedbackStale:
		// Stale authenticated evidence cannot raise the target or cause a
		// loss-based decrease. Local queue delay remains current evidence and
		// takes the fail-closed decrease branch above.
	case loaded && (!actual.FeedbackEverSeen || actual.LossFresh):
		target += c.seed * increaseFraction
		if c.limit > 0 && target > c.limit {
			target = c.limit
		}
	}
	c.snapshot.Target = TargetState{
		Epoch:                   actual.Epoch,
		OuterRateBytesPerSecond: target,
		IngressRateBytesPerSecond: target /
			c.snapshot.OverheadRatio *
			c.snapshot.IngressServiceHeadroom,
	}
	c.snapshot.Held = c.snapshot.Target == previousTarget
	c.snapshot.AwaitingInstalled = !c.snapshot.Held
	if !c.snapshot.Held {
		c.snapshot.TargetChanges++
		c.snapshot.InstalledIngress.Fresh = false
	}
	return c.snapshot, nil
}

func (c *Controller) ObserveIngressPressure(
	actual IngressPressureState,
) (Snapshot, error) {
	if actual.At.IsZero() {
		return Snapshot{}, errors.New("congestion ingress-pressure time is required")
	}
	if actual.Interval <= 0 {
		return Snapshot{}, errors.New("congestion ingress-pressure interval must be positive")
	}
	if actual.AdmissionWaitDuration < 0 {
		return Snapshot{}, errors.New("congestion admission-wait duration must be non-negative")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.haveSample || actual.Epoch != c.snapshot.Target.Epoch {
		return Snapshot{}, errors.New("congestion ingress-pressure epoch does not match target")
	}
	if !c.lastIngressPressureAt.IsZero() &&
		!actual.At.After(c.lastIngressPressureAt) {
		return Snapshot{}, errors.New("congestion ingress-pressure time must advance")
	}
	c.lastIngressPressureAt = actual.At
	c.snapshot.IngressPressureRatio =
		float64(actual.AdmissionWaitDuration) / float64(actual.Interval)
	c.snapshot.IngressPressure =
		actual.Loaded &&
			(actual.RingPending ||
				c.snapshot.IngressPressureRatio >= ingressPressureThreshold)
	if c.snapshot.IngressPressure {
		c.cleanIngressIntervals = 0
	} else if !actual.Loaded {
		c.cleanIngressIntervals = 0
	}
	if !c.ingressTargetSettledLocked(actual.At) {
		return c.snapshot, nil
	}

	nextHeadroom := c.snapshot.IngressServiceHeadroom
	if c.snapshot.IngressPressure {
		nextHeadroom *= ingressPressureFactor
		if nextHeadroom < minimumIngressHeadroom {
			nextHeadroom = minimumIngressHeadroom
		}
	} else if actual.Loaded {
		c.cleanIngressIntervals++
		if c.cleanIngressIntervals < ingressRecoveryIntervals {
			return c.snapshot, nil
		}
		c.cleanIngressIntervals = 0
		nextHeadroom += ingressRecoveryStep
		if nextHeadroom > 1 {
			nextHeadroom = 1
		}
	} else {
		return c.snapshot, nil
	}
	if nextHeadroom == c.snapshot.IngressServiceHeadroom {
		return c.snapshot, nil
	}
	c.snapshot.IngressServiceHeadroom = nextHeadroom
	c.snapshot.Target.IngressRateBytesPerSecond =
		c.snapshot.Target.OuterRateBytesPerSecond /
			c.snapshot.OverheadRatio *
			nextHeadroom
	c.snapshot.AwaitingInstalled = true
	c.snapshot.InstalledIngress.Fresh = false
	c.snapshot.TargetChanges++
	c.snapshot.IngressHeadroomChanges++
	c.snapshot.Held = false
	return c.snapshot, nil
}

func (c *Controller) ingressTargetSettledLocked(at time.Time) bool {
	if c.snapshot.AwaitingInstalled ||
		!c.snapshot.InstalledIngress.Fresh ||
		c.snapshot.InstalledIngress.Epoch != c.snapshot.Target.Epoch ||
		!ratesWithinTolerance(
			c.snapshot.InstalledIngress.RateBytesPerSecond,
			c.snapshot.Target.IngressRateBytesPerSecond,
		) {
		return false
	}
	settle := minimumRetargetSettle
	if c.snapshot.BaseRTT > settle {
		settle = c.snapshot.BaseRTT
	}
	return !at.Before(c.snapshot.InstalledIngress.At.Add(settle))
}

func (c *Controller) ObserveInstalledIngress(actual InstalledIngressState) error {
	if actual.At.IsZero() {
		return errors.New("installed ingress observation time is required")
	}
	if math.IsNaN(actual.RateBytesPerSecond) ||
		math.IsInf(actual.RateBytesPerSecond, 0) ||
		actual.RateBytesPerSecond <= 0 {
		return errors.New("installed ingress rate must be finite and positive")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	actual.Fresh = actual.Fresh &&
		actual.Epoch == c.snapshot.Target.Epoch &&
		ratesWithinTolerance(
			actual.RateBytesPerSecond,
			c.snapshot.Target.IngressRateBytesPerSecond,
		)
	if actual.Fresh && c.snapshot.InstalledIngress.Fresh {
		actual.At = c.snapshot.InstalledIngress.At
	}
	c.snapshot.InstalledIngress = actual
	return nil
}

func (c *Controller) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshot
}

func ratesWithinTolerance(actual, target float64) bool {
	return math.Abs(actual-target) <= target*installedRateTolerance
}

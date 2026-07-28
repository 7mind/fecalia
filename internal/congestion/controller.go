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
	increaseFraction         = 0.10
	overheadEWMAWeight       = 0.25
	minimumQueueThreshold    = 10 * time.Millisecond
	rttVariationFactor       = 4
	delayCongestionDwell     = time.Second
	lossCongestionThreshold  = 0.005
	lossRecoveryDwell        = time.Second
	minimumRetargetSettle    = time.Second
	installedRateTolerance   = 0.01
	initialIngressHeadroom   = 0.95
	minimumIngressHeadroom   = 0.50
	ingressPressureFactor    = 0.85
	ingressRecoveryStep      = 0.01
	ingressPressureThreshold = 0.50
	ingressRecoveryIntervals = 3
	loadThreshold            = 0.50
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
	RTTVariation      time.Duration
	AuthenticatedLoss float64
	LossRevision      uint64
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
	mu                            sync.Mutex
	seed                          float64
	limit                         float64
	snapshot                      Snapshot
	haveSample                    bool
	lastIngressPressureAt         time.Time
	cleanIngressIntervals         int
	ingressPressurePending        bool
	ingressPressureAcknowledgedAt time.Time
	delayCongestedSince           time.Time
	lossCleanSince                time.Time
	lossDecreasePending           bool
	lossEpisodeActive             bool
	lossRecoveryBlockedByRetarget bool
	lastLossRevision              uint64
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
	if actual.RTTVariation < 0 {
		return Snapshot{}, errors.New("congestion RTT variation must be non-negative")
	}
	if math.IsNaN(actual.AuthenticatedLoss) ||
		actual.AuthenticatedLoss < 0 ||
		actual.AuthenticatedLoss > 1 {
		return Snapshot{}, errors.New("authenticated loss must be in [0,1]")
	}
	if actual.LossFresh &&
		actual.AuthenticatedLoss > 0 &&
		actual.LossRevision == 0 {
		return Snapshot{}, errors.New("fresh authenticated loss revision is required")
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
		c.ingressPressurePending = false
		c.ingressPressureAcknowledgedAt = time.Time{}
		c.delayCongestedSince = time.Time{}
		c.lossCleanSince = time.Time{}
		c.lossDecreasePending = actual.LossFresh &&
			actual.AuthenticatedLoss >= lossCongestionThreshold
		c.lossEpisodeActive = c.lossDecreasePending
		c.lossRecoveryBlockedByRetarget = false
		c.lastLossRevision = actual.LossRevision
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

	newLossEvidence := actual.LossFresh &&
		actual.LossRevision != 0 &&
		actual.LossRevision != c.lastLossRevision
	if actual.LossFresh && actual.LossRevision != 0 {
		c.lastLossRevision = actual.LossRevision
	}
	lossAboveThreshold := actual.LossFresh &&
		actual.AuthenticatedLoss >= lossCongestionThreshold
	switch {
	case lossAboveThreshold:
		c.lossCleanSince = time.Time{}
		if newLossEvidence && !c.lossEpisodeActive {
			c.lossDecreasePending = true
		}
		c.lossEpisodeActive = true
	case c.lossDecreasePending || !actual.LossFresh:
		c.lossCleanSince = time.Time{}
	}
	if actual.OuterWireBytes < previous.OuterWireBytes ||
		actual.InnerDataBytes < previous.InnerDataBytes {
		c.delayCongestedSince = time.Time{}
		c.lossCleanSince = time.Time{}
		return c.snapshot, nil
	}
	elapsed := actual.At.Sub(previous.At).Seconds()
	outerRate := float64(actual.OuterWireBytes-previous.OuterWireBytes) / elapsed
	innerRate := float64(actual.InnerDataBytes-previous.InnerDataBytes) / elapsed
	feedbackStale := actual.FeedbackEverSeen && !actual.LossFresh
	target := c.snapshot.Target.OuterRateBytesPerSecond
	loaded := outerRate >= target*loadThreshold
	ingressLoaded := innerRate >=
		c.snapshot.Target.IngressRateBytesPerSecond*loadThreshold

	deliveredRate := outerRate
	if actual.LossFresh {
		deliveredRate *= 1 - actual.AuthenticatedLoss
	}
	c.snapshot.DeliveredRateBytesPerSecond = deliveredRate

	queueThreshold := queueDelayThreshold(
		c.snapshot.BaseRTT,
		actual.RTTVariation,
	)
	if c.snapshot.AwaitingInstalled {
		c.delayCongestedSince = time.Time{}
		if c.lossRecoveryBlockedByRetarget {
			c.lossCleanSince = time.Time{}
		}
		installed := c.snapshot.InstalledIngress
		rateMatches := installed.Epoch == actual.Epoch &&
			installed.Fresh &&
			ratesWithinTolerance(
				installed.RateBytesPerSecond,
				c.snapshot.Target.IngressRateBytesPerSecond,
			)
		settle := c.retargetSettleLocked()
		if !rateMatches || actual.At.Before(installed.At.Add(settle)) {
			c.observeLossRecoveryLocked(
				actual.At,
				!c.lossRecoveryBlockedByRetarget &&
					actual.LossFresh &&
					!lossAboveThreshold &&
					!c.lossDecreasePending &&
					c.lossEpisodeActive,
			)
			return c.snapshot, nil
		}
		c.snapshot.AwaitingInstalled = false
		c.lossRecoveryBlockedByRetarget = false
	}
	c.observeLossRecoveryLocked(
		actual.At,
		actual.LossFresh &&
			!lossAboveThreshold &&
			!c.lossDecreasePending &&
			c.lossEpisodeActive,
	)
	lossCongested := c.lossDecreasePending
	delayCandidate := loaded && c.snapshot.QueueDelay >= queueThreshold
	delayCongested := false
	if lossCongested {
		c.delayCongestedSince = time.Time{}
	} else {
		delayCongested = c.observeDelayCongestionLocked(actual.At, delayCandidate)
	}

	if innerRate > 0 && (loaded || ingressLoaded) && !feedbackStale {
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
	case lossCongested || delayCongested:
		if lossCongested {
			c.lossDecreasePending = false
		}
		next := target * decreaseFactor
		floor, _ := MinimumTargetRate(c.seed)
		if next < floor {
			next = floor
		}
		target = next
	case feedbackStale:
		// Stale authenticated evidence cannot raise the target or cause a
		// loss-based decrease. Local queue delay remains current evidence and
		// takes the fail-closed decrease branch after its dwell.
	case c.lossEpisodeActive:
		// Adjacent reports from one sustained loss episode hold the target after
		// its first multiplicative response. Clean evidence must dwell before
		// additive recovery or a later episode can re-arm the response.
	case delayCandidate:
		// A transient queue crossing holds the target while its dwell accrues.
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
		c.lossRecoveryBlockedByRetarget =
			c.snapshot.Target.OuterRateBytesPerSecond !=
				previousTarget.OuterRateBytesPerSecond
		c.delayCongestedSince = time.Time{}
		c.snapshot.TargetChanges++
		c.snapshot.InstalledIngress.Fresh = false
	}
	return c.snapshot, nil
}

func (c *Controller) observeLossRecoveryLocked(at time.Time, eligible bool) {
	if !eligible {
		return
	}
	if c.lossCleanSince.IsZero() {
		c.lossCleanSince = at
	}
	if at.Sub(c.lossCleanSince) >= lossRecoveryDwell {
		c.lossCleanSince = time.Time{}
		c.lossEpisodeActive = false
	}
}

func (c *Controller) observeDelayCongestionLocked(
	at time.Time,
	candidate bool,
) bool {
	if !candidate {
		c.delayCongestedSince = time.Time{}
		return false
	}
	if c.delayCongestedSince.IsZero() {
		c.delayCongestedSince = at
		return false
	}
	return at.Sub(c.delayCongestedSince) >= delayCongestionDwell
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
			c.snapshot.IngressPressureRatio >= ingressPressureThreshold
	if c.snapshot.IngressPressure {
		c.cleanIngressIntervals = 0
	} else if !actual.Loaded {
		c.cleanIngressIntervals = 0
	}
	if c.snapshot.IngressPressure {
		if c.ingressPressurePending ||
			(!c.ingressPressureAcknowledgedAt.IsZero() &&
				actual.At.Before(
					c.ingressPressureAcknowledgedAt.Add(
						c.retargetSettleLocked(),
					),
				)) {
			return c.snapshot, nil
		}
	} else if !c.ingressTargetSettledLocked(actual.At) {
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
	c.ingressPressurePending = c.snapshot.IngressPressure
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
	settle := c.retargetSettleLocked()
	return !at.Before(c.snapshot.InstalledIngress.At.Add(settle))
}

func (c *Controller) retargetSettleLocked() time.Duration {
	if c.snapshot.BaseRTT > minimumRetargetSettle {
		return c.snapshot.BaseRTT
	}
	return minimumRetargetSettle
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
	if actual.Fresh && c.ingressPressurePending {
		c.ingressPressurePending = false
		c.ingressPressureAcknowledgedAt = actual.At
	}
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

func queueDelayThreshold(baseRTT, rttVariation time.Duration) time.Duration {
	threshold := baseRTT / 2
	if threshold < minimumQueueThreshold {
		threshold = minimumQueueThreshold
	}
	const maximumDuration = time.Duration(1<<63 - 1)
	if rttVariation > (maximumDuration-threshold)/rttVariationFactor {
		return maximumDuration
	}
	return threshold + rttVariationFactor*rttVariation
}

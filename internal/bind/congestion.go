package bind

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/7mind/wanbond/internal/congestion"
	"github.com/7mind/wanbond/internal/shaper"
)

type congestionObservation struct {
	controller *congestion.Controller
	actual     congestion.ActualState
	retargeter pathShaperRetargeter
	linkRTT    time.Duration
	lmax       int
}

// driveCongestionControllers samples the same active-backup carrier identity
// that owns authenticated DATA-loss feedback. It runs once per probe cadence;
// controller decisions remain off the DATA send path.
func (m *Multipath) driveCongestionControllers() {
	now := m.clock.Now()
	m.mu.Lock()
	observations := make([]congestionObservation, 0, len(m.peers))
	for _, peer := range m.peers {
		dataPaths := peer.scheduler.DataPaths()
		if len(dataPaths) != 1 {
			peer.congestionHaveCarrier = false
			continue
		}
		index := dataPaths[0].Index
		if index < 0 || index >= len(peer.paths) {
			peer.congestionHaveCarrier = false
			continue
		}
		path := peer.paths[index]
		if path.congestion == nil || path.prober == nil {
			continue
		}
		shaperConfig := m.shaperConfigLocked(path.name)
		retargeter, retargetable := path.shaper.(pathShaperRetargeter)
		if shaperConfig == nil || !retargetable {
			continue
		}
		if !peer.congestionHaveCarrier || peer.congestionCarrierID != path.id {
			peer.congestionHaveCarrier = true
			peer.congestionCarrierID = path.id
			peer.congestionGeneration++
			if peer.congestionGeneration == 0 {
				peer.congestionGeneration++
			}
		}

		var loss float64
		var fresh, ever bool
		if peer.dataLoss != nil {
			identity := localDataLossIdentity{}
			if peer.contracts != nil {
				identity, _ = peer.contracts.localDataLossIdentity()
			}
			loss, fresh, ever = peer.dataLoss.sampleIdentity(path.id, identity, now)
		}
		estimate := path.prober.Estimate()
		observations = append(observations, congestionObservation{
			controller: path.congestion,
			retargeter: retargeter,
			linkRTT:    shaperConfig.LinkRTT,
			lmax:       shaperConfig.MaxEncodedDatagramBytes,
			actual: congestion.ActualState{
				At:                now,
				Epoch:             congestion.CarrierEpoch{PathID: path.id, Generation: peer.congestionGeneration},
				OuterWireBytes:    path.outerWireBytes.Load(),
				InnerDataBytes:    path.innerDataBytes.Load(),
				RTT:               estimate.RTT,
				RTTVariation:      estimate.Jitter,
				AuthenticatedLoss: loss,
				LossFresh:         fresh,
				FeedbackEverSeen:  ever,
			},
		})
	}
	m.mu.Unlock()

	for _, observation := range observations {
		snapshot, err := observation.controller.Observe(observation.actual)
		if err != nil {
			m.log.Warn("bind: congestion observation rejected", "error", err.Error())
			continue
		}
		dataBudgetBytes, err := congestionDataBudget(
			snapshot.Target.OuterRateBytesPerSecond,
			observation.linkRTT,
			observation.lmax,
		)
		if err != nil {
			m.log.Warn("bind: congestion retarget rejected", "error", err.Error())
			continue
		}
		if _, err := observation.retargeter.TryRetarget(
			snapshot.Target.OuterRateBytesPerSecond,
			dataBudgetBytes,
		); err != nil && !errors.Is(err, shaper.ErrClosed) {
			m.log.Warn("bind: path shaper retarget rejected", "error", err.Error())
		}
	}
}

func congestionDataBudget(rateBytesPerSecond float64, rtt time.Duration, lmax int) (int, error) {
	if math.IsNaN(rateBytesPerSecond) ||
		math.IsInf(rateBytesPerSecond, 0) ||
		rateBytesPerSecond <= 0 {
		return 0, errors.New("bind: congestion target rate must be finite and positive")
	}
	if rtt <= 0 {
		return 0, errors.New("bind: congestion link RTT must be positive")
	}
	if lmax <= 0 {
		return 0, errors.New("bind: congestion maximum datagram must be positive")
	}
	budget := math.Ceil(rateBytesPerSecond * rtt.Seconds())
	if budget > float64(math.MaxInt) {
		return 0, fmt.Errorf("bind: congestion DATA budget %g exceeds int", budget)
	}
	if budget < float64(lmax) {
		return lmax, nil
	}
	return int(budget), nil
}

type CongestionPathSnapshot struct {
	Peer     string
	Path     string
	Snapshot congestion.Snapshot
}

// CongestionSnapshots returns one target/actual snapshot per shaped path.
func (m *Multipath) CongestionSnapshots() []CongestionPathSnapshot {
	m.mu.Lock()
	type pathRef struct {
		peer       string
		path       string
		controller *congestion.Controller
	}
	refs := make([]pathRef, 0)
	for _, peer := range m.peers {
		for _, path := range peer.paths {
			if path.congestion != nil {
				refs = append(refs, pathRef{peer: peer.name, path: path.name, controller: path.congestion})
			}
		}
	}
	m.mu.Unlock()

	out := make([]CongestionPathSnapshot, len(refs))
	for index, ref := range refs {
		out[index] = CongestionPathSnapshot{
			Peer: ref.peer, Path: ref.path, Snapshot: ref.controller.Snapshot(),
		}
	}
	return out
}

// TUNIngressTarget sums the current per-peer active-backup ingress targets.
// Weighted striping has no single authenticated carrier record and therefore
// deliberately does not participate in this early-TUN controller.
func (m *Multipath) TUNIngressTarget() (
	rateBytesPerSecond float64,
	epoch uint64,
	dataBudgetBytes int,
	ok bool,
) {
	m.mu.Lock()
	type targetRef struct {
		controller *congestion.Controller
		shaper     pathShaperReporter
		epoch      congestion.CarrierEpoch
	}
	refs := make([]targetRef, 0, len(m.peers))
	for _, peer := range m.peers {
		dataPaths := peer.scheduler.DataPaths()
		if len(dataPaths) != 1 {
			m.mu.Unlock()
			return 0, 0, 0, false
		}
		index := dataPaths[0].Index
		if index < 0 || index >= len(peer.paths) {
			m.mu.Unlock()
			return 0, 0, 0, false
		}
		path := peer.paths[index]
		if path.congestion == nil || !peer.congestionHaveCarrier {
			m.mu.Unlock()
			return 0, 0, 0, false
		}
		reporter, reports := path.shaper.(pathShaperReporter)
		if !reports {
			m.mu.Unlock()
			return 0, 0, 0, false
		}
		refs = append(refs, targetRef{
			controller: path.congestion,
			shaper:     reporter,
			epoch: congestion.CarrierEpoch{
				PathID: path.id, Generation: peer.congestionGeneration,
			},
		})
	}
	m.mu.Unlock()

	for _, ref := range refs {
		snapshot := ref.controller.Snapshot()
		if snapshot.Target.Epoch != ref.epoch ||
			snapshot.Target.IngressRateBytesPerSecond <= 0 {
			return 0, 0, 0, false
		}
		shaperSnapshot := ref.shaper.Snapshot()
		if !ratesWithinOnePercent(
			shaperSnapshot.RateBytesPerSecond,
			snapshot.Target.OuterRateBytesPerSecond,
		) {
			return 0, 0, 0, false
		}
		rateBytesPerSecond += snapshot.Target.IngressRateBytesPerSecond
		if shaperSnapshot.DataBudgetBytes > dataBudgetBytes {
			dataBudgetBytes = shaperSnapshot.DataBudgetBytes
		}
		epoch = epoch*1099511628211 ^ (ref.epoch.Generation<<8 | uint64(ref.epoch.PathID))
	}
	return rateBytesPerSecond, epoch, dataBudgetBytes,
		rateBytesPerSecond > 0 && dataBudgetBytes > 0
}

func ratesWithinOnePercent(actual, target float64) bool {
	return math.Abs(actual-target) <= target*0.01
}

// ObserveTUNIngressActual acknowledges an aggregate rate only when it still
// matches the current active-carrier target. Each contributing controller then
// receives its own exact installed share, so a concurrent retarget cannot
// accidentally release another controller decision.
func (m *Multipath) ObserveTUNIngressActual(
	rateBytesPerSecond float64,
	epoch uint64,
	observedAt time.Time,
	fresh bool,
) error {
	if observedAt.IsZero() {
		return errors.New("bind: TUN ingress readback time is required")
	}
	if math.IsNaN(rateBytesPerSecond) ||
		math.IsInf(rateBytesPerSecond, 0) ||
		rateBytesPerSecond <= 0 {
		return errors.New("bind: TUN ingress readback rate must be finite and positive")
	}

	m.mu.Lock()
	type installedRef struct {
		controller *congestion.Controller
		epoch      congestion.CarrierEpoch
		rate       float64
	}
	refs := make([]installedRef, 0, len(m.peers))
	for _, peer := range m.peers {
		dataPaths := peer.scheduler.DataPaths()
		if len(dataPaths) != 1 {
			m.mu.Unlock()
			return nil
		}
		index := dataPaths[0].Index
		if index < 0 || index >= len(peer.paths) {
			m.mu.Unlock()
			return nil
		}
		path := peer.paths[index]
		if path.congestion == nil || !peer.congestionHaveCarrier {
			m.mu.Unlock()
			return nil
		}
		carrierEpoch := congestion.CarrierEpoch{
			PathID: path.id, Generation: peer.congestionGeneration,
		}
		snapshot := path.congestion.Snapshot()
		if snapshot.Target.Epoch != carrierEpoch ||
			snapshot.Target.IngressRateBytesPerSecond <= 0 {
			m.mu.Unlock()
			return nil
		}
		refs = append(refs, installedRef{
			controller: path.congestion,
			epoch:      carrierEpoch,
			rate:       snapshot.Target.IngressRateBytesPerSecond,
		})
	}
	m.mu.Unlock()

	var targetRate float64
	var targetEpoch uint64
	for _, ref := range refs {
		targetRate += ref.rate
		targetEpoch =
			targetEpoch*1099511628211 ^
				(ref.epoch.Generation<<8 | uint64(ref.epoch.PathID))
	}
	if targetRate <= 0 ||
		epoch != targetEpoch ||
		math.Abs(rateBytesPerSecond-targetRate) > targetRate*0.01 {
		return nil
	}

	for _, ref := range refs {
		if err := ref.controller.ObserveInstalledIngress(congestion.InstalledIngressState{
			At:                 observedAt,
			Epoch:              ref.epoch,
			RateBytesPerSecond: ref.rate,
			Fresh:              fresh,
		}); err != nil {
			return err
		}
	}
	return nil
}

// ObserveTUNIngressPressure routes one device-wide local-backpressure interval
// to the active carrier controllers without changing their outer-rate targets.
func (m *Multipath) ObserveTUNIngressPressure(
	admissionWaitDuration time.Duration,
	tunBytes uint64,
	interval time.Duration,
	ringPending bool,
	epoch uint64,
	observedAt time.Time,
) error {
	if observedAt.IsZero() {
		return errors.New("bind: TUN ingress pressure time is required")
	}
	if admissionWaitDuration < 0 || interval <= 0 {
		return errors.New(
			"bind: TUN ingress admission wait must be non-negative and interval positive",
		)
	}

	m.mu.Lock()
	type pressureRef struct {
		controller *congestion.Controller
		epoch      congestion.CarrierEpoch
		rate       float64
	}
	refs := make([]pressureRef, 0, len(m.peers))
	for _, peer := range m.peers {
		dataPaths := peer.scheduler.DataPaths()
		if len(dataPaths) != 1 {
			m.mu.Unlock()
			return nil
		}
		index := dataPaths[0].Index
		if index < 0 || index >= len(peer.paths) {
			m.mu.Unlock()
			return nil
		}
		path := peer.paths[index]
		if path.congestion == nil || !peer.congestionHaveCarrier {
			m.mu.Unlock()
			return nil
		}
		carrierEpoch := congestion.CarrierEpoch{
			PathID: path.id, Generation: peer.congestionGeneration,
		}
		snapshot := path.congestion.Snapshot()
		if snapshot.Target.Epoch != carrierEpoch ||
			snapshot.Target.IngressRateBytesPerSecond <= 0 {
			m.mu.Unlock()
			return nil
		}
		refs = append(refs, pressureRef{
			controller: path.congestion,
			epoch:      carrierEpoch,
			rate:       snapshot.Target.IngressRateBytesPerSecond,
		})
	}
	m.mu.Unlock()

	var targetRate float64
	var targetEpoch uint64
	for _, ref := range refs {
		targetRate += ref.rate
		targetEpoch =
			targetEpoch*1099511628211 ^
				(ref.epoch.Generation<<8 | uint64(ref.epoch.PathID))
	}
	if targetRate <= 0 || targetEpoch != epoch || len(refs) == 0 {
		return nil
	}
	loaded := float64(tunBytes) >=
		targetRate*interval.Seconds()*0.50
	perControllerWait := admissionWaitDuration / time.Duration(len(refs))
	for _, ref := range refs {
		if _, err := ref.controller.ObserveIngressPressure(
			congestion.IngressPressureState{
				At:                    observedAt,
				Epoch:                 ref.epoch,
				AdmissionWaitDuration: perControllerWait,
				Interval:              interval,
				RingPending:           ringPending,
				Loaded:                loaded,
			},
		); err != nil {
			return err
		}
	}
	return nil
}

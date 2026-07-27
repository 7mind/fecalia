package bind

import "github.com/7mind/wanbond/internal/congestion"

type congestionObservation struct {
	controller *congestion.Controller
	actual     congestion.ActualState
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
		observations = append(observations, congestionObservation{
			controller: path.congestion,
			actual: congestion.ActualState{
				At:                now,
				Epoch:             congestion.CarrierEpoch{PathID: path.id, Generation: peer.congestionGeneration},
				OuterWireBytes:    path.outerWireBytes.Load(),
				InnerDataBytes:    path.innerDataBytes.Load(),
				RTT:               path.prober.Estimate().RTT,
				AuthenticatedLoss: loss,
				LossFresh:         fresh,
				FeedbackEverSeen:  ever,
			},
		})
	}
	m.mu.Unlock()

	for _, observation := range observations {
		if _, err := observation.controller.Observe(observation.actual); err != nil {
			m.log.Warn("bind: congestion observation rejected", "error", err.Error())
		}
	}
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
func (m *Multipath) TUNIngressTarget() (rateBytesPerSecond float64, epoch uint64, ok bool) {
	m.mu.Lock()
	type targetRef struct {
		controller *congestion.Controller
		epoch      congestion.CarrierEpoch
	}
	refs := make([]targetRef, 0, len(m.peers))
	for _, peer := range m.peers {
		dataPaths := peer.scheduler.DataPaths()
		if len(dataPaths) != 1 {
			m.mu.Unlock()
			return 0, 0, false
		}
		index := dataPaths[0].Index
		if index < 0 || index >= len(peer.paths) {
			m.mu.Unlock()
			return 0, 0, false
		}
		path := peer.paths[index]
		if path.congestion == nil || !peer.congestionHaveCarrier {
			m.mu.Unlock()
			return 0, 0, false
		}
		refs = append(refs, targetRef{
			controller: path.congestion,
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
			return 0, 0, false
		}
		rateBytesPerSecond += snapshot.Target.IngressRateBytesPerSecond
		epoch = epoch*1099511628211 ^ (ref.epoch.Generation<<8 | uint64(ref.epoch.PathID))
	}
	return rateBytesPerSecond, epoch, rateBytesPerSecond > 0
}

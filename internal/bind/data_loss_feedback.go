package bind

import (
	"net/netip"
	"sync"
	"time"

	"github.com/7mind/wanbond/internal/telemetry"
)

const dataLossFeedbackFreshness = 2 * telemetry.DefaultProbeInterval

type dataLossCarrier struct {
	pathID             uint8
	pathKey            uint32
	source             netip.AddrPort
	topologyGeneration uint64
}

type acceptedDataLossFeedback struct {
	report          telemetry.DataLossFeedback
	reporterSession uint64
	acceptedAt      time.Time
}

// dataLossFeedbackCoordinator owns the receive-side interval accumulator and
// the sender-side accepted report for exactly one peer.
type dataLossFeedbackCoordinator struct {
	mu sync.Mutex

	haveCarrier       bool
	carrier           dataLossCarrier
	carrierGeneration uint64
	nextReportID      uint64
	received          uint64
	lost              uint64
	haveFinalized     bool
	finalizedThrough  uint64

	haveAccepted    bool
	everAccepted    bool
	accepted        acceptedDataLossFeedback
	haveReporter    bool
	reporterSession uint64
	lastReportID    uint64
	lastCarrierGen  uint64
	haveSamplePath  bool
	samplePathID    uint8
}

func (c *dataLossFeedbackCoordinator) observeData(
	pathID uint8,
	pathKey uint32,
	source netip.AddrPort,
	topologyGeneration uint64,
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.selectCarrierLocked(dataLossCarrier{
		pathID:             pathID,
		pathKey:            pathKey,
		source:             source,
		topologyGeneration: topologyGeneration,
	})
	c.received++
}

func (c *dataLossFeedbackCoordinator) selectCarrierLocked(next dataLossCarrier) {
	if !c.haveCarrier || c.carrier != next {
		c.haveCarrier = true
		c.carrier = next
		c.carrierGeneration++
		if c.carrierGeneration == 0 {
			c.carrierGeneration++
		}
		c.received = 0
		c.lost = 0
	}
}

func (c *dataLossFeedbackCoordinator) recordLost(first, count uint64) {
	c.mu.Lock()
	if count == 0 {
		c.mu.Unlock()
		return
	}
	last := first + count - 1
	if last < first {
		last = ^uint64(0)
	}
	if !c.haveFinalized || last > c.finalizedThrough {
		c.haveFinalized = true
		c.finalizedThrough = last
	}
	if c.haveCarrier {
		c.lost += count
	}
	c.mu.Unlock()
}

func (c *dataLossFeedbackCoordinator) recordRecovered(seq uint64, carrier dataLossCarrier) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.haveFinalized && seq <= c.finalizedThrough {
		return
	}
	c.selectCarrierLocked(carrier)
	c.lost++
}

func (c *dataLossFeedbackCoordinator) buildReport(
	contract receivedRecoverySnapshot,
	now time.Time,
) *telemetry.DataLossFeedback {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.haveCarrier || c.carrier.topologyGeneration != contract.generation {
		c.received = 0
		c.lost = 0
		return nil
	}
	if !contract.present || contract.invalid || contract.session == 0 ||
		contract.message.ContractID == 0 || !now.Before(contract.validUntil) ||
		(c.received == 0 && c.lost == 0) {
		return nil
	}
	c.nextReportID++
	if c.nextReportID == 0 {
		c.nextReportID++
	}
	report := &telemetry.DataLossFeedback{
		ObservedSessionID: contract.session,
		ContractID:        contract.message.ContractID,
		CarrierPathID:     c.carrier.pathID,
		CarrierGeneration: c.carrierGeneration,
		ReportID:          c.nextReportID,
		Received:          c.received,
		Lost:              c.lost,
	}
	c.received = 0
	c.lost = 0
	return report
}

func (c *dataLossFeedbackCoordinator) accept(
	report telemetry.DataLossFeedback,
	reporterSession uint64,
	adopted bool,
	now time.Time,
) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.haveReporter || c.reporterSession != reporterSession {
		if c.haveReporter && !adopted {
			return false
		}
		c.haveReporter = true
		c.reporterSession = reporterSession
		c.lastReportID = 0
		c.lastCarrierGen = 0
	}
	if report.ReportID <= c.lastReportID ||
		report.CarrierGeneration < c.lastCarrierGen {
		return false
	}
	c.lastReportID = report.ReportID
	c.lastCarrierGen = report.CarrierGeneration
	c.accepted = acceptedDataLossFeedback{
		report:          report,
		reporterSession: reporterSession,
		acceptedAt:      now,
	}
	c.haveAccepted = true
	c.everAccepted = true
	c.haveSamplePath = true
	c.samplePathID = report.CarrierPathID
	return true
}

func (c *dataLossFeedbackCoordinator) sample(
	pathID uint8,
	observedSessionID uint64,
	contractID uint64,
	now time.Time,
) (loss float64, fresh bool, ever bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ever = c.everAccepted
	if c.haveSamplePath && c.samplePathID != pathID {
		c.haveAccepted = false
		c.samplePathID = pathID
	} else if !c.haveSamplePath {
		c.haveSamplePath = true
		c.samplePathID = pathID
	}
	if !c.haveAccepted ||
		c.accepted.report.CarrierPathID != pathID ||
		c.accepted.report.ObservedSessionID != observedSessionID ||
		c.accepted.report.ContractID != contractID ||
		now.Sub(c.accepted.acceptedAt) > dataLossFeedbackFreshness {
		return 0, false, ever
	}
	return c.accepted.report.Loss(), true, ever
}

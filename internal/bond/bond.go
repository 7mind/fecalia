// Package bond implements a bounded, delivery-clocked multipath datagram transport.
// The owner supplies authenticated frames, monotonic time, and validated path changes.
package bond

import (
	"encoding/binary"
	"errors"
	"math"
	"math/bits"
	"time"

	"github.com/7mind/wanbond/internal/frame"
)

const (
	initialWindowBytes    = 3000
	initialRate           = 125000.0
	minimumRate           = 16000.0
	maximumRate           = 1250000000.0
	targetQueue           = 10 * time.Millisecond
	jitterAllowanceFactor = 2
	jitterSpreadFactor    = 3                // a uniform spread is three times the mean difference of consecutive samples
	reorderHeadroom       = 1.25             // allowance above the reordering observed
	reorderMemory         = 10 * time.Second // per bucket; observed reordering is remembered for five
	lossPacingHeadroom    = 1.05
	lossRateReduction     = 0.9
	queueSendExcessRatio  = 1.2
	startupDeliveryGain   = 2
	startupPlateauGrowth  = 1.25
	startupPlateauRounds  = 3
	startupPlateauSending = 0.75
	startupLossEvents     = 3
	startupLossRatio      = 0.02
	maxDelaySamples       = 16
	jitterWarmup          = 8
	maxQueueAge           = 100 * time.Millisecond
	realtimeQueueTarget   = 20 * time.Millisecond
	realtimeQueueInterval = 100 * time.Millisecond
	maxBulkQueueAge       = 250 * time.Millisecond
	discoveryQueueAge     = time.Second
	maxPacketAge          = 250 * time.Millisecond
	ackInterval           = 25 * time.Millisecond
	maxACKInterval        = 50 * time.Millisecond
	ackShare              = 0.08
	ackStreak             = 3
	ackWireBytes          = headerBytes + ackBytes + 78
	ackGapHorizon         = 4 * maxACKInterval
	ackBatchPackets       = 64
	priorityLead          = 5 * time.Millisecond
	minimumClassShare     = 0.05
	smallClassShare       = 0.5
	reservationHeadroom   = 1.1
	ipProtocolTCP         = 6
	pathLease             = time.Second
	minimumRTO            = 60 * time.Millisecond
	// maxWander bounds what passes for a path's own latency wander; a larger
	// delay on a lightly loaded lane is somebody's queue.
	maxWander           = 3 * targetQueue
	maxThreshold        = 10 * targetQueue // above this, delay is a queue whatever the idle lane saw
	repairTail          = 3                // datagrams after one on its lane, fewer than which cannot show it missing
	feedbackHorizon     = 2 * time.Second
	deliveryInterval    = 50 * time.Millisecond
	baselineInterval    = 10 * time.Second
	baselineDrain       = 200 * time.Millisecond
	baselineStagger     = 500 * time.Millisecond
	maxPackets          = 8192
	smallPacketHeadroom = 1024 // of maxPackets, kept from bulk for the small classes
	smallPacket         = 384
	redundancyRate      = 64000.0
	redundancyShare     = 0.2
	maxDatagram         = 9000
	// fullDatagramWireBytes is an ordinary full-size datagram on the wire.
	fullDatagramWireBytes = 1500
	wireOverhead          = Overhead + 28 // data frame, IPv4 and UDP headers
	transitSizeBucket     = 128
	// stallSilence is how long the peer reports nothing new on a lane that
	// holds datagrams before the lane counts as having stalled: twice the
	// longest interval between acknowledgements.
	stallSilence = 2 * maxACKInterval
)

type Transmission struct {
	Path  PathID
	Frame frame.Control
}

type Delivery struct {
	Sequence    uint64
	Interactive bool
	Payload     []byte
}

type FlowID [40]byte

type TCPACK struct {
	Eligible                  bool
	Sequence, Acknowledgement uint32
	Window                    uint16
	TrafficClass              byte
	Timestamp                 bool
	TSVal, TSEcr              uint32
}

type PacketMetadata struct {
	Flow FlowID
	ACK  TCPACK
}

type PathStats struct {
	Path                 PathID
	TransitFloor         time.Duration
	TransitFloorKnown    bool
	TransitFloorAge      time.Duration
	PathDelay            time.Duration
	Rank                 time.Duration
	Liveness             LaneLiveness
	LivenessAge          time.Duration
	ACKProgressKnown     bool
	Capacity             float64 // the demonstrated capacity the target holds below; 0 while discovering
	Rate                 float64
	SendRate             float64
	DeliveryRate         float64
	RTT                  time.Duration
	RTTVariation         time.Duration
	IdleRTTVariation     time.Duration
	IdleForwardVariation time.Duration
	FeedbackRTT          time.Duration
	FeedbackRTTVariation time.Duration
	BaseRTT              time.Duration
	QueueDelay           time.Duration
	InFlight             int
	Window               int
	Sent                 uint64
	ACKed                uint64
	Retransmits          uint64
	InteractiveSent      uint64
	RealtimeOriginals    uint64
	BulkOriginals        uint64
	BulkReceived         uint64
	Up                   bool
	Discovering          bool
	// Threshold is the queue delay above which the lane is taken to queue.
	Threshold time.Duration
	Decisions Decisions
}

type LaneLiveness string

const (
	LaneLive    LaneLiveness = "live"
	LaneSuspect LaneLiveness = "suspect"
	LaneDead    LaneLiveness = "dead"
)

// Decisions counts what the lane's control concluded, so that a rate held low
// can be traced to its cause from outside.
type Decisions struct {
	// DelaySignals and LossSignals count control intervals judged congested
	// by queue delay and by material loss; an interval may be both.
	DelaySignals, LossSignals uint64
	// DiscoveryCongested and DiscoveryPlateau count discoveries ended by a
	// congestion signal and by delivery that stopped growing.
	DiscoveryCongested, DiscoveryPlateau uint64
	// CapacityRemeasured counts estimates replaced by measured delivery after
	// repeated cuts; CapacityDecays the 3% reductions of a held estimate.
	CapacityRemeasured, CapacityDecays uint64
	// Pulses counts probes above the estimate, PulseWins those that drew no
	// congestion signal, PulseLosses those that found the limit, and
	// Rediscoveries the returns to discovery after consecutive wins.
	Pulses, PulseWins, PulseLosses, Rediscoveries uint64
	// StallSignals counts delay signals put down to a stall of the path: the
	// lane delivered nothing for a while and then what it held.
	StallSignals uint64
}

type SmallQueueDropStats struct {
	Admission uint64
	Deadline  uint64
	Stale     uint64
}

type Snapshot struct {
	Paths                 []PathStats
	QueueDrops            uint64
	AdmissionDrops        uint64
	AQMDrops              uint64
	InteractiveQueueDrops uint64
	RealtimeQueueDrops    SmallQueueDropStats
	SmallTCPQueueDrops    SmallQueueDropStats
	InteractiveQueued     int
	CoalescedACKs         uint64
	Expired               uint64
	Duplicates            uint64
	RealtimeMoves         uint64
	Rejected              [RejectionCauses]uint64
}

type packet struct {
	flow           FlowID
	ack            TCPACK
	seq            uint64
	order          uint64
	class          class
	interactive    bool
	payload        []byte
	created        time.Time
	queueDeadline  time.Time
	repairDeadline time.Time
	lastSent       time.Time
	lastPath       PathID
	lastLaneSeq    uint64
	attempts       int
	acked          bool
}

type attempt struct {
	packet    *packet
	sent      time.Time
	bytes     int
	released  bool
	confirmed bool
	// through is the lane's cumulative wire bytes sent up to and including
	// this attempt; against the receiver's cumulative count it measures loss.
	through uint64
}

type transitBaseline struct {
	delay    time.Duration
	known    bool
	observed time.Time
}

type lane struct {
	model                linkModel
	feedbackSent         rateMeter
	id                   PathID
	remoteID             PathID
	lease                time.Time
	rate                 float64
	deliveryRate         float64
	deliverySample       float64
	sendRate             float64
	startup              bool
	discoveryGain        float64
	startupBest          float64
	startupFlatRounds    int
	roundEnd             uint64
	roundDone            bool
	losses               lossLedger
	roundLossy           bool
	previousDelivery     float64
	control              control
	peakDelivery         float64
	recentDelivery       peak
	sustained            sustainedDelivery
	reordering           peak
	newestConfirmed      time.Time
	wander               time.Duration
	wanderVariation      time.Duration
	wanderKnown          bool
	rtt                  time.Duration
	rttVariation         time.Duration
	idleRTTVariation     time.Duration
	idleRTT              time.Duration
	idleForwardMean      time.Duration
	idleForwardVariation time.Duration
	idleForwardKnown     bool
	feedbackRTT          time.Duration
	feedbackRTTVariation time.Duration
	baseRTT              time.Duration
	baseAt               time.Time
	nextSend             time.Time
	classNext            [classes]time.Time
	reserved             [classBulk]float64
	copies               float64
	guaranteed           [classes]bool
	shared               bool
	decisions            Decisions
	// bulkElsewhere: another usable lane carries no real-time originals.
	bulkElsewhere      bool
	lastACK            time.Time
	ackGap             time.Duration
	lastAdjust         time.Time
	lossMark           float64
	lossMarked         bool
	inflight           int
	classInflight      [classes]int
	confirmedWireBytes int
	seq                uint64
	ackRevision        uint64
	receivedHigh       uint64 // the highest lane sequence the peer reported
	arrived            arrived
	stall              stall
	ackReceipts        receiptWindow
	ackedBytes         uint64
	ackedElapsed       uint64
	feedbackAt         time.Time
	rateBytes          uint64
	rateSentBytes      uint64
	rateElapsed        uint64
	firstSent          time.Time
	transitBucket      int
	transitBases       [(maxDatagram+wireOverhead)/transitSizeBucket + 1]transitBaseline
	intervalQueueDelay time.Duration
	signalDelay        time.Duration // the queue delay the last control interval judged
	delayJitter        time.Duration
	intervalJitter     time.Duration
	delayDifferences   int
	intervalSamples    int
	haveInterval       bool
	queueDelay         time.Duration
	nextBaseline       time.Time
	drainUntil         time.Time
	baselinePending    bool
	floorTesting       bool
	floorSampling      bool
	floorTested        time.Time
	floorTestEvery     time.Duration
	droppedAt          time.Time // when the path last showed material loss
	lastTransmit       time.Time
	lastPayload        time.Time
	attempts           map[uint64]attempt
	sent               uint64
	interactiveSent    uint64
	realtimeOriginals  uint64
	bulkOriginals      uint64
	bulkReceived       uint64
	acked              uint64
	retries            uint64
}

type receiver struct {
	path       PathID
	remoteLane PathID
	receipts   receiptWindow
	bytes      uint64
	start      time.Time
	last       time.Time
	highAt     time.Time
	pending    int
	ackAt      time.Time
	streak     int
	received   rateMeter
	revision   uint64
}

// Transport has no goroutines or I/O. Its owner serializes calls and gives
// each a time no earlier than the one before.
type Transport struct {
	epoch     Epoch
	remote    Epoch
	paths     []*lane
	receivers map[PathID]*receiver
	// receiverOrder holds the receivers in order of creation: acknowledgements
	// leave in that order, so a run does not depend on map iteration.
	receiverOrder     []*receiver
	queue             packetFIFO
	bulkAQM           codel
	small             [classBulk]fairPacketQueue
	demand            [classBulk]rateMeter
	pending           map[uint64]*packet
	pendingOrder      []*packet
	seq               uint64
	bulkSeq           uint64
	interactiveSeq    uint64
	redundancyTokens  float64
	realtimeLate      time.Time
	realtimeSkipping  bool
	realtimePath      PathID
	realtimePathKnown bool
	realtimeMoves     uint64
	lastPoll          time.Time
	lastTime          time.Time
	rejected          [RejectionCauses]uint64
	drops             uint64
	admissionDrops    uint64
	aqmDrops          uint64
	interactiveDrops  uint64
	smallQueueDrops   [classBulk]SmallQueueDropStats
	coalescedACKs     uint64
	expired           uint64
	duplicates        uint64
	received          receiptWindow
	unreported        receiptWindow
}

type receiptWindow struct {
	high uint64
	mask [maxPackets / 64]uint64
}

func New(epoch Epoch) *Transport {
	if epoch.Boot == 0 || epoch.Generation == 0 {
		panic("bond: zero local epoch")
	}
	return &Transport{epoch: epoch, receivers: make(map[PathID]*receiver), pending: make(map[uint64]*packet)}
}

func (t *Transport) Epoch() Epoch { return t.epoch }

// SetRemote accepts only a fresh probe's epoch. A boot change requires the
// reflector's challenge proof; generations within that boot never go backwards.
func (t *Transport) SetRemote(epoch Epoch, adopted bool) bool {
	if epoch == t.remote {
		return false
	}
	if epoch.Boot == 0 || epoch.Generation == 0 {
		return false
	}
	if t.remote.Boot != 0 {
		if epoch.Boot == t.remote.Boot && epoch.Generation <= t.remote.Generation {
			return false
		}
		if epoch.Boot != t.remote.Boot && !adopted {
			return false
		}
	}
	t.remote = epoch
	// Sequence numbers belong to the pair of authenticated endpoint generations.
	t.expired += uint64(len(t.pending))
	t.pending = make(map[uint64]*packet)
	t.pendingOrder = nil
	t.seq, t.bulkSeq, t.interactiveSeq = 0, 0, 0
	t.receivers = make(map[PathID]*receiver)
	t.receiverOrder = nil
	t.received = receiptWindow{}
	t.unreported = receiptWindow{}
	for _, p := range t.paths {
		p.lease = time.Time{}
		p.attempts = make(map[uint64]attempt)
		p.inflight, p.classInflight = 0, [classes]int{}
		p.seq = 0
		// A delivery round ends at a lane sequence, and the sequences begin
		// again: left at the old one, no round ended for as long as the lane
		// took to send that many datagrams again, and loss was not judged.
		p.roundEnd, p.roundDone, p.roundLossy = 0, false, false
		p.lastACK, p.ackGap = time.Time{}, 0
		p.nextSend = time.Time{}
		p.feedbackSent = rateMeter{}
		p.losses, p.lossMark, p.lossMarked = lossLedger{}, 0, false
		p.reordering, p.newestConfirmed = peak{bucket: reorderMemory}, time.Time{}
		p.ackRevision, p.receivedHigh, p.arrived = 0, 0, arrived{}
		p.model, p.stall = linkModel{}, stall{}
		p.ackReceipts = receiptWindow{}
		p.ackedBytes, p.ackedElapsed = 0, 0
		clear(p.transitBases[:])
		p.haveInterval, p.intervalSamples, p.delayJitter, p.delayDifferences, p.intervalJitter = false, 0, 0, 0, 0
		p.floorTesting, p.floorSampling, p.floorTestEvery = false, false, 0
		p.firstSent = time.Time{}
		p.idleForwardMean, p.idleForwardVariation = 0, 0
		p.idleForwardKnown = false
		p.feedbackAt = time.Time{}
		p.sustained.marks = nil
		// The peer restarted, not the path: the capacity estimate stands, and
		// the probing state starts over. With the estimate gone and the lane
		// out of discovery, the target grew unbounded until a signal stopped
		// it, and a path that drops rather than queues gave none
		// (TestPeerRestartKeepsTheCapacityEstimate). A lane that never found its
		// capacity discovers it.
		p.control = control{capacity: p.control.capacity}
		if p.control.capacity == 0 && !p.startup {
			p.startup, p.startupBest, p.startupFlatRounds, p.discoveryGain = true, 0, 0, rediscoveryGain
		}
	}
	return true
}

func (t *Transport) Remote() Epoch { return t.remote }

func (t *Transport) Path(id, remoteID PathID, rtt time.Duration, now time.Time) error {
	if err := t.checkTime(now); err != nil {
		return err
	}
	p := t.find(id)
	if p == nil {
		if rtt <= 0 {
			rtt = 50 * time.Millisecond
		}
		p = &lane{id: id, remoteID: remoteID, rate: initialRate, startup: true, discoveryGain: startupDeliveryGain, rtt: rtt, idleRTT: rtt, baseRTT: rtt, baseAt: now, attempts: make(map[uint64]attempt), reordering: peak{bucket: reorderMemory}, sustained: sustainedDelivery{best: peak{bucket: sustainedMemory}}}
		p.lastTransmit = now
		p.nextBaseline = now.Add(baselineInterval + time.Duration((uint16(id)^uint16(id)>>8)&255)*baselineStagger)
		t.paths = append(t.paths, p)
	}
	p.lease = now
	return nil
}

func (t *Transport) Disable(id PathID) {
	if p := t.find(id); p != nil {
		p.lease = time.Time{}
	}
}

func (t *Transport) find(id PathID) *lane {
	for _, p := range t.paths {
		if p.id == id {
			return p
		}
	}
	return nil
}

func (t *Transport) Enqueue(payload []byte, metadata PacketMetadata, now time.Time) error {
	if err := t.checkTime(now); err != nil {
		return err
	}
	if len(payload) == 0 || len(payload) > maxDatagram {
		return errors.New("bond: invalid datagram length")
	}
	class := classify(len(payload), metadata)
	// Bulk stops short of the bound: a flood that holds the count there must
	// not have every small datagram refused behind it
	// (`TestBulkFloodDoesNotRefuseVoice`).
	limit := maxPackets
	if class == classBulk {
		limit -= smallPacketHeadroom
	}
	if t.queued()+len(t.pending) >= limit {
		t.drops++
		t.admissionDrops++
		if class != classBulk {
			t.interactiveDrops++
			t.smallQueueDrops[class].Admission++
		}
		return nil
	}
	p := &packet{flow: metadata.Flow, ack: metadata.ACK, class: class, payload: append([]byte(nil), payload...), created: now}
	// Each bulk datagram keeps the residence bound in force when it arrived, so
	// the end of discovery does not discard its backlog at once.
	p.queueDeadline = now.Add(t.bulkQueueAge(now))
	if p.class != classBulk {
		p.interactive = true
		p.queueDeadline = now.Add(maxQueueAge)
		if t.small[p.class].push(p) {
			t.coalescedACKs++
		}
	} else {
		t.queue = append(t.queue, p)
	}
	return nil
}

// release returns an attempt's bytes to the lane's window.
func (p *lane) release(a attempt) {
	p.inflight -= a.bytes
	if a.packet != nil {
		p.classInflight[a.packet.class] -= a.bytes
	}
}

// passed reports that the lane has shown the datagram's last transmission
// missing: the peer received a later one, or too few followed it for that to
// show. Until then the lane may only be late with it. While a lane delivers
// nothing, everything in flight on it times out together; what is sent again
// on the same lane waits behind the originals and arrives as a duplicate
// (`TestSilentLaneIsNotSentRepairs`).
func (p *lane) passed(d *packet) bool {
	return p.receivedHigh > d.lastLaneSeq || p.seq-d.lastLaneSeq < repairTail
}

func (p *lane) up(now time.Time) bool { return !p.lease.IsZero() && now.Sub(p.lease) < pathLease }

func (p *lane) rto() time.Duration {
	variation := p.rttVariation
	if p.feedbackRTT == 0 {
		// Before the first confirmation the variation is unknown, unless the
		// lane measured it while idle. Half the round trip puts the first
		// repair of a radio lane beyond the repair lifetime, and a datagram
		// lost as a transfer starts then reaches the sender's TCP as loss.
		unknown := p.rtt / 2
		if p.idleRTTVariation > 0 {
			unknown = p.idleRTTVariation
		}
		variation = max(variation, unknown)
	}
	return max(minimumRTO, p.rtt+4*variation+p.peerACKInterval(), p.feedbackRTT+4*p.feedbackRTTVariation)
}

// arrived is the last acknowledgement's account of the lane: the sequence it
// acknowledged, and the bytes sent up to it that the peer had not received.
type arrived struct {
	high    uint64
	missing int64
	known   bool
}

// confirmArrived takes the peer's count of received bytes as a cumulative
// acknowledgement. Every acknowledgement says how many bytes the lane has
// delivered, and the sender knows how many it sent up to the acknowledged
// sequence. When no more are missing than at the last acknowledgement, every
// datagram sent between the two arrived, whatever the bitmaps cover.
//
// The bitmaps report each receipt once: 64 datagrams by lane, 256 by
// receipt. When datagrams arrive a hundred or more at a time, the datagrams
// that only a lost acknowledgement reported were never confirmed, and each
// was sent again though it had arrived
// (`TestLostAcknowledgementDoesNotCauseRepairs`). A datagram that arrives late
// lowers the count of missing bytes; then the count proves nothing about the
// others, and the bitmaps decide as before.
func (t *Transport) confirmArrived(p *lane, a acknowledgement, through uint64) {
	missing := int64(through) - int64(a.bytes)
	last := p.arrived
	if a.high <= last.high && last.known {
		return
	}
	p.arrived = arrived{a.high, missing, true}
	if !last.known || missing != last.missing {
		return
	}
	for seq, sent := range p.attempts {
		if seq <= last.high || seq > a.high {
			continue
		}
		if !sent.released {
			p.release(sent)
		}
		p.acked += uint64(sent.bytes)
		if sent.packet != nil {
			p.confirmedWireBytes = min(maxPackets*maxDatagram, p.confirmedWireBytes+sent.bytes)
			sent.packet.acked = true
			delete(t.pending, sent.packet.seq)
		}
		delete(p.attempts, seq)
	}
}

// observeOrder records how far a confirmed datagram was sent before the newest
// one that an earlier acknowledgement confirmed: the path delivered them in
// the other order, and by that much it reorders. What one acknowledgement
// confirms arrived in an order it does not tell.
func (p *lane) observeOrder(now, sent time.Time) {
	if sent.Before(p.newestConfirmed) {
		p.reordering.record(now, float64(p.newestConfirmed.Sub(sent)))
	}
}

// lateBelow is the bytes sent before the acknowledged sequence, not confirmed
// by this acknowledgement, and sent so shortly before the acknowledged
// datagram that they may still be on their way: the path's jitter lets later
// datagrams arrive first. They are missing from the receiver's byte count
// without being lost, and while the rate doubles their number doubles with it.
func (p *lane) lateBelow(a acknowledgement, highSent, now time.Time) uint64 {
	allowance := max(targetQueue, jitterSpreadFactor*p.delayJitter, jitterSpreadFactor*p.idleForwardVariation,
		time.Duration(reorderHeadroom*p.reordering.value(now)))
	var late uint64
	for seq, sent := range p.attempts {
		if seq >= a.high || highSent.Sub(sent.sent) >= allowance {
			continue
		}
		if a.high-seq < 64 && a.mask&(uint64(1)<<(a.high-seq)) != 0 {
			continue
		}
		if sent.packet != nil && (sent.packet.acked || a.received(sent.packet.seq)) {
			continue
		}
		late += uint64(sent.bytes)
	}
	return late
}

// ackIntervalFor bounds acknowledgement bytes to a share of the bytes they
// acknowledge. Acknowledgements bypass pacing, so a fixed cadence costs the
// reverse direction of a slow lane a fixed slice that data cannot use.
func ackIntervalFor(received float64) time.Duration {
	if received <= 0 {
		return maxACKInterval
	}
	interval := time.Duration(ackWireBytes / (ackShare * received) * float64(time.Second))
	return min(maxACKInterval, max(ackInterval, interval))
}

// ackDue reports whether the receiver owes an acknowledgement. The first
// acknowledgements of a burst keep the shortest interval, so sparse traffic
// is confirmed promptly; sustained reception stretches it.
func (r *receiver) ackDue(now time.Time) bool {
	if r.pending >= ackBatchPackets {
		return true
	}
	interval := ackInterval
	if r.streak >= ackStreak {
		interval = ackIntervalFor(r.received.rate(now))
	}
	return now.Sub(r.ackAt) >= interval
}

// peerACKInterval is the acknowledgement cadence observed from the peer.
func (p *lane) peerACKInterval() time.Duration {
	return min(maxACKInterval, max(ackInterval, p.ackGap))
}

// delaySamplesNeeded is the number of queue-delay samples whose minimum
// separates a standing queue from jitter. Consecutive samples of a uniform
// spread s differ by s/3 on average, and the minimum of n of them lies about
// s/(n+1) above the true floor; it must fall well below the threshold before
// exceeding it means a queue.
func (p *lane) delaySamplesNeeded() int {
	// The estimate from before this interval: a queue's onset must not count
	// as the jitter that excuses it.
	jitter := p.intervalJitter
	if p.delayDifferences <= jitterWarmup {
		// A few differences cannot tell jitter from the onset of a queue; the
		// probes' unloaded round-trip variation stands in until they can. One
		// sample ended discovery of a 300 Mbit/s lane with 30 ms of jitter in
		// its first 100 ms, at a capacity measured from a handful of
		// datagrams (`TestJitteryLaneStartsUp`).
		jitter = max(jitter, p.idleRTTVariation)
	}
	spread := jitterSpreadFactor * jitter
	threshold := p.congestionThreshold()
	if spread <= threshold {
		return 1
	}
	return min(maxDelaySamples, int(2*spread/threshold))
}

func (p *lane) observeFeedbackRTT(sample time.Duration) {
	// Receipt-window buffering and reordered arrivals belong in the repair timer.
	// Sampling only the highest physical sequence favours the fastest arrivals.
	if sample <= 0 {
		return
	}
	if p.feedbackRTT == 0 {
		p.feedbackRTT = sample
		p.feedbackRTTVariation = sample / 2
		return
	}
	difference := sample - p.feedbackRTT
	if difference < 0 {
		difference = -difference
	}
	p.feedbackRTTVariation = (3*p.feedbackRTTVariation + difference) / 4
	p.feedbackRTT = (7*p.feedbackRTT + sample) / 8
}

func (p *lane) window() int {
	return min(initialWindowBytes+p.confirmedWireBytes, max(initialWindowBytes, int(p.rate*(p.idleRTT+jitterAllowanceFactor*p.idleRTTVariation+2*targetQueue+p.peerACKInterval()).Seconds())))
}

func (t *Transport) PacingRate(now time.Time) float64 {
	var rate float64
	for _, p := range t.paths {
		if p.eligible(classRealtime, now) {
			rate += p.rate
		}
	}
	return rate
}

// rebaseline takes a round trip measured on the drained lane as unloaded: a
// changed propagation delay must also resize the window, which the target can
// no longer compensate for while it holds below capacity.
//
// The sample moves the estimate halfway. One sample is a draw from the lane's
// jitter, not its round trip: replacing the estimate with it ranked a lane of
// 80 ms mean round trip, at 23 ms, ahead of one of 46 ms, and real-time
// datagrams moved to the slower lane. The variation is left alone: counting
// the step towards it ranked a steady lane behind a slower one after its rate
// changed (VM run 20260930-003806-continuity: voice on the 25 ms lane, a
// 246 ms gap when that lane failed).
func (p *lane) rebaseline(sample time.Duration) {
	p.idleRTT += (sample - p.idleRTT) / 2
}

// copyBudget is the rate allowed for copies of real-time datagrams.
func (t *Transport) copyBudget(now time.Time) float64 {
	return math.Min(redundancyRate, redundancyShare*t.PacingRate(now))
}

func (t *Transport) transmit(p *packet, path *lane, now time.Time) Transmission {
	size := len(p.payload) + wireOverhead
	if p.seq == 0 {
		if p.class == classRealtime {
			path.realtimeOriginals++
			if t.realtimePathKnown && t.realtimePath != path.id {
				t.realtimeMoves++
			}
			t.realtimePath, t.realtimePathKnown = path.id, true
		}
		if p.class == classBulk {
			path.bulkOriginals++
		}
		if p.class != classBulk {
			t.demand[p.class].add(now, float64(size))
		}
		p.repairDeadline = now.Add(maxPacketAge)
		if p.interactive {
			p.repairDeadline = p.created.Add(maxPacketAge)
		}
		t.seq++
		p.seq = t.seq
		if p.interactive {
			t.interactiveSeq++
			p.order = t.interactiveSeq | interactiveBit
		} else {
			t.bulkSeq++
			p.order = t.bulkSeq
		}
		t.pendingOrder = append(t.pendingOrder, p)
	}
	path.beginAttempt(now)
	if path.firstSent.IsZero() {
		path.firstSent = now
	}
	path.attempts[path.seq] = attempt{packet: p, sent: now, bytes: size, through: path.sent + uint64(size)}
	path.inflight += size
	path.classInflight[p.class] += size
	path.sent += uint64(size)
	if p.interactive {
		path.interactiveSent += uint64(size)
	}
	path.pace(now, size)
	if allowed := path.allowed(p.class); allowed > 0 {
		path.classNext[p.class] = maxTime(path.classNext[p.class], now.Add(-2*time.Millisecond)).Add(time.Duration(float64(size) / allowed * float64(time.Second)))
	}
	if p.attempts > 0 {
		path.retries++
	}
	p.attempts++
	p.lastSent, p.lastPath, p.lastLaneSeq = now, path.id, path.seq
	path.lastTransmit = now
	path.lastPayload = now
	t.pending[p.seq] = p
	return Transmission{path.id, dataFrame(t.epoch, t.remote, path.id, path.seq, p.seq, p.order, p.payload)}
}

func (p *lane) pace(now time.Time, bytes int) {
	rate := p.rate + p.feedbackSent.rate(now)
	p.nextSend = maxTime(p.nextSend, now.Add(-2*time.Millisecond)).Add(time.Duration(float64(bytes) / rate * float64(time.Second)))
}

func (t *Transport) Poll(now time.Time) ([]Transmission, error) {
	if err := t.checkTime(now); err != nil {
		return nil, err
	}
	if !t.lastPoll.IsZero() {
		budget := t.copyBudget(now)
		t.redundancyTokens = math.Min(budget/10, t.redundancyTokens+now.Sub(t.lastPoll).Seconds()*budget)
	}
	t.lastPoll = now
	out := make([]Transmission, 0, 8)
	for _, r := range t.receiverOrder {
		path := t.find(r.path)
		if path == nil || !path.up(now) {
			continue
		}
		oldest := t.unreported.oldest()
		if (r.pending > 0 || oldest != 0) && r.ackDue(now) {
			if now.Sub(r.ackAt) < ackGapHorizon {
				r.streak++
			} else {
				r.streak = 0
			}
			r.revision++
			high := t.received.high
			if oldest != 0 {
				// A late receipt must reach the sender even after newer data moved
				// outside the wire bitmap. Its anchor need not be monotonic.
				high = oldest + min(ackReceiptBits-1, high-oldest)
			}
			a := acknowledgement{observed: t.remote, high: r.receipts.high, mask: r.receipts.mask[0], bytes: r.bytes, elapsed: uint64(r.highAt.Sub(r.start)), delay: uint64(now.Sub(r.highAt)), receivedHigh: high, receivedMask: t.received.bitmap(high)}
			out = append(out, Transmission{r.path, ackFrame(t.epoch, r.remoteLane, r.revision, a)})
			path.feedbackSent.add(now, ackWireBytes)
			path.pace(now, ackWireBytes)
			t.unreported.forgetThrough(high)
			r.pending = 0
			r.ackAt = now
		}
	}
	for _, path := range t.paths {
		if !now.Before(path.nextBaseline) && path.queueDelay > targetQueue && path.rate <= math.Max(1.1*minimumRate, path.peakDelivery/2) {
			path.drainUntil = now.Add(max(baselineDrain, 2*path.rtt))
			path.nextBaseline = now.Add(baselineInterval)
			path.baselinePending = true
		}
		for seq, a := range path.attempts {
			if !a.released && now.Sub(a.sent) >= path.rto() {
				// The datagram is sent again now. Whether it was lost is not
				// decided here: a confirmation delayed by jitter or by the
				// acknowledgement cadence arrives after the timeout as often
				// as a loss does. Loss is measured from the receiver's
				// cumulative byte count (lossLedger).
				path.release(a)
				a.released = true
				path.attempts[seq] = a
			}
			if now.Sub(a.sent) >= feedbackHorizon {
				if !a.released {
					path.release(a)
				}
				delete(path.attempts, seq)
			}
		}
		pingInterval := 200 * time.Millisecond
		if path.liveness(now) == LaneDead {
			pingInterval = 50 * time.Millisecond
		}
		if path.up(now) && now.Sub(path.lastTransmit) >= pingInterval {
			path.beginAttempt(now)
			path.lastTransmit = now
			if path.firstSent.IsZero() {
				path.firstSent = now
			}
			path.attempts[path.seq] = attempt{sent: now, bytes: wireOverhead, through: path.sent + wireOverhead}
			path.inflight += wireOverhead
			path.sent += wireOverhead
			path.pace(now, wireOverhead)
			out = append(out, Transmission{path.id, dataFrame(t.epoch, t.remote, path.id, path.seq, 0, 0, nil)})
		}
	}
	t.reserve(now)
	retained := t.pendingOrder[:0]
	for _, p := range t.pendingOrder {
		if p.acked {
			delete(t.pending, p.seq)
			continue
		}
		if now.After(p.repairDeadline) {
			delete(t.pending, p.seq)
			t.expired++
			continue
		}
		retained = append(retained, p)
		previous := t.find(p.lastPath)
		rto := minimumRTO
		if previous != nil {
			rto = previous.rto()
		}
		size := len(p.payload) + wireOverhead
		if p.class == classRealtime && p.attempts == 1 && previous != nil && previous.liveness(now) != LaneLive {
			if path := t.chooseLane(now, p.class, size, p.lastPath, true); path != nil {
				out = append(out, t.transmit(p, path, now))
				continue
			}
		}
		if now.Sub(p.lastSent) < rto || p.attempts >= 4 {
			continue
		}
		path := t.chooseLane(now, classBulk, size, p.lastPath, true)
		if path == nil && (previous == nil || previous.passed(p)) {
			path = t.chooseLane(now, classBulk, size, 0, false)
		}
		if path != nil {
			out = append(out, t.transmit(p, path, now))
		}
	}
	clear(t.pendingOrder[len(retained):])
	t.pendingOrder = retained
	return t.send(now, out), nil
}

// discovering reports whether an up lane has not yet seen congestion. Its
// queue then reflects the lane's pacing, not path capacity, so dropping would
// signal congestion that does not exist.
func (t *Transport) discovering(now time.Time) bool {
	for _, p := range t.paths {
		if p.liveness(now) == LaneLive && p.startup {
			return true
		}
	}
	return false
}

func (t *Transport) slowestRoundTrip(now time.Time) time.Duration {
	var slowest time.Duration
	for _, p := range t.paths {
		if p.liveness(now) == LaneLive {
			slowest = max(slowest, p.rtt)
		}
	}
	return slowest
}

func (t *Transport) bulkQueueAge(now time.Time) time.Duration {
	if t.discovering(now) {
		return discoveryQueueAge
	}
	// The bound is a backstop. It must leave the drop schedule room to act:
	// expiring the head first discards a run of datagrams at once.
	return min(discoveryQueueAge, max(maxBulkQueueAge, t.bulkAQM.target+t.bulkAQM.interval))
}

func (t *Transport) Receive(path PathID, f frame.Control, now time.Time) ([]Delivery, error) {
	deliveries, err := t.receive(path, f, now)
	if err != nil {
		var rejection *RejectionError
		if !errors.As(err, &rejection) {
			panic(err)
		}
		t.rejected[rejection.Cause]++
	}
	return deliveries, err
}

func (t *Transport) receive(path PathID, f frame.Control, now time.Time) ([]Delivery, error) {
	if err := t.checkTime(now); err != nil {
		return nil, err
	}
	h, err := parseHeader(f.Payload)
	if err != nil {
		return nil, err
	}
	if h.epoch != t.remote || f.Seq == 0 {
		return nil, reject(RejectEpoch, "bond: stale epoch or zero sequence")
	}
	local := t.find(path)
	if local == nil || !local.up(now) {
		return nil, reject(RejectPath, "bond: unvalidated path")
	}
	switch f.ControlType {
	case DataType:
		if h.lane != local.remoteID {
			return nil, reject(RejectLane, "bond: DATA lane disagrees with authenticated probe")
		}
		if len(f.Payload) < headerBytes+32 || len(f.Payload) > headerBytes+32+maxDatagram {
			return nil, reject(RejectMalformed, "bond: invalid DATA size")
		}
		destination := Epoch{binary.BigEndian.Uint64(f.Payload[headerBytes:]), binary.BigEndian.Uint64(f.Payload[headerBytes+8:])}
		if destination != t.epoch {
			return nil, reject(RejectEpoch, "bond: DATA for stale local epoch")
		}
		seq := binary.BigEndian.Uint64(f.Payload[headerBytes+16:])
		order := binary.BigEndian.Uint64(f.Payload[headerBytes+24:])
		if seq == 0 && (order != 0 || len(f.Payload) != headerBytes+32) {
			return nil, reject(RejectMalformed, "bond: malformed keepalive")
		}
		if seq != 0 && (order & ^interactiveBit == 0 || len(f.Payload) == headerBytes+32) {
			return nil, reject(RejectMalformed, "bond: invalid delivery sequence or empty datagram")
		}
		r := t.receivers[h.lane]
		if r == nil {
			r = &receiver{path: path, remoteLane: h.lane, start: now, ackAt: now}
			t.receivers[h.lane] = r
			t.receiverOrder = append(t.receiverOrder, r)
		}
		if r.path != path {
			return nil, reject(RejectLane, "bond: receiver lane changed without probe")
		}
		previousHigh := r.receipts.high
		if !r.receipts.mark(f.Seq) {
			t.duplicates++
			return nil, nil
		}
		if f.Seq > previousHigh {
			r.highAt = now
		}
		r.bytes += uint64(len(f.Payload) - headerBytes - dataFieldBytes + wireOverhead)
		r.received.add(now, float64(len(f.Payload)-headerBytes-dataFieldBytes+wireOverhead))
		r.last = now
		r.pending++
		if seq == 0 {
			return nil, nil
		}
		if order&interactiveBit == 0 {
			local.bulkReceived++
		}
		if !t.received.mark(seq) {
			t.duplicates++
			return nil, nil
		}
		t.unreported.mark(seq)
		return []Delivery{{Sequence: order & ^interactiveBit, Interactive: order&interactiveBit != 0, Payload: f.Payload[headerBytes+32:]}}, nil
	case ACKType:
		if h.lane != path {
			return nil, reject(RejectLane, "bond: ACK arrived on wrong path")
		}
		a, err := parseACK(f.Payload)
		if err != nil {
			return nil, err
		}
		if a.observed != t.epoch {
			return nil, reject(RejectEpoch, "bond: ACK for stale local epoch")
		}
		fresh := f.Seq > local.ackRevision
		if a.high > local.seq || a.receivedHigh > t.seq ||
			fresh && (a.bytes < local.ackedBytes || a.elapsed < local.ackedElapsed) ||
			!fresh && (a.bytes > local.ackedBytes || a.elapsed > local.ackedElapsed) ||
			!local.ackReceipts.mark(f.Seq) {
			return nil, reject(RejectACK, "bond: stale or impossible ACK")
		}
		if fresh {
			local.ackRevision = f.Seq
		}
		t.ack(local, a, now, fresh)
		return nil, nil
	default:
		return nil, reject(RejectType, "bond: unknown frame type")
	}
}

func (t *Transport) ack(p *lane, a acknowledgement, now time.Time, fresh bool) {
	silence := p.model.progressAge(now)
	progressed := fresh && (a.high > p.receivedHigh || a.bytes > p.ackedBytes)
	p.receivedHigh = max(p.receivedHigh, a.high)
	if progressed {
		p.model.progressAt = now
		if a.high > p.stall.through {
			p.stall = stall{}
		}
	}
	backlogged := t.queued() > 0 || p.inflight >= p.window()/2
	var sample time.Duration
	var physicalFeedback time.Duration
	newestConfirmed := p.newestConfirmed
	// The acknowledged sequence: when it was sent, and the bytes sent on the
	// lane up to it.
	var highSent time.Time
	var through uint64
	var counted bool
	for seq, pending := range t.pending {
		if a.received(seq) {
			pending.acked = true
			delete(t.pending, seq)
		}
	}
	for seq, sent := range p.attempts {
		if seq <= a.high && a.high-seq < 64 && a.mask&(uint64(1)<<(a.high-seq)) != 0 {
			if !sent.confirmed {
				physicalFeedback = max(physicalFeedback, now.Sub(sent.sent))
				p.observeOrder(now, sent.sent)
				newestConfirmed = maxTime(newestConfirmed, sent.sent)
			}
			if sent.packet != nil {
				p.confirmedWireBytes = min(maxPackets*maxDatagram, p.confirmedWireBytes+sent.bytes)
			}
			if !sent.released {
				p.release(sent)
			}
			p.acked += uint64(sent.bytes)
			if sent.packet != nil {
				sent.packet.acked = true
				delete(t.pending, sent.packet.seq)
			}
			delete(p.attempts, seq)
			if fresh && seq == a.high {
				highSent, through, counted = sent.sent, sent.through, true
			}
			if fresh && seq == a.high && a.delay <= uint64(now.Sub(sent.sent)) {
				sample = now.Sub(sent.sent) - time.Duration(a.delay)
				transit := time.Duration(a.elapsed) - sent.sent.Sub(p.firstSent)
				if sent.packet == nil && now.Sub(p.lastPayload) >= feedbackHorizon {
					if !p.idleForwardKnown {
						p.idleForwardMean, p.idleForwardKnown = transit, true
					} else {
						difference := transit - p.idleForwardMean
						p.idleForwardMean += difference / 8
						if difference < 0 {
							difference = -difference
						}
						p.idleForwardVariation = (3*p.idleForwardVariation + difference) / 4
					}
				}
				if p.baselinePending && !sent.sent.Before(p.drainUntil) {
					// This datagram met no queue of the lane's own. A floor
					// test asks whether it and those after it are late all
					// the same; the control interval that begins here answers.
					if p.floorTesting {
						p.floorTesting, p.floorSampling = false, true
					} else {
						p.rebaseline(sample)
						clear(p.transitBases[:])
					}
					p.haveInterval, p.intervalSamples = false, 0
					p.baselinePending = false
				}
				// Different-sized datagrams have different serialization delays.
				// A bucket spans less than 8 ms at the minimum supported rate.
				p.transitBucket = sent.bytes / transitSizeBucket
				baseline := &p.transitBases[p.transitBucket]
				if !baseline.known || transit < baseline.delay {
					baseline.delay, baseline.known, baseline.observed = transit, true, now
				}
				previous := p.queueDelay
				p.queueDelay = transit - baseline.delay
				// Jitter makes consecutive samples differ by a large part of
				// its spread; a queue changes little between them.
				difference := p.queueDelay - previous
				if difference < 0 {
					difference = -difference
				}
				if p.delayDifferences > 0 {
					p.delayJitter += (difference - p.delayJitter) / 8
				}
				p.delayDifferences = min(jitterWarmup+1, p.delayDifferences+1)
				if !p.haveInterval || p.queueDelay < p.intervalQueueDelay {
					p.intervalQueueDelay, p.haveInterval = p.queueDelay, true
				}
				p.intervalSamples++
			}
		} else if sent.packet != nil && (sent.packet.acked || a.received(sent.packet.seq)) && seq < a.high && a.high-seq >= 64 {
			// The global receipt confirms delivery after the lane bitmap moved on.
			// Release ownership without claiming which physical attempt arrived.
			if !sent.released {
				p.release(sent)
			}
			if !sent.confirmed && sent.packet.attempts == 1 {
				physicalFeedback = max(physicalFeedback, now.Sub(sent.sent))
				p.observeOrder(now, sent.sent)
			}
			delete(p.attempts, seq)
		}
	}
	if fresh && counted {
		t.confirmArrived(p, a, through)
	}
	for _, path := range t.paths {
		var feedbackSample time.Duration
		if path == p {
			feedbackSample = physicalFeedback
		}
		for seq, sent := range path.attempts {
			if sent.packet != nil && (sent.packet.acked || a.received(sent.packet.seq)) && !sent.confirmed {
				sent.packet.acked = true
				if !sent.released {
					path.release(sent)
				}
				if sent.packet.attempts == 1 {
					feedbackSample = max(feedbackSample, now.Sub(sent.sent))
					path.observeOrder(now, sent.sent)
				}
				sent.released = true
				sent.confirmed = true
				path.attempts[seq] = sent
			}
		}
		path.observeFeedbackRTT(feedbackSample)
	}
	p.newestConfirmed = newestConfirmed
	if progressed && silence >= stallSilence && silence < pathLease && physicalFeedback >= silence {
		p.stallEnded(silence)
	}
	if counted {
		p.losses.record(now, a.high, through, a.bytes, p.lateBelow(a, highSent, now))
	}
	if fresh && a.high >= p.roundEnd {
		// A delivery round ends once data sent after it began is acknowledged.
		p.roundEnd, p.roundDone = p.seq+1, true
		p.roundLossy = p.losses.material(now)
	}
	if !fresh {
		return
	}
	if sample > 0 {
		difference := sample - p.rtt
		if difference < 0 {
			difference = -difference
		}
		p.rttVariation = (3*p.rttVariation + difference) / 4
		if now.Sub(p.lastPayload) >= feedbackHorizon {
			if p.idleRTT == 0 {
				p.idleRTT = sample
			} else {
				difference := sample - p.idleRTT
				if difference < 0 {
					difference = -difference
				}
				p.idleRTTVariation = (3*p.idleRTTVariation + difference) / 4
				p.idleRTT = (7*p.idleRTT + sample) / 8
			}
		}
		p.rtt = (7*p.rtt + sample) / 8
		if p.lastACK.IsZero() || sample < p.baseRTT || now.Sub(p.baseAt) > 30*time.Second {
			p.baseRTT, p.baseAt = sample, now
		}
	}
	if p.feedbackAt.IsZero() {
		p.rateBytes, p.rateElapsed, p.feedbackAt = a.bytes, a.elapsed, now
		p.rateSentBytes = p.sent
	} else if a.elapsed-p.rateElapsed >= uint64(deliveryInterval) && now.Sub(p.feedbackAt) >= deliveryInterval {
		rate := float64(a.bytes-p.rateBytes) / time.Duration(a.elapsed-p.rateElapsed).Seconds()
		sendRate := float64(p.sent-p.rateSentBytes) / now.Sub(p.feedbackAt).Seconds()
		p.previousDelivery, p.deliverySample = p.deliverySample, rate
		if p.deliveryRate == 0 {
			p.deliveryRate = rate
			p.sendRate = sendRate
		} else {
			p.deliveryRate = 0.8*p.deliveryRate + 0.2*rate
			p.sendRate = 0.8*p.sendRate + 0.2*sendRate
		}
		p.rateBytes, p.rateElapsed, p.feedbackAt = a.bytes, a.elapsed, now
		p.rateSentBytes = p.sent
		p.sustained.record(now, a.bytes, a.elapsed)
		p.raiseToDelivery(now)
	}
	p.peakDelivery = math.Max(p.peakDelivery, p.deliveryRate)
	p.ackedBytes, p.ackedElapsed = a.bytes, a.elapsed
	p.recentDelivery.record(now, p.deliveryRate)
	p.boundByDelivery(now, t.demand[classRealtime].steady(now))
	if gap := now.Sub(p.lastACK); !p.lastACK.IsZero() && gap < ackGapHorizon {
		// Longer gaps are idle periods, not the peer's cadence.
		p.ackGap += (gap - p.ackGap) / 8
	}
	p.lastACK = now
	if now.Before(p.drainUntil) || p.baselinePending || now.Sub(p.lastAdjust) < max(min(p.rtt, 100*time.Millisecond), 50*time.Millisecond) {
		return
	}
	p.lastAdjust = now
	queueDelay := p.queueDelay
	if p.haveInterval {
		queueDelay = p.intervalQueueDelay
	}
	p.signalDelay = queueDelay
	if p.floorSampling {
		// What was sent after the pause of a floor test found an empty path.
		// One sample near the floor shows the floor where it was; that it
		// moved takes as many late ones as a delay signal does.
		if sample == 0 {
			return
		}
		late := queueDelay > p.congestionThreshold()
		if late && p.intervalSamples < p.delaySamplesNeeded() {
			return
		}
		p.haveInterval, p.intervalSamples, p.intervalJitter = false, 0, p.delayJitter
		p.floorSampling = false
		if late {
			p.floorMoved(sample)
		} else {
			p.queueFound()
		}
		return
	}
	if sample > 0 && queueDelay <= maxWander && p.lightlyLoaded() {
		// Jitter drawn anew for every datagram also lifts the lowest of a few
		// samples above the floor, by the spread over one more than their
		// number. Only what exceeds that is the path's wander.
		scatter := jitterSpreadFactor * p.delayJitter / time.Duration(max(1, p.intervalSamples)+1)
		p.observeWander(max(0, queueDelay-scatter))
	}
	delayed := sample > 0 && queueDelay > p.congestionThreshold()
	if delayed && p.intervalSamples < p.delaySamplesNeeded() {
		// Jitter spreads the samples, and the minimum of a few of them exceeds
		// the threshold by chance; keep collecting before calling it a queue.
		return
	}
	p.haveInterval, p.intervalSamples, p.intervalJitter = false, 0, p.delayJitter
	// Loss since the last control interval: growth of the settled deficit by
	// at least one datagram.
	lost := false
	if settled, known := p.losses.settled(now); known {
		lost = p.lossMarked && settled-p.lossMark >= wireOverhead
		p.lossMark, p.lossMarked = settled, true
	}
	if !backlogged {
		return
	}
	p.adjust(now, lost, delayed, sample > 0, t.laneLimited(now), t.demand[classRealtime].rate(now) > 0, t.demand[classRealtime].steady(now))
}

func (w *receiptWindow) mark(seq uint64) bool {
	if seq > w.high {
		delta := seq - w.high
		if delta >= maxPackets {
			w.mask = [maxPackets / 64]uint64{}
		} else {
			words, shift := int(delta/64), uint(delta%64)
			for i := len(w.mask) - 1; i >= 0; i-- {
				var value uint64
				if i >= words {
					value = w.mask[i-words] << shift
				}
				if shift > 0 && i > words {
					value |= w.mask[i-words-1] >> (64 - shift)
				}
				w.mask[i] = value
			}
		}
		w.high = seq
	}
	delta := w.high - seq
	if delta >= maxPackets {
		return false
	}
	bit := uint64(1) << (delta % 64)
	if w.mask[delta/64]&bit != 0 {
		return false
	}
	w.mask[delta/64] |= bit
	return true
}

func (w *receiptWindow) oldest() uint64 {
	for i := len(w.mask) - 1; i >= 0; i-- {
		if word := w.mask[i]; word != 0 {
			return w.high - uint64(i*64+bits.Len64(word)-1)
		}
	}
	return 0
}

func (w *receiptWindow) bitmap(high uint64) [ackReceiptWords]uint64 {
	var result [ackReceiptWords]uint64
	for offset := uint64(0); offset < ackReceiptBits && offset < high; offset++ {
		delta := w.high - (high - offset)
		if delta < maxPackets && w.mask[delta/64]&(uint64(1)<<(delta%64)) != 0 {
			result[offset/64] |= uint64(1) << (offset % 64)
		}
	}
	return result
}

func (w *receiptWindow) forgetThrough(high uint64) {
	if high >= w.high {
		clear(w.mask[:])
		return
	}
	delta := w.high - high
	if delta < maxPackets {
		w.mask[delta/64] &= (uint64(1) << (delta % 64)) - 1
		clear(w.mask[delta/64+1:])
	}
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (t *Transport) Snapshot(now time.Time) Snapshot {
	s := Snapshot{QueueDrops: t.drops, AdmissionDrops: t.admissionDrops, AQMDrops: t.aqmDrops, InteractiveQueueDrops: t.interactiveDrops, InteractiveQueued: t.small[classRealtime].count + t.small[classSmall].count, CoalescedACKs: t.coalescedACKs, Expired: t.expired, Duplicates: t.duplicates, RealtimeMoves: t.realtimeMoves, Rejected: t.rejected}
	s.RealtimeQueueDrops, s.SmallTCPQueueDrops = t.smallQueueDrops[classRealtime], t.smallQueueDrops[classSmall]
	for _, p := range t.paths {
		baseline := p.transitBases[p.transitBucket]
		age := time.Duration(0)
		if baseline.known {
			age = max(0, now.Sub(baseline.observed))
		}
		liveness := p.liveness(now)
		s.Paths = append(s.Paths, PathStats{
			TransitFloor: baseline.delay, TransitFloorKnown: baseline.known, TransitFloorAge: age,
			PathDelay: p.idleRTT, Rank: p.latency(), Liveness: liveness,
			LivenessAge: p.model.progressAge(now), ACKProgressKnown: !p.model.progressAt.IsZero(),
			Path: p.id, Capacity: p.control.capacity, Rate: p.rate, SendRate: p.sendRate, DeliveryRate: p.deliveryRate,
			RTT: p.rtt, RTTVariation: p.rttVariation, IdleRTTVariation: p.idleRTTVariation,
			IdleForwardVariation: p.idleForwardVariation,
			FeedbackRTT:          p.feedbackRTT, FeedbackRTTVariation: p.feedbackRTTVariation,
			BaseRTT: p.baseRTT, QueueDelay: p.queueDelay,
			InFlight: p.inflight, Window: p.window(), Sent: p.sent, ACKed: p.acked, Retransmits: p.retries, InteractiveSent: p.interactiveSent, RealtimeOriginals: p.realtimeOriginals, BulkOriginals: p.bulkOriginals, BulkReceived: p.bulkReceived,
			Up:          p.eligible(classRealtime, now),
			Discovering: p.startup,
			Threshold:   p.congestionThreshold(),
			Decisions:   p.decisions,
		})
	}
	return s
}

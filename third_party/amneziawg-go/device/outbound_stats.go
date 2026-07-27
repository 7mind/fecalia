package device

import "sync/atomic"

var outboundBatchFrameBounds = [...]uint64{1, 2, 4, 8, 16, 32, 64, 128}

type OutboundBatchHistogram struct {
	Count   uint64
	Frames  uint64
	Buckets map[uint64]uint64
}

type OutboundStats struct {
	TUNBytes                  uint64
	TUNBatchFrames            OutboundBatchHistogram
	SendBytes                 uint64
	SendBatchFrames           OutboundBatchHistogram
	EncryptionQueueContainers uint64
	PeerQueueContainers       uint64
	PeerQueueHighWater        uint64
	ActiveSendFrames          uint64
	ActiveSendBytes           uint64
	ActiveSendFramesHighWater uint64
	ActiveSendBytesHighWater  uint64
	AdmissionLimitBytes       uint64
	AdmissionRetainedBytes    uint64
	AdmissionHighWaterBytes   uint64
	AdmissionWaits            uint64
	AdmissionWaitNanoseconds  uint64
	AdmissionOversizeBatches  uint64
}

type outboundStats struct {
	tunBytes           atomic.Uint64
	tunBatches         outboundBatchHistogram
	sendBytes          atomic.Uint64
	sendBatches        outboundBatchHistogram
	peerQueueHighWater atomic.Uint64
	activeSendFrames   atomic.Int64
	activeSendBytes    atomic.Int64
	activeFramesHigh   atomic.Uint64
	activeBytesHigh    atomic.Uint64
}

type outboundBatchHistogram struct {
	count   atomic.Uint64
	frames  atomic.Uint64
	buckets [len(outboundBatchFrameBounds)]atomic.Uint64
}

func (s *outboundStats) recordTUNBatch(frames, bytes int) {
	s.tunBytes.Add(uint64(bytes))
	s.tunBatches.record(frames)
}

func (s *outboundStats) recordSendBatch(frames, bytes int) {
	s.sendBytes.Add(uint64(bytes))
	s.sendBatches.record(frames)
}

func (s *outboundStats) recordPeerQueueDepth(depth int) {
	updateHighWater(&s.peerQueueHighWater, uint64(depth))
}

func (s *outboundStats) addActiveSend(frames, bytes int64) {
	activeFrames := s.activeSendFrames.Add(frames)
	activeBytes := s.activeSendBytes.Add(bytes)
	if activeFrames < 0 || activeBytes < 0 {
		panic("device: negative active outbound send")
	}
	updateHighWater(&s.activeFramesHigh, uint64(activeFrames))
	updateHighWater(&s.activeBytesHigh, uint64(activeBytes))
}

func (h *outboundBatchHistogram) record(frames int) {
	h.count.Add(1)
	h.frames.Add(uint64(frames))
	for i, bound := range outboundBatchFrameBounds {
		if uint64(frames) <= bound {
			h.buckets[i].Add(1)
		}
	}
}

func (h *outboundBatchHistogram) snapshot() OutboundBatchHistogram {
	buckets := make(map[uint64]uint64, len(outboundBatchFrameBounds))
	for i, bound := range outboundBatchFrameBounds {
		buckets[bound] = h.buckets[i].Load()
	}
	return OutboundBatchHistogram{
		Count:   h.count.Load(),
		Frames:  h.frames.Load(),
		Buckets: buckets,
	}
}

func updateHighWater(high *atomic.Uint64, value uint64) {
	for current := high.Load(); value > current; current = high.Load() {
		if high.CompareAndSwap(current, value) {
			return
		}
	}
}

func (device *Device) OutboundStats() OutboundStats {
	var peerQueueContainers uint64
	var admissionLimitBytes uint64
	var admissionRetainedBytes uint64
	var admissionHighWaterBytes uint64
	var admissionWaits uint64
	var admissionWaitNanoseconds uint64
	var admissionOversizeBatches uint64
	device.peers.RLock()
	for _, peer := range device.peers.keyMap {
		peerQueueContainers += uint64(len(peer.queue.outbound.c))
		admission := peer.outboundAdmission.snapshot()
		admissionLimitBytes += uint64(admission.limitBytes)
		admissionRetainedBytes += uint64(admission.retainedBytes)
		admissionHighWaterBytes += uint64(admission.highWaterBytes)
		admissionWaits += admission.waits
		admissionWaitNanoseconds += uint64(admission.waitDuration)
		admissionOversizeBatches += admission.oversize
	}
	device.peers.RUnlock()

	var encryptionQueueContainers uint64
	if device.queue.encryption != nil {
		encryptionQueueContainers = uint64(len(device.queue.encryption.c))
	}

	return OutboundStats{
		TUNBytes:                  device.outbound.tunBytes.Load(),
		TUNBatchFrames:            device.outbound.tunBatches.snapshot(),
		SendBytes:                 device.outbound.sendBytes.Load(),
		SendBatchFrames:           device.outbound.sendBatches.snapshot(),
		EncryptionQueueContainers: encryptionQueueContainers,
		PeerQueueContainers:       peerQueueContainers,
		PeerQueueHighWater:        device.outbound.peerQueueHighWater.Load(),
		ActiveSendFrames:          uint64(device.outbound.activeSendFrames.Load()),
		ActiveSendBytes:           uint64(device.outbound.activeSendBytes.Load()),
		ActiveSendFramesHighWater: device.outbound.activeFramesHigh.Load(),
		ActiveSendBytesHighWater:  device.outbound.activeBytesHigh.Load(),
		AdmissionLimitBytes:       admissionLimitBytes,
		AdmissionRetainedBytes:    admissionRetainedBytes,
		AdmissionHighWaterBytes:   admissionHighWaterBytes,
		AdmissionWaits:            admissionWaits,
		AdmissionWaitNanoseconds:  admissionWaitNanoseconds,
		AdmissionOversizeBatches:  admissionOversizeBatches,
	}
}

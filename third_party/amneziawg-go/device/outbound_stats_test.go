package device

import "testing"

func TestOutboundStatsBatchHistograms(t *testing.T) {
	var device Device
	device.outbound.recordTUNBatch(1, 100)
	device.outbound.recordTUNBatch(8, 800)
	device.outbound.recordTUNBatch(128, 64*1024)
	device.outbound.recordSendBatch(4, 400)
	device.outbound.recordPeerQueueDepth(1)
	device.outbound.addActiveSend(4, 400)

	got := device.OutboundStats()

	if got.TUNBytes != 100+800+64*1024 {
		t.Errorf("TUNBytes = %d", got.TUNBytes)
	}
	if got.TUNBatchFrames.Count != 3 || got.TUNBatchFrames.Frames != 137 {
		t.Errorf("TUNBatchFrames = %+v", got.TUNBatchFrames)
	}
	for bound, want := range map[uint64]uint64{
		1: 1, 2: 1, 4: 1, 8: 2, 16: 2, 32: 2, 64: 2, 128: 3,
	} {
		if got.TUNBatchFrames.Buckets[bound] != want {
			t.Errorf("TUNBatchFrames bucket %d = %d, want %d", bound, got.TUNBatchFrames.Buckets[bound], want)
		}
	}
	if got.SendBytes != 400 || got.SendBatchFrames.Count != 1 || got.SendBatchFrames.Frames != 4 {
		t.Errorf("send stats = bytes %d histogram %+v", got.SendBytes, got.SendBatchFrames)
	}
	if got.PeerQueueHighWater != 1 {
		t.Errorf("PeerQueueHighWater = %d, want 1", got.PeerQueueHighWater)
	}
	if got.ActiveSendFrames != 4 || got.ActiveSendBytes != 400 {
		t.Errorf("active send = %d frames, %d bytes", got.ActiveSendFrames, got.ActiveSendBytes)
	}
	if got.ActiveSendFramesHighWater != 4 || got.ActiveSendBytesHighWater != 400 {
		t.Errorf("active send high water = %d frames, %d bytes", got.ActiveSendFramesHighWater, got.ActiveSendBytesHighWater)
	}

	device.outbound.addActiveSend(-4, -400)
	got = device.OutboundStats()
	if got.ActiveSendFrames != 0 || got.ActiveSendBytes != 0 {
		t.Errorf("active send after completion = %d frames, %d bytes", got.ActiveSendFrames, got.ActiveSendBytes)
	}
}

func TestOutboundStatsAggregatePerPeerAdmission(t *testing.T) {
	var device Device
	device.peers.keyMap = make(map[NoisePublicKey]*Peer)
	for index := range 2 {
		admission := newOutboundAdmission(2_000)
		admission.start()
		reservation, ok := admission.reserve(int64(500 + index*100))
		if !ok {
			t.Fatal("running admission rejected reservation")
		}
		defer reservation.release()
		var key NoisePublicKey
		key[0] = byte(index + 1)
		peer := &Peer{outboundAdmission: admission}
		peer.queue.outbound = &autodrainingOutboundQueue{
			c: make(chan *QueueOutboundElementsContainer, 1),
		}
		device.peers.keyMap[key] = peer
	}

	got := device.OutboundStats()
	if got.AdmissionLimitBytes != 4_000 ||
		got.AdmissionRetainedBytes != 1_100 ||
		got.AdmissionHighWaterBytes != 1_100 {
		t.Fatalf("admission byte stats = limit %d retained %d high-water %d",
			got.AdmissionLimitBytes,
			got.AdmissionRetainedBytes,
			got.AdmissionHighWaterBytes,
		)
	}
}

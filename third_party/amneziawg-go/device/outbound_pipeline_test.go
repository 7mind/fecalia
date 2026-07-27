package device

import "testing"

func TestOutboundStatsTrackTUNToBindSend(t *testing.T) {
	pair := genTestPair(t, false)
	pair.Send(t, Ping, nil)

	stats := pair[1].dev.OutboundStats()
	if stats.TUNBatchFrames.Count == 0 || stats.TUNBatchFrames.Frames == 0 || stats.TUNBytes == 0 {
		t.Fatalf("TUN outbound stats = %+v, want observed traffic", stats)
	}
	if stats.SendBatchFrames.Count == 0 || stats.SendBatchFrames.Frames == 0 || stats.SendBytes == 0 {
		t.Fatalf("Bind.Send outbound stats = %+v, want observed traffic", stats)
	}
	if stats.EncryptionQueueContainers > QueueOutboundSize {
		t.Errorf("encryption queue depth = %d, capacity %d", stats.EncryptionQueueContainers, QueueOutboundSize)
	}
	if stats.PeerQueueContainers > QueueOutboundSize {
		t.Errorf("peer queue depth = %d, capacity %d", stats.PeerQueueContainers, QueueOutboundSize)
	}
	if stats.PeerQueueHighWater > QueueOutboundSize {
		t.Errorf("peer queue high water = %d, capacity %d", stats.PeerQueueHighWater, QueueOutboundSize)
	}
}

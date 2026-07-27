package device

import "testing"

func TestOutboundPipelineAccountsExactWireBytesWithinAdmissionLimit(t *testing.T) {
	pair := genTestPair(t, false)
	const limit = 10_000
	if err := pair[1].dev.SetOutboundAdmissionLimit(limit); err != nil {
		t.Fatal(err)
	}

	before := pair[1].dev.OutboundStats()
	pair.Send(t, Ping, nil)
	after := pair[1].dev.OutboundStats()

	sent := after.SendBytes - before.SendBytes
	if sent == 0 {
		t.Fatal("pipeline sent zero WireGuard bytes")
	}
	if after.AdmissionLimitBytes != limit {
		t.Fatalf("admission limit = %d, want %d", after.AdmissionLimitBytes, limit)
	}
	if after.AdmissionRetainedBytes != 0 {
		t.Fatalf("retained admission bytes after delivery = %d, want 0", after.AdmissionRetainedBytes)
	}
	if after.AdmissionHighWaterBytes != sent {
		t.Fatalf("admission high-water = %d, exact sent bytes = %d", after.AdmissionHighWaterBytes, sent)
	}
	if after.AdmissionHighWaterBytes > after.AdmissionLimitBytes {
		t.Fatalf("admission high-water = %d, limit = %d", after.AdmissionHighWaterBytes, after.AdmissionLimitBytes)
	}
	if after.AdmissionOversizeBatches != 0 {
		t.Fatalf("oversize admitted batches = %d, want 0", after.AdmissionOversizeBatches)
	}
}

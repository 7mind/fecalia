package device

import "testing"

func TestOutboundPipelineAccountsWireBytes(t *testing.T) {
	pair := genTestPair(t, false)

	before := pair[1].dev.OutboundStats()
	pair.Send(t, Ping, nil)
	after := pair[1].dev.OutboundStats()

	if after.SendBytes == before.SendBytes {
		t.Fatal("pipeline sent zero WireGuard bytes")
	}
}

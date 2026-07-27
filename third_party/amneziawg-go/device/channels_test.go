package device

import (
	"testing"

	"github.com/amnezia-vpn/amneziawg-go/conn"
)

func TestOutboundQueuesBoundAdmissionAheadOfConsumer(t *testing.T) {
	deviceQueue := newOutboundQueue()
	defer deviceQueue.wg.Done()
	assertOneContainerAheadOfConsumer(t, "encryption", deviceQueue.c)

	peerQueue := newAutodrainingOutboundQueue(&Device{})
	assertOneContainerAheadOfConsumer(t, "peer", peerQueue.c)
}

func assertOneContainerAheadOfConsumer(t *testing.T, name string, queue chan *QueueOutboundElementsContainer) {
	t.Helper()
	fullGSORead := &QueueOutboundElementsContainer{
		elems: make([]*QueueOutboundElement, conn.IdealBatchSize),
	}
	select {
	case queue <- fullGSORead:
	default:
		t.Fatalf("%s outbound queue did not admit one GSO container", name)
	}
	select {
	case queue <- fullGSORead:
		t.Fatalf("%s outbound queue admitted more than one GSO container without a consumer; TUN traffic can accumulate ahead of the shaped Bind.Send boundary", name)
	default:
	}
}

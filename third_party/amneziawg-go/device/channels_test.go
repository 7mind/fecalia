package device

import "testing"

func TestOutboundQueuesRequireConsumerBeforeAdmission(t *testing.T) {
	deviceQueue := newOutboundQueue()
	defer deviceQueue.wg.Done()
	assertRequiresConsumer(t, "encryption", deviceQueue.c)

	peerQueue := newAutodrainingOutboundQueue(&Device{})
	assertRequiresConsumer(t, "peer", peerQueue.c)
}

func assertRequiresConsumer(t *testing.T, name string, queue chan *QueueOutboundElementsContainer) {
	t.Helper()
	select {
	case queue <- &QueueOutboundElementsContainer{}:
		t.Fatalf("%s outbound queue admitted a container without a consumer; TUN traffic can accumulate ahead of the shaped Bind.Send boundary", name)
	default:
	}
}

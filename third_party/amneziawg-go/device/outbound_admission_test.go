package device

import (
	"testing"
	"time"
)

const admissionTestTimeout = 250 * time.Millisecond

func reserveAsync(a *outboundAdmission, bytes int64) <-chan *outboundAdmissionReservation {
	result := make(chan *outboundAdmissionReservation, 1)
	go func() {
		reservation, ok := a.reserve(bytes)
		if !ok {
			result <- nil
			return
		}
		result <- reservation
	}()
	return result
}

func requireBlocked(
	t *testing.T,
	admission *outboundAdmission,
	previousWaits uint64,
	result <-chan *outboundAdmissionReservation,
) {
	t.Helper()
	deadline := time.Now().Add(admissionTestTimeout)
	for admission.snapshot().waits == previousWaits {
		select {
		case reservation := <-result:
			if reservation != nil {
				reservation.release()
			}
			t.Fatal("outbound admission accepted a batch beyond its byte budget")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("outbound admission did not enter a byte-budget wait")
		}
	}
	select {
	case reservation := <-result:
		if reservation != nil {
			reservation.release()
		}
		t.Fatal("outbound admission waiter completed before service release")
	default:
	}
}

func requireReservation(t *testing.T, result <-chan *outboundAdmissionReservation) *outboundAdmissionReservation {
	t.Helper()
	select {
	case reservation := <-result:
		if reservation == nil {
			t.Fatal("outbound admission canceled a running peer")
		}
		return reservation
	case <-time.After(admissionTestTimeout):
		t.Fatal("timed out waiting for outbound admission")
		return nil
	}
}

func TestOutboundAdmissionBlocksWholeBatchUntilServiceReleased(t *testing.T) {
	admission := newOutboundAdmission(2_000)
	admission.start()

	first := requireReservation(t, reserveAsync(admission, 1_500))
	secondResult := reserveAsync(admission, 1_500)
	requireBlocked(t, admission, 0, secondResult)

	first.release()
	second := requireReservation(t, secondResult)
	got := admission.snapshot()
	if got.retainedBytes != 1_500 {
		t.Fatalf("retained bytes = %d, want 1500", got.retainedBytes)
	}
	if got.highWaterBytes > 2_000 {
		t.Fatalf("high-water bytes = %d, want <= 2000", got.highWaterBytes)
	}
	if got.waits != 1 {
		t.Fatalf("admission waits = %d, want 1", got.waits)
	}
	second.release()
}

func TestOutboundAdmissionPreservesOneOversizeWholeBatch(t *testing.T) {
	admission := newOutboundAdmission(2_000)
	admission.start()

	oversize := requireReservation(t, reserveAsync(admission, 2_500))
	blocked := reserveAsync(admission, 1)
	requireBlocked(t, admission, 0, blocked)
	got := admission.snapshot()
	if got.retainedBytes != 2_500 || got.highWaterBytes != 2_500 || got.oversize != 1 {
		t.Fatalf("oversize snapshot = %+v, want retained/high-water 2500 and one oversize batch", got)
	}

	oversize.release()
	next := requireReservation(t, blocked)
	next.release()
}

func TestOutboundAdmissionStopCancelsWaiter(t *testing.T) {
	admission := newOutboundAdmission(1_000)
	admission.start()
	first := requireReservation(t, reserveAsync(admission, 1_000))
	blocked := reserveAsync(admission, 1)
	requireBlocked(t, admission, 0, blocked)

	admission.stop()
	select {
	case reservation := <-blocked:
		if reservation != nil {
			reservation.release()
			t.Fatal("stopped admission returned a reservation")
		}
	case <-time.After(admissionTestTimeout):
		t.Fatal("stopping admission did not cancel its waiter")
	}
	first.release()
}

func TestOutboundAdmissionIsIndependentPerPeer(t *testing.T) {
	peerA := newOutboundAdmission(1_000)
	peerB := newOutboundAdmission(1_000)
	peerA.start()
	peerB.start()

	a := requireReservation(t, reserveAsync(peerA, 1_000))
	aBlocked := reserveAsync(peerA, 1)
	requireBlocked(t, peerA, 0, aBlocked)
	b := requireReservation(t, reserveAsync(peerB, 1_000))

	b.release()
	a.release()
	nextA := requireReservation(t, aBlocked)
	nextA.release()
}

func TestOutboundAdmissionCompletionRetainsUntilTerminalCallback(t *testing.T) {
	admission := newOutboundAdmission(2_000)
	admission.start()
	reservation, ok := admission.reserve(1_000)
	if !ok {
		t.Fatal("running admission rejected reservation")
	}
	container := &QueueOutboundElementsContainer{reservation: reservation}

	complete := container.takeOutboundAdmissionCompletion()
	if complete == nil {
		t.Fatal("container did not transfer admission completion")
	}
	if container.reservation != nil {
		t.Fatal("container retained reservation after ownership transfer")
	}
	if got := admission.snapshot().retainedBytes; got != 1_000 {
		t.Fatalf("retained bytes before terminal completion = %d, want 1000", got)
	}

	complete()
	if got := admission.snapshot().retainedBytes; got != 0 {
		t.Fatalf("retained bytes after terminal completion = %d, want 0", got)
	}
}

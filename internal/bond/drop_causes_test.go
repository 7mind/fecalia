package bond_test

import (
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

func TestQueueDropCausesAreReportedSeparately(t *testing.T) {
	now := time.Unix(100, 0)
	a := bond.New(bond.Epoch{Boot: 1, Generation: 1})
	// Bulk is admitted up to the bound less the headroom kept for small datagrams.
	const limit = 8192 - 1024
	for range limit + 3 {
		if err := a.Enqueue(make([]byte, 1200), bond.PacketMetadata{}, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Enqueue(make([]byte, 200), bond.PacketMetadata{Flow: bond.FlowID{4, 17}}, now); err != nil {
		t.Fatal(err)
	}
	snapshot := a.Snapshot(now)
	if snapshot.AdmissionDrops != 3 || snapshot.QueueDrops != 3 || snapshot.AQMDrops != 0 {
		t.Fatalf("admission overflow reported as %+v", snapshot)
	}
	if snapshot.InteractiveQueueDrops != 0 || snapshot.InteractiveQueued != 1 {
		t.Fatalf("a small datagram behind a full bulk queue: %+v", snapshot)
	}
}

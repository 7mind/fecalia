package bind

import (
	"runtime"
	"testing"
)

func TestFECStagingSnapshotIsCoherentUnderConcurrentScrape(t *testing.T) {
	fs := &fecSender{}
	peer := &peerState{name: "edge"}
	peer.fecSend.Store(fs)
	m := &Multipath{peers: []*peerState{peer}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100_000; i++ {
			fs.publishStaging(i%32 + 1)
			runtime.Gosched()
			fs.publishStaging(0)
			runtime.Gosched()
		}
	}()

	observations := 0
	for {
		select {
		case <-done:
			if observations == 0 {
				t.Fatal("concurrent staging scrape made no observations")
			}
			return
		default:
		}
		snapshot := m.PeerSnapshots()[0].FEC
		observations++
		if (snapshot.StagedGroups == 0) != (snapshot.StagedDataFrames == 0) {
			t.Fatalf("torn staging snapshot group=%d frames=%d",
				snapshot.StagedGroups,
				snapshot.StagedDataFrames,
			)
		}
		if snapshot.StagedGroups > 1 || snapshot.StagedDataFrames > 32 {
			t.Fatalf("out-of-range staging snapshot group=%d frames=%d",
				snapshot.StagedGroups,
				snapshot.StagedDataFrames,
			)
		}
	}
}

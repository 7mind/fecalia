package bind

import (
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

func TestAdaptivePeerRestartIsReportedOnce(t *testing.T) {
	m, _ := newProbingMultipath(t, loopbackPaths(1), testKey(t, 0x42), newFakeClock())
	restarts := make(chan string, 4)
	m.SetOnPeerRestart(func(peer string) { restarts <- peer })
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	_, source := rawPeer(t)
	a := m.adaptive.Load()
	path := m.peers[0].paths[0]
	reported := func() bool {
		select {
		case <-restarts:
			return true
		case <-time.After(100 * time.Millisecond):
			return false
		}
	}

	first := bond.Hello(bond.Epoch{Boot: 987, Generation: 1}, 0)
	a.learn(path, source, first, true)
	if reported() {
		t.Fatal("first contact with a peer reported as a restart")
	}
	a.learn(path, source, bond.Hello(bond.Epoch{Boot: 987, Generation: 2}, 0), true)
	if reported() {
		t.Fatal("new generation of the same process reported as a restart")
	}
	a.learn(path, source, bond.Hello(bond.Epoch{Boot: 988, Generation: 1}, 0), false)
	if reported() {
		t.Fatal("unadopted process epoch reported as a restart")
	}
	restarted := bond.Hello(bond.Epoch{Boot: 988, Generation: 1}, 0)
	a.learn(path, source, restarted, true)
	if !reported() {
		t.Fatal("restart of a known peer not reported")
	}
	a.learn(path, source, restarted, true)
	if reported() {
		t.Fatal("restart reported again for the same process epoch")
	}
}

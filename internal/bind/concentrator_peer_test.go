package bind

import (
	"io"
	"net/netip"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// concPeerWiring builds ONE concentrator peer's send scheduler, boot-time per-path prober set,
// and runtime prober factory — all keyed on psk — over paths, exactly as device.buildScheduler
// does in production. sessionID is distinct per call so a test can prove the fan-out mints
// genuinely per-peer probers (not shared handles), though the load-bearing per-peer distinction
// is the psk.
func concPeerWiring(t testing.TB, paths []config.Path, psk config.Key, sessionID uint64, clk telemetry.Clock) (sched.Scheduler, []*telemetry.Prober, ProberFactory) {
	t.Helper()
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	cfg := telemetry.ProberConfig{
		LossWindow: 0,
		Liveness:   telemetry.LivenessConfig{DownAfter: testProbeDownAfter, UpAfterSuccesses: testProbeUpSucc},
	}
	newProber := func(name string, id uint8) *telemetry.Prober {
		return telemetry.NewProber(name, id, sessionID, psk, cfg, clk, lg)
	}
	probers := make([]*telemetry.Prober, len(paths))
	health := make([]sched.PathHealth, len(paths))
	for i := range paths {
		probers[i] = newProber(paths[i].Name, uint8(i))
		health[i] = probers[i]
	}
	scheduler, err := sched.NewActiveBackup(health, sched.Config{FailbackAfter: time.Hour}, clk, lg)
	if err != nil {
		t.Fatalf("build concentrator peer scheduler: %v", err)
	}
	return scheduler, probers, newProber
}

// decodesAsProbe reports whether raw decodes and MAC-verifies as a PROBE under psk. It is the
// operational test of "a prober is keyed on THIS psk": a probe the prober emitted verifies
// under a codec of the same psk and under NO other.
func decodesAsProbe(t testing.TB, psk config.Key, raw []byte) bool {
	t.Helper()
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatalf("build codec: %v", err)
	}
	fr, err := codec.Decode(raw)
	if err != nil {
		return false
	}
	_, ok := fr.(frame.Probe)
	return ok
}

// assertProberKeyedOn asserts prober emits a PROBE that verifies under wantPSK and NOT under
// otherPSK — i.e. the prober authenticates on wantPSK. This is the mutation-sensitive check: a
// prober mistakenly keyed on otherPSK would verify under otherPSK and fail here.
func assertProberKeyedOn(t testing.TB, who string, prober *telemetry.Prober, wantPSK, otherPSK config.Key) {
	t.Helper()
	raw, err := prober.SendProbe()
	if err != nil {
		t.Fatalf("%s: SendProbe: %v", who, err)
	}
	if !decodesAsProbe(t, wantPSK, raw) {
		t.Fatalf("%s: probe does NOT verify under its own peer's psk (prober not keyed on the right psk)", who)
	}
	if decodesAsProbe(t, otherPSK, raw) {
		t.Fatalf("%s: probe verifies under the OTHER peer's psk — the prober is keyed on the wrong peer's psk", who)
	}
}

// TestConcentratorTwoPeersEachOwnWiring is the T93 acceptance: a 2-peer concentrator (the
// primary keyed on pskA, a second peer registered via AddConcentratorPeer keyed on pskB) yields
// two peerStates over the SAME shared socket, each with its OWN scheduler, prober set, and
// stable virtual endpoint, and each with a per-(peer,path) view whose prober is keyed on that
// peer's psk.
func TestConcentratorTwoPeersEachOwnWiring(t *testing.T) {
	pskA := testKey(t, 0x11)
	pskB := testKey(t, 0x22)
	clk := newFakeClock()
	paths := loopbackPaths(1) // one shared socket, path "a"
	m, primaryProbers, primarySched := newProbingMultipath(t, paths, pskA, clk)

	betaSched, betaProbers, betaFactory := concPeerWiring(t, paths, pskB, 0x0BEECAFE, clk)
	if err := m.AddConcentratorPeer("beta", pskB, betaSched, betaProbers, betaFactory); err != nil {
		t.Fatalf("AddConcentratorPeer: %v", err)
	}

	// Registration is refused after Open (views are rebuilt by Open from the registered set).
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	if names := m.BoundPeerNames(); len(names) != 2 || names[0] != "" || names[1] != "beta" {
		t.Fatalf("BoundPeerNames = %v, want [\"\" \"beta\"]", names)
	}

	primary := m.peerState
	beta := m.peersByName["beta"]
	if beta == nil {
		t.Fatal("second peer not registered under its name")
	}

	// Distinct schedulers, prober sets, and virtual endpoints per peer.
	if primary.scheduler != primarySched || beta.scheduler != betaSched {
		t.Fatal("peers do not hold their own schedulers")
	}
	if primary.scheduler == beta.scheduler {
		t.Fatal("the two peers share ONE scheduler")
	}
	if primary.virt == beta.virt {
		t.Fatal("the two peers share ONE virtual endpoint (invariant A1: one virt per peer)")
	}
	if &primaryProbers[0] == &betaProbers[0] {
		t.Fatal("the two peers share ONE prober set")
	}
	virts := m.PeerVirtEndpoints()
	if len(virts) != 2 || virts[0] == virts[1] {
		t.Fatalf("PeerVirtEndpoints = %v, want two distinct endpoints", virts)
	}

	// Each peer holds a per-(peer,path) VIEW of the ONE shared socket, with its OWN prober.
	if len(m.shared) != 1 {
		t.Fatalf("shared sockets = %d, want 1", len(m.shared))
	}
	pa := peerPathByName(primary, "a")
	pb := peerPathByName(beta, "a")
	if pa == nil || pb == nil {
		t.Fatalf("boot path 'a' missing: primary=%v beta=%v", pathNamesOfPeer(primary), pathNamesOfPeer(beta))
	}
	if pa.sharedPathState != pb.sharedPathState {
		t.Fatal("the two peers' path 'a' must VIEW the same shared socket, not duplicate it")
	}
	if pa.prober == pb.prober {
		t.Fatal("the two peers share ONE prober for path 'a'")
	}
	if pa.codec == pb.codec {
		t.Fatal("the two peers share ONE receive codec for path 'a'")
	}
	// The boot probers are keyed on each peer's OWN psk (mutation-sensitive).
	assertProberKeyedOn(t, "primary boot prober 'a'", pa.prober, pskA, pskB)
	assertProberKeyedOn(t, "beta boot prober 'a'", pb.prober, pskB, pskA)
}

// TestConcentratorRuntimePathFanOutPerPeerPSK is the T93 runtime-path acceptance: with two peers
// bound, a runtime AddPath fans a per-(peer,path) prober out to BOTH peers, each keyed on its
// OWN psk, and a RemovePath tears each peer's per-(peer,path) prober down. It exercises the
// runtime add/remove flow while >=2 peers are bound (the runtime path add/remove flow must work
// with multiple peers bound).
func TestConcentratorRuntimePathFanOutPerPeerPSK(t *testing.T) {
	pskA := testKey(t, 0x33)
	pskB := testKey(t, 0x44)
	clk := newFakeClock()
	paths := loopbackPaths(1) // boot path "a"
	m, _, _ := newProbingMultipath(t, paths, pskA, clk)

	betaSched, betaProbers, betaFactory := concPeerWiring(t, paths, pskB, 0x0FACE, clk)
	if err := m.AddConcentratorPeer("beta", pskB, betaSched, betaProbers, betaFactory); err != nil {
		t.Fatalf("AddConcentratorPeer: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	primary := m.peerState
	beta := m.peersByName["beta"]

	// --- Runtime AddPath while 2 peers are bound: the fan-out reaches BOTH peers. ---
	if err := m.AddPath(config.Path{Name: "runtime-b", SourceAddr: netip.MustParseAddr("127.0.0.1")}); err != nil {
		t.Fatalf("AddPath: %v", err)
	}
	pbPrimary := peerPathByName(primary, "runtime-b")
	pbBeta := peerPathByName(beta, "runtime-b")
	if pbPrimary == nil || pbBeta == nil {
		t.Fatalf("runtime path missing after add: primary=%v beta=%v", pathNamesOfPeer(primary), pathNamesOfPeer(beta))
	}
	if pbPrimary.prober == nil || pbBeta.prober == nil {
		t.Fatal("a bound peer gained no per-(peer,path) prober for the runtime path")
	}
	if pbPrimary.prober == pbBeta.prober {
		t.Fatal("both peers share ONE runtime prober — the fan-out did not mint per-peer state")
	}
	// The runtime probers agree on the shared DATA-frame path-id (DATA and PROBE must agree on
	// the wire), yet each is keyed on its OWN peer's psk (mutation-sensitive).
	if pbPrimary.id != pbBeta.id {
		t.Fatalf("the two peers' runtime path-ids diverge: primary=%d beta=%d", pbPrimary.id, pbBeta.id)
	}
	assertProberKeyedOn(t, "primary runtime prober", pbPrimary.prober, pskA, pskB)
	assertProberKeyedOn(t, "beta runtime prober", pbBeta.prober, pskB, pskA)

	// --- Runtime RemovePath: the teardown reaches BOTH peers, leaving path 'a' untouched. ---
	if err := m.RemovePath("runtime-b"); err != nil {
		t.Fatalf("RemovePath: %v", err)
	}
	for _, tc := range []struct {
		who string
		p   *peerState
	}{{"primary", primary}, {"beta", beta}} {
		if peerPathByName(tc.p, "runtime-b") != nil {
			t.Fatalf("%s peer still holds the runtime path after remove: %v", tc.who, pathNamesOfPeer(tc.p))
		}
		if len(tc.p.paths) != 1 || peerPathByName(tc.p, "a") == nil {
			t.Fatalf("%s peer lost its surviving path 'a' on the remove: %v", tc.who, pathNamesOfPeer(tc.p))
		}
	}
}

// TestConcentratorPeerRegistrationRefusedAfterOpen pins the ordering invariant: a concentrator
// peer MUST be registered before Open (the per-(peer,path) views are rebuilt by Open from the
// registered set, so a late registration would leave the peer view-less).
func TestConcentratorPeerRegistrationRefusedAfterOpen(t *testing.T) {
	pskA := testKey(t, 0x55)
	pskB := testKey(t, 0x66)
	clk := newFakeClock()
	paths := loopbackPaths(1)
	m, _, _ := newProbingMultipath(t, paths, pskA, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	betaSched, betaProbers, betaFactory := concPeerWiring(t, paths, pskB, 0x0BAD, clk)
	if err := m.AddConcentratorPeer("beta", pskB, betaSched, betaProbers, betaFactory); err == nil {
		t.Fatal("AddConcentratorPeer after Open succeeded, want refusal")
	}
}

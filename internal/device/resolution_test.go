package device

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/telemetry"
)

// scriptedResolver is a hand-driven Resolver whose answer for a host can be changed between
// lookups (mutated under its lock), and which counts every Lookup call — enough to script a
// re-resolution's success/failure/empty timeline and to observe that an out-of-band lookup fired.
type scriptedResolver struct {
	mu     sync.Mutex
	answer map[string][]netip.Addr
	ttl    time.Duration
	ttlOk  bool
	err    error // when non-nil, every Lookup returns it (simulates timeout/NXDOMAIN/transport error)
	calls  int
}

func newScriptedResolver() *scriptedResolver {
	return &scriptedResolver{answer: map[string][]netip.Addr{}}
}

func (s *scriptedResolver) Lookup(ctx context.Context, host string) ([]netip.Addr, time.Duration, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if err := ctx.Err(); err != nil {
		return nil, 0, false, err
	}
	if s.err != nil {
		return nil, 0, false, s.err
	}
	return s.answer[host], s.ttl, s.ttlOk, nil
}

func (s *scriptedResolver) set(host string, addrs ...netip.Addr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answer[host] = addrs
	s.err = nil
}

func (s *scriptedResolver) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *scriptedResolver) empty(host string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answer[host] = nil
	s.err = nil
}

func (s *scriptedResolver) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("parse addr %q: %v", s, err)
	}
	return a
}

// resolutionHarness wires a re-resolution controller over a real hubFailover built on fake
// health/remote/clock, so a repoint is observable through the recording remote exactly as
// production drives it through updateResolution.
type resolutionHarness struct {
	res  *resolution
	fo   *hubFailover
	rem  *recordingRemote
	rslv *scriptedResolver
	clk  *fakeClock
	hp   []*fakeHealth
}

const (
	testPollInterval = 30 * time.Second
	testDNSTimeout   = 5 * time.Second
)

// newResolutionHarness builds the controller for a single active hostname spec (spec 0) plus a
// literal standby (spec 1). The active hostname is pre-seeded with initialActive so activeSpec is
// already set (no boot adoption noise) unless initialActive is the zero value.
func newResolutionHarness(t *testing.T, host string, port uint16, initialActive netip.AddrPort, health ...telemetry.PathState) *resolutionHarness {
	t.Helper()
	var initAddrs []netip.AddrPort
	if initialActive != (netip.AddrPort{}) {
		initAddrs = []netip.AddrPort{initialActive}
	}
	specs := []failoverSpec{
		{spec: config.EndpointSpec{Host: host, Port: port, IsName: true}, addrs: initAddrs},
		litSpec(t, "198.51.100.7:51820"),
	}
	hp := make([]*fakeHealth, len(health))
	hh := make([]hubHealth, len(health))
	for i, st := range health {
		hp[i] = &fakeHealth{st}
		hh[i] = hp[i]
	}
	rem := &recordingRemote{}
	clk := &fakeClock{now: time.Unix(1000, 0)}
	fo := newHubFailoverFromSpecs(specs, hh, rem, func() {}, clk, testSettle, discardLogger(t))
	rslv := newScriptedResolver()
	res := newResolution(rslv, fo, nameTargetsFromSpecs([]config.EndpointSpec{
		{Host: host, Port: port, IsName: true},
		{Addr: mustAP(t, "198.51.100.7:51820")},
	}), pathFamilies{v4: true, v6: true}, clk, testPollInterval, testDNSTimeout, discardLogger(t))
	return &resolutionHarness{res: res, fo: fo, rem: rem, rslv: rslv, clk: clk, hp: hp}
}

// advancePastPoll advances the fake clock beyond the next scheduled poll so the following step
// performs a full poll.
func (h *resolutionHarness) advancePastPoll() {
	h.clk.advance(testPollInterval + time.Second)
}

// TestResolutionLookupFailureRetainsAndRetries is acceptance (1) + the D46 error case: a lookup
// that FAILS at every tick leaves the endpoint set untouched (no SetPeerRemote), never hard-fails,
// and keeps retrying — the resolver is called once per poll.
func TestResolutionLookupFailureRetainsAndRetries(t *testing.T) {
	host := "hub.example.com"
	active := mustAP(t, "203.0.113.1:51820")
	h := newResolutionHarness(t, host, 51820, active, telemetry.StateUp)
	h.rslv.fail(context.DeadlineExceeded)

	for i := 0; i < 8; i++ {
		h.advancePastPoll()
		h.res.step()
	}

	if h.rem.calls != 0 {
		t.Fatalf("lookup failure repointed the bond %d times, want 0 (retention: keep last-good)", h.rem.calls)
	}
	if h.fo.activeSpec != 0 || h.fo.activeAddr != active {
		t.Fatalf("active endpoint moved to (%d,%v) under lookup failure, want (0,%v) untouched", h.fo.activeSpec, h.fo.activeAddr, active)
	}
	if got := len(h.fo.specs[0].addrs); got != 1 || h.fo.specs[0].addrs[0] != active {
		t.Fatalf("active spec expansion = %v under lookup failure, want [%v] retained", h.fo.specs[0].addrs, active)
	}
	// Retries kept happening: one lookup per poll tick (8 ticks).
	if got := h.rslv.callCount(); got < 8 {
		t.Fatalf("resolver called %d times over 8 poll ticks, want >= 8 (kept retrying)", got)
	}
}

// TestResolutionChangedActiveIPRepointsOnce is acceptance (2): a changed active IP produces
// EXACTLY one SetPeerRemote — the repoint — even across many subsequent ticks returning the same
// new IP (D32 suppression inside updateResolution).
func TestResolutionChangedActiveIPRepointsOnce(t *testing.T) {
	host := "hub.example.com"
	oldA := mustAP(t, "203.0.113.1:51820")
	newAddr := mustAddr(t, "203.0.113.2")
	newAP := netip.AddrPortFrom(newAddr, 51820)
	h := newResolutionHarness(t, host, 51820, oldA, telemetry.StateUp)
	h.rslv.set(host, newAddr)

	for i := 0; i < 6; i++ {
		h.advancePastPoll()
		h.res.step()
	}

	if h.rem.calls != 1 {
		t.Fatalf("changed active IP produced %d SetPeerRemote, want exactly 1", h.rem.calls)
	}
	if h.rem.last != newAP {
		t.Fatalf("repointed remote = %v, want new active record %v", h.rem.last, newAP)
	}
	if h.fo.activeAddr != newAP {
		t.Fatalf("active addr = %v, want %v", h.fo.activeAddr, newAP)
	}
}

// TestResolutionUnchangedIPNoRepoint is acceptance (3): an unchanged IP produces NO SetPeerRemote
// over many ticks.
func TestResolutionUnchangedIPNoRepoint(t *testing.T) {
	host := "hub.example.com"
	active := mustAP(t, "203.0.113.1:51820")
	h := newResolutionHarness(t, host, 51820, active, telemetry.StateUp)
	h.rslv.set(host, active.Addr())

	for i := 0; i < 20; i++ {
		h.advancePastPoll()
		h.res.step()
	}

	if h.rem.calls != 0 {
		t.Fatalf("unchanged IP produced %d SetPeerRemote over 20 ticks, want 0", h.rem.calls)
	}
	if h.fo.activeSpec != 0 || h.fo.activeAddr != active {
		t.Fatalf("active endpoint drifted to (%d,%v), want (0,%v)", h.fo.activeSpec, h.fo.activeAddr, active)
	}
}

// TestResolutionAllDownTriggersOutOfBandReResolve is acceptance (4): when every path to the
// active endpoint goes DOWN, an out-of-band re-resolve of the active spec fires BEFORE the next
// scheduled poll tick — observable both as an extra resolver Lookup while the poll is NOT yet due
// and, since the answer changed, as an immediate repoint.
func TestResolutionAllDownTriggersOutOfBandReResolve(t *testing.T) {
	host := "hub.example.com"
	oldA := mustAP(t, "203.0.113.1:51820")
	newAddr := mustAddr(t, "203.0.113.2")
	newAP := netip.AddrPortFrom(newAddr, 51820)
	h := newResolutionHarness(t, host, 51820, oldA, telemetry.StateUp, telemetry.StateUp)
	h.rslv.set(host, oldA.Addr())

	// One step with paths UP: records the not-down baseline (edge detection) and consumes the
	// initial scheduled poll (unchanged IP → no repoint).
	h.advancePastPoll()
	h.res.step()
	if h.rem.calls != 0 {
		t.Fatalf("baseline step repointed %d times, want 0", h.rem.calls)
	}
	callsBefore := h.rslv.callCount()

	// The DNS answer changes AND every path goes DOWN. WITHOUT advancing the clock (poll is not
	// due), a step must still re-resolve the active spec out of band and repoint.
	h.rslv.set(host, newAddr)
	h.hp[0].state = telemetry.StateDown
	h.hp[1].state = telemetry.StateDown
	h.res.step()

	if got := h.rslv.callCount(); got != callsBefore+1 {
		t.Fatalf("out-of-band re-resolve made %d lookups, want exactly 1 before the next poll tick", got-callsBefore)
	}
	if h.rem.calls != 1 || h.rem.last != newAP {
		t.Fatalf("liveness-loss re-resolve repoint = (calls %d, last %v), want (1, %v)", h.rem.calls, h.rem.last, newAP)
	}

	// Edge-triggered: a second step while still all-down (no new edge) does NOT re-resolve again.
	callsAfter := h.rslv.callCount()
	h.res.step()
	if got := h.rslv.callCount(); got != callsAfter {
		t.Fatalf("all-down re-resolve fired again on a level (non-edge) tick: %d extra lookups, want 0", got-callsAfter)
	}
}

// TestResolutionTTLClampShortensNextDelay is acceptance (5): a TTL below the poll interval
// shortens the next resolve delay to that TTL; a TTL at or above it (or an absent TTL) leaves the
// poll interval unchanged.
func TestResolutionTTLClampShortensNextDelay(t *testing.T) {
	host := "hub.example.com"
	active := mustAP(t, "203.0.113.1:51820")
	h := newResolutionHarness(t, host, 51820, active, telemetry.StateUp)
	h.rslv.set(host, active.Addr())

	// TTL (5s) below the poll interval (30s): next delay clamps to the TTL.
	h.rslv.ttl = 5 * time.Second
	h.rslv.ttlOk = true
	if got := h.res.pollAll(); got != 5*time.Second {
		t.Fatalf("pollAll next delay = %v with a 5s TTL, want 5s (clamped to TTL)", got)
	}

	// TTL absent (system resolver, ttlOk=false): next delay is the full poll interval.
	h.rslv.ttlOk = false
	if got := h.res.pollAll(); got != testPollInterval {
		t.Fatalf("pollAll next delay = %v with no TTL, want %v (poll interval)", got, testPollInterval)
	}

	// TTL (60s) above the poll interval: not shortened below the interval.
	h.rslv.ttl = 60 * time.Second
	h.rslv.ttlOk = true
	if got := h.res.pollAll(); got != testPollInterval {
		t.Fatalf("pollAll next delay = %v with a 60s TTL, want %v (interval, not lengthened)", got, testPollInterval)
	}
}

// TestResolutionEmptyResultRetainsLastGood is the D46 acceptance: a lookup returning an EMPTY /
// NXDOMAIN result for a PREVIOUSLY-resolved hostname retains the last-good expansion — the
// controller never publishes an empty set, so hubFailover never sees the active spec collapse to
// empty (the condition that could strand the bond under its total<2 guard).
func TestResolutionEmptyResultRetainsLastGood(t *testing.T) {
	host := "hub.example.com"
	active := mustAP(t, "203.0.113.1:51820")
	h := newResolutionHarness(t, host, 51820, active, telemetry.StateUp)

	// First: a good answer keeps the previously-resolved record in place (unchanged → no repoint).
	h.rslv.set(host, active.Addr())
	h.advancePastPoll()
	h.res.step()
	if got := len(h.fo.specs[0].addrs); got != 1 || h.fo.specs[0].addrs[0] != active {
		t.Fatalf("after good lookup, active spec = %v, want [%v]", h.fo.specs[0].addrs, active)
	}

	// Now the resolver goes NODATA/NXDOMAIN (empty). The last-good record MUST be retained.
	h.rslv.empty(host)
	for i := 0; i < 5; i++ {
		h.advancePastPoll()
		h.res.step()
	}

	if got := len(h.fo.specs[0].addrs); got != 1 || h.fo.specs[0].addrs[0] != active {
		t.Fatalf("empty/NXDOMAIN result collapsed the active spec to %v, want [%v] retained (D46)", h.fo.specs[0].addrs, active)
	}
	if h.rem.calls != 0 {
		t.Fatalf("empty result touched the bond %d times, want 0 (never publish empty)", h.rem.calls)
	}
	if h.fo.activeSpec != 0 || h.fo.activeAddr != active {
		t.Fatalf("active identity = (%d,%v) after empty result, want (0,%v)", h.fo.activeSpec, h.fo.activeAddr, active)
	}
}

// TestResolutionSingleHostnameBootAdopts proves the controller runs even for a SINGLE-hostname
// peer (independent of hub-failover's >=2 guard): the sole spec boots EMPTY (activeSpec == -1),
// and the first successful poll publishes the resolved head, which updateResolution adopts as the
// active endpoint via exactly one SetPeerRemote.
func TestResolutionSingleHostnameBootAdopts(t *testing.T) {
	host := "hub.example.com"
	first := mustAddr(t, "203.0.113.1")
	firstAP := netip.AddrPortFrom(first, 51820)
	specs := []failoverSpec{
		{spec: config.EndpointSpec{Host: host, Port: 51820, IsName: true}}, // sole spec, EMPTY at boot
	}
	hp := []hubHealth{&fakeHealth{telemetry.StateUp}}
	rem := &recordingRemote{}
	clk := &fakeClock{now: time.Unix(1000, 0)}
	fo := newHubFailoverFromSpecs(specs, hp, rem, func() {}, clk, testSettle, discardLogger(t))
	if fo.activeSpec != -1 {
		t.Fatalf("single hostname boot activeSpec = %d, want -1 (empty)", fo.activeSpec)
	}
	rslv := newScriptedResolver()
	rslv.set(host, first)
	res := newResolution(rslv, fo, nameTargetsFromSpecs([]config.EndpointSpec{
		{Host: host, Port: 51820, IsName: true},
	}), pathFamilies{v4: true, v6: true}, clk, testPollInterval, testDNSTimeout, discardLogger(t))

	// First step polls immediately (nextPollAt armed at construction time) and adopts.
	res.step()

	if rem.calls != 1 || rem.last != firstAP {
		t.Fatalf("single-hostname boot adoption SetPeerRemote = (calls %d, last %v), want (1, %v)", rem.calls, rem.last, firstAP)
	}
	if fo.activeSpec != 0 || fo.activeAddr != firstAP {
		t.Fatalf("post-adoption active = (%d,%v), want (0,%v)", fo.activeSpec, fo.activeAddr, firstAP)
	}
}

// TestResolutionFamilyFilterRetainsLastGood is the family-filter acceptance: an AAAA-only answer
// on a v4-only edge (no local path can source v6) filters down to an EMPTY usable set, so the spec
// keeps its last-good v4 expansion and NO SetPeerRemote fires — a successful lookup of an
// unreachable family must never tear down a working v4 endpoint.
func TestResolutionFamilyFilterRetainsLastGood(t *testing.T) {
	host := "hub.example.com"
	active := mustAP(t, "203.0.113.1:51820")
	h := newResolutionHarness(t, host, 51820, active, telemetry.StateUp)
	h.res.families = pathFamilies{v4: true} // v4-only paths: v6 answers are unreachable

	// The resolver now answers with AAAA records only (a v6-only rrset for the active spec).
	h.rslv.set(host, mustAddr(t, "2001:db8::1"), mustAddr(t, "2001:db8::2"))
	for i := 0; i < 5; i++ {
		h.advancePastPoll()
		h.res.step()
	}

	if h.rem.calls != 0 {
		t.Fatalf("AAAA-only answer on v4-only paths repointed the bond %d times, want 0 (family filter → retain)", h.rem.calls)
	}
	if got := len(h.fo.specs[0].addrs); got != 1 || h.fo.specs[0].addrs[0] != active {
		t.Fatalf("active spec expansion = %v under an all-filtered answer, want [%v] retained", h.fo.specs[0].addrs, active)
	}
	if h.fo.activeSpec != 0 || h.fo.activeAddr != active {
		t.Fatalf("active identity = (%d,%v), want (0,%v) untouched", h.fo.activeSpec, h.fo.activeAddr, active)
	}
}

// TestResolutionLivenessLossReArmClampsToTTL extends the TTL-clamp invariant to the OUT-OF-BAND
// (liveness-loss) re-resolve path: when every path to the active concentrator goes DOWN, the
// re-arm of the next scheduled poll must be clamped to min(pollInterval, TTL) — not reset to a
// blind full interval — so a short-TTL record is re-checked no later than its TTL in exactly the
// hub-loss window where record freshness matters most. A lookup with no TTL leaves the full interval.
func TestResolutionLivenessLossReArmClampsToTTL(t *testing.T) {
	host := "hub.example.com"
	active := mustAP(t, "203.0.113.1:51820")
	h := newResolutionHarness(t, host, 51820, active, telemetry.StateUp, telemetry.StateUp)
	h.rslv.set(host, active.Addr())

	// Baseline UP step: record the not-down edge state and consume the initial scheduled poll.
	h.advancePastPoll()
	h.res.step()

	// A short-TTL record AND every path goes DOWN → out-of-band re-resolve re-arms at the TTL.
	h.rslv.ttl = 5 * time.Second
	h.rslv.ttlOk = true
	h.hp[0].state = telemetry.StateDown
	h.hp[1].state = telemetry.StateDown
	now := h.clk.Now()
	h.res.step()
	if got, want := h.res.nextPollAt, now.Add(5*time.Second); !got.Equal(want) {
		t.Fatalf("liveness-loss re-arm nextPollAt = %v, want %v (clamped to 5s TTL)", got, want)
	}

	// Clear the edge (paths back UP, no re-resolve), then lose them again with NO TTL exposed:
	// the out-of-band re-arm must fall back to the full poll interval.
	h.hp[0].state = telemetry.StateUp
	h.hp[1].state = telemetry.StateUp
	h.res.step()
	h.rslv.ttlOk = false
	h.hp[0].state = telemetry.StateDown
	h.hp[1].state = telemetry.StateDown
	now = h.clk.Now()
	h.res.step()
	if got, want := h.res.nextPollAt, now.Add(testPollInterval); !got.Equal(want) {
		t.Fatalf("liveness-loss re-arm nextPollAt = %v with no TTL, want %v (full poll interval)", got, want)
	}
}

// TestOrderAddrPortsDeterministicFamilyOrder pins the ordering contract orderAddrPorts must hold
// for updateResolution's suppression to work: IPv4 first (resolver order), then IPv6, deduped,
// 4in6 canonicalized — byte-identical across identical answers.
func TestOrderAddrPortsDeterministicFamilyOrder(t *testing.T) {
	in := []netip.Addr{
		mustAddr(t, "2001:db8::2"),
		mustAddr(t, "203.0.113.1"),
		mustAddr(t, "2001:db8::1"),
		mustAddr(t, "203.0.113.1"), // duplicate v4 → dropped
		mustAddr(t, "203.0.113.2"),
	}
	got := orderAddrPorts(in, 51820, pathFamilies{v4: true, v6: true})
	want := []netip.AddrPort{
		netip.AddrPortFrom(mustAddr(t, "203.0.113.1"), 51820),
		netip.AddrPortFrom(mustAddr(t, "203.0.113.2"), 51820),
		netip.AddrPortFrom(mustAddr(t, "2001:db8::2"), 51820),
		netip.AddrPortFrom(mustAddr(t, "2001:db8::1"), 51820),
	}
	if len(got) != len(want) {
		t.Fatalf("orderAddrPorts len = %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("orderAddrPorts[%d] = %v, want %v (v4-first, resolver order, deduped)", i, got[i], want[i])
		}
	}
}

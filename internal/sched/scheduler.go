package sched

import (
	"github.com/7mind/wanbond/internal/telemetry"
)

// Negative Pick sentinels. Pick returns a NON-NEGATIVE path index when a datagram
// should egress, and one of these when it should not. They are distinct so the Send
// path can tell a genuine outage (no eligible path) apart from a transient pacer
// shedding a frame while every path is healthy — the two must not read the same in
// operator logs or the e2e log-grep harness.
const (
	// PickNone means no path is currently eligible (a total outage among this
	// scheduler's paths). The Bind maps it to "no healthy path".
	PickNone = -1
	// PickPaced means eligible paths EXIST but every one is momentarily paced out, so
	// this frame is shed (dropped) to bound egress and the send backlog. The paths are
	// healthy — this is deliberate rate limiting, NOT an outage. Any pacing-enabled
	// scheduler returns it: the weighted scheduler when every eligible path's bucket is
	// empty, and active-backup (defect D65) when the single active path's own bucket is
	// empty. A scheduler with pacing off never returns it.
	PickPaced = -2
)

// FrameClass tags an outbound datagram with the traffic class the pacer must treat
// it as (defect D22). The multipath Bind classifies each frame from the inner
// WireGuard message type and passes the class to Pick so a pacing scheduler can give
// WireGuard control frames (handshake init/response, cookie reply, keepalive) a
// different admission policy than bulk data. Pick() has no other frame-type
// visibility, so this is the ONLY channel by which frame-type reaches the pacer.
//
// FrameClass covers only frames that traverse Pick. wanbond's OWN PROBE frames
// (frame.KindProbe) do NOT reach Pick at all — emitProbes and dispatchInbound write
// them straight to the path socket, bypassing Send->Pick->token-bucket — so they have
// no FrameClass; the pacer accounts for them through the separate ProbeBudget seam
// (T145). Together these define the THREE-TIER pacing-priority model the pacer honours:
//
//	ClassControl:     pacing-EXEMPT and UNCHARGED (defect D22) — WireGuard rekey is
//	                  never shed AND spends no token.
//	frame.KindProbe:  pacing-EXEMPT but CHARGED (T145) — the probe is never shed or
//	                  delayed (strict priority), but ProbeBudget.AccountProbe deducts a
//	                  token so paced ClassData yields the headroom the probe stream
//	                  (plus reflected echoes) consumes.
//	ClassData:        FULLY paced — subject to the per-path token bucket, shed
//	                  (PickPaced) when it is empty.
type FrameClass uint8

const (
	// ClassData is bulk/opaque data (a WireGuard transport datagram carrying tunnelled
	// payload). It is fully paced: under any pacing-enabled scheduler (the weighted
	// scheduler, and active-backup per defect D65) it is subject to the per-path token
	// buckets and is shed (PickPaced) when they are empty.
	ClassData FrameClass = iota
	// ClassControl is a WireGuard control frame — handshake initiation/response, cookie
	// reply, or keepalive. It is pacing-EXEMPT: a pacing scheduler never sheds it for an
	// empty token bucket and never spends a data token for it, so sustained bulk overload
	// cannot starve rekey (defect D22). Control frames originate from OUR OWN WireGuard
	// engine on the send path (an attacker cannot inject them here), and WireGuard's own
	// state machine bounds their rate, so exempting them from the data pace is safe.
	ClassControl
)

// Scheduler is the send-side path-selection policy the multipath Bind consults
// for every outbound datagram. It is the extension seam for the send scheduler:
// active-backup failover (ActiveBackup, the P1 MVP) is one implementation; the
// later weighted / FEC-aware policy (T21) is a DIFFERENT implementation of this
// same interface, swapped in at the composition root with no Bind change.
type Scheduler interface {
	// Pick returns the index of the path the next datagram must egress on, in
	// the path priority order the scheduler was built over (index 0 = preferred
	// primary). It returns a negative value when no datagram should be sent this
	// call; the negative value is one of the Pick* sentinels (PickNone for no
	// eligible path, PickPaced for a healthy-but-paced-out shed), so the caller can
	// tell a genuine outage apart from deliberate rate limiting. Pick is safe for
	// concurrent callers on the Bind's send path.
	//
	// class is the frame's traffic class (defect D22): a pacing scheduler exempts
	// ClassControl (WireGuard handshake/keepalive) from the data token buckets so
	// bulk overload cannot shed control frames and starve rekey, while ClassData is
	// fully paced. BOTH the weighted scheduler and active-backup (defect D65) honour
	// this exemption when pacing is enabled; a scheduler with pacing off ignores class.
	//
	// Pick MAY be stateful: a weighted/aggregating scheduler advances its
	// distribution bookkeeping (deficit/round-robin credits, pacing tokens, offered-
	// load meter) on every call, so ONE Pick consumes ONE frame-selection slot.
	// Callers that only want the liveness-derived active set refreshed — without
	// perturbing distribution — MUST call Recompute, not Pick (see the T40 eager-
	// failover nudge in the multipath Bind).
	Pick(class FrameClass) int

	// Recompute refreshes the scheduler's liveness-derived selection state (which
	// paths are eligible, which is the active primary) from the current PathHealth,
	// and logs any active-path transition, WITHOUT selecting or consuming a frame
	// slot: it advances no distribution/pacing/load state. It is the split-out
	// "recompute the eligible/active set from liveness" half of the old Pick, so the
	// eager-failover nudge (defect D18/T40) can drive an egress-lull failover recompute
	// without stealing a weighted-distribution slot the way a spurious Pick would. For
	// an idempotent scheduler (active-backup) it is exactly Pick without the return;
	// for a stateful one (weighted) it is strictly the non-consuming subset. It is
	// safe for concurrent callers and never calls back into the Bind.
	Recompute()
}

// ProbeBudget is the OPTIONAL pacing-headroom seam a pacing scheduler implements so the
// bind can CHARGE the per-path token bucket for frames that egress OUTSIDE Pick — the
// MIDDLE tier of the three-tier pacing-priority model documented on FrameClass. wanbond's
// own PROBE frames (frame.KindProbe) and their reflected echoes do not traverse
// Send->Pick->token-bucket (emitProbes and dispatchInbound write them straight to the
// path socket), so the token bucket would otherwise budget ZERO headroom for them: a pace
// sized at ~link rate then lets paced ClassData plus the probe stream jointly oversubscribe
// the link, building the standing queue that delays probes past DownAfter into a spurious
// path-DOWN / failover flap (T145). AccountProbe closes that gap by deducting a token per
// emitted probe / reflected echo WITHOUT ever shedding or delaying the probe.
//
// The bind type-asserts its scheduler to ProbeBudget and charges per emitted probe and per
// reflected echo (symmetric); a scheduler with no pacing headroom (or pacing off) either
// does not implement it or no-ops, so the seam is inert when unused. *WeightedScheduler
// implements it.
type ProbeBudget interface {
	// AccountProbe deducts one pacing token from path pathIdx's bucket WITHOUT ever
	// shedding or delaying the frame (strict priority): the bucket MAY go negative so a
	// subsequent ClassData Pick yields until refill catches up. pathIdx is the same
	// priority-ordered index Pick returns and AddPath/RemovePath renumber; an out-of-range
	// index is a no-op (best-effort headroom). It is safe for concurrent callers.
	AccountProbe(pathIdx int)
}

// PathQuality is the per-path measured-quality source the weighted scheduler reads
// to derive distribution weights: the read side of the T13 telemetry Estimate
// (RTT/Jitter/Loss), ANALOGOUS to PathHealth but exposing the quality snapshot
// rather than the up/down verdict. *telemetry.Prober satisfies BOTH PathHealth and
// PathQuality (its Estimate() is mutex-guarded, so it meets the same concurrency
// contract PathHealth documents), which is how one *Prober per path feeds liveness
// AND weight to the scheduler. The weighted scheduler's unit tests drive a synthetic
// PathQuality so the RTT/loss → weight formula is exercised on hand-built Estimates
// with no real probe stream.
type PathQuality interface {
	Estimate() telemetry.Estimate
}

// DynamicScheduler is a Scheduler whose path set can change at runtime (T30): a
// path may be admitted or dropped WITHOUT rebuilding the scheduler or disturbing
// the active selection of the surviving paths. The multipath Bind holds its
// scheduler as a plain Scheduler and type-asserts to this richer interface only
// when a runtime path add/remove is requested, so the Scheduler seam (and the
// T21 weighted policy that will implement it) is preserved: a scheduler that
// does not support dynamic membership simply cannot back a reloadable bind.
//
// The index space is the SAME priority-ordered index Pick returns, and the Bind
// keeps its own path slice index-aligned with this scheduler's path list, so an
// index returned by Pick always addresses the matching path. Both methods are
// safe for concurrent callers.
type DynamicScheduler interface {
	Scheduler
	// AddPath admits h as a NEW lowest-priority path (highest index) and returns
	// that index. Appending at the tail cannot change any existing path's index
	// nor steal the active selection from a higher-priority survivor: an
	// active-backup policy only ever selects the new path once every
	// higher-priority path is down. h must be non-nil.
	AddPath(h PathHealth) (int, error)
	// RemovePath drops the path at index i (shifting higher indices down by one)
	// and repairs the cached selection so a surviving path keeps being selected
	// and, if the dropped path was the active one, egress fails over to the best
	// remaining path on the next Pick. i must be in range.
	RemovePath(i int) error
	// SetPaths replaces the ENTIRE path/health list with health (priority order,
	// index 0 = preferred primary) and clears any cached selection, so the next
	// Pick re-derives the active path from live liveness. The Bind calls it on
	// every Open to re-align the scheduler's membership with the path slice it
	// rebuilds from the current definitions — the single reconciliation point that
	// makes a runtime path add/remove survive a Close→Open cycle index-aligned
	// (T30). It is safe for concurrent callers; health must be non-empty and hold
	// no nil element.
	SetPaths(health []PathHealth) error
}

// PathHealth is the per-path liveness the scheduler consumes: the read side of
// the T13 telemetry liveness state machine, so the scheduler reads exactly the
// up/down verdict T13 already computes (with its own probe-loss hysteresis)
// rather than duplicating detection here.
//
// Concurrency contract: Pick runs on the Bind send path CONCURRENTLY with the
// probe/timer goroutines that mutate liveness, so a production PathHealth MUST
// be INTERNALLY SYNCHRONIZED. *telemetry.Prober satisfies this — its State()
// takes the Prober's mutex (probe.go). A bare *telemetry.Liveness does NOT: it
// is not independently synchronized (probe.go: "must be reached only through
// the Prober") and its State() is an unguarded field read (liveness.go), so
// wiring a live, probe-driven *Liveness directly here is a data race — use a
// *Prober or an explicitly-locked adapter instead. (The sched unit tests drive
// a raw *Liveness on a single goroutine with no concurrent writer, which is not
// a race and is fine for testing.)
type PathHealth interface {
	State() telemetry.PathState
}

// AlwaysUp is a PathHealth that reports StateUp unconditionally. It is the
// composition-root placeholder used until the probe-transport wiring (a
// follow-up task) drives real per-path *telemetry.Prober liveness: with every
// path statically Up, the active-backup scheduler keeps all egress on the
// preferred primary (the data-thrift bring-up behaviour), and swapping in real
// Probers later activates failover with no further Bind or scheduler change.
type AlwaysUp struct{}

// State reports the path as up.
func (AlwaysUp) State() telemetry.PathState { return telemetry.StateUp }

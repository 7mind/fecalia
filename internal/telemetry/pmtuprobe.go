package telemetry

import (
	"errors"
	"sync"
	"time"
)

// ErrProbeTooLarge is the sentinel the bind-layer send func (T227) returns when a
// padded PMTU probe's outer datagram exceeds the path MTU and the kernel refuses it
// under the Don't-Fragment policy (T201, EMSGSIZE). EchoAwaitProbe maps it to a
// benign echoed=false (the binary search's "too large" signal), NOT a transport
// error — and keeping it a sentinel keeps the telemetry plane free of any syscall
// dependency, since the EMSGSIZE detection lives in bind where the socket write is.
var ErrProbeTooLarge = errors.New("telemetry: padded probe exceeds path MTU (EMSGSIZE)")

// DefaultPMTUProbeDeadline bounds how long EchoAwaitProbe waits for one padded
// probe's echo before concluding the size did not traverse the path. The PMTU binary
// search issues ~log2(ceiling-floor) probes, up to about half of which (the oversize
// ones) wait the full deadline, so this multiplies into total search time and must
// stay well under the e2e convergence window (TestE2EPMTUDiscovery allows 20s). It is
// comfortably larger than a realistic path RTT so a slow-but-live echo is not mistaken
// for a drop.
const DefaultPMTUProbeDeadline = 1 * time.Second

// EchoAwaitProbe is the production telemetry.PMTUProbe (T226, defect D88): it turns a
// path's Prober.SendPaddedProbe plus a bind-injected outer-socket send into the
// ProbePMTU(onWire) (echoed, err) contract the PMTUDiscovery binary search drives. It
// sends a padded probe of the requested on-wire size and blocks until either the
// matching echo is delivered via NotifyEcho (echoed=true) or the deadline elapses
// (echoed=false). NotifyEcho matches on the probe's ProbeSeq DIRECTLY, decoupled from
// the Prober's anti-replay guard, so a slow padded echo still completes even after a
// later, faster liveness echo advanced the guard high-water (R245).
//
// Concurrency: ProbePMTU is called from a single per-path discovery goroutine (T228);
// NotifyEcho is called from the per-path receive goroutine (T227). The pending-waiter
// map is guarded by mu, held ONLY for the map lookup/registration — never across the
// blocking wait or the socket send — so a blocked probe never stalls a concurrent
// NotifyEcho or Send.
type EchoAwaitProbe struct {
	prober   *Prober
	admit    func(int, func() ([]byte, error)) error
	dispatch func(func() error) error
	// outerIPUDPOverhead is selected from the validated path socket's address
	// family by bind. ProbePMTU candidates remain outer IP MTU units; subtracting
	// this exact family cost yields the UDP payload passed to SendPaddedProbe.
	outerIPUDPOverhead int
	deadline           time.Duration
	// after returns a channel that fires after d; time.After in production, injectable
	// so unit tests drive the deadline deterministically with no real sleep.
	after func(d time.Duration) <-chan time.Time

	mu      sync.Mutex
	pending map[uint64]chan struct{}
}

// compile-time proof EchoAwaitProbe satisfies the discovery machine's probe seam.
var _ PMTUProbe = (*EchoAwaitProbe)(nil)

// NewEchoAwaitProbe builds the probe backend for one path. send transmits an encoded
// probe on that path's outer DF socket and returns ErrProbeTooLarge on a local
// EMSGSIZE. outerIPUDPOverhead is the required family-specific IP+UDP cost selected
// by the transport; a non-positive deadline uses DefaultPMTUProbeDeadline; a nil
// after uses time.After (production).
func NewEchoAwaitProbe(
	prober *Prober,
	send func([]byte) error,
	outerIPUDPOverhead int,
	deadline time.Duration,
	after func(time.Duration) <-chan time.Time,
) *EchoAwaitProbe {
	return newEchoAwaitProbe(prober, directProbeAdmission(send), func(work func() error) error {
		return work()
	}, outerIPUDPOverhead, deadline, after)
}

// NewCadencedEchoAwaitProbe builds a probe backend whose dispatch function chooses
// when one complete send attempt begins. dispatch must execute work synchronously
// before returning. This lets a transport queue a PMTU request until an existing
// cadence slot without generating its sequence or timestamp before that wait.
func NewCadencedEchoAwaitProbe(
	prober *Prober,
	send func([]byte) error,
	dispatch func(func() error) error,
	outerIPUDPOverhead int,
	deadline time.Duration,
	after func(time.Duration) <-chan time.Time,
) *EchoAwaitProbe {
	return newEchoAwaitProbe(prober, directProbeAdmission(send), dispatch, outerIPUDPOverhead, deadline, after)
}

// NewCadencedAdmittedEchoAwaitProbe additionally lets the transport reserve
// retained priority capacity before sequence and timestamp generation.
func NewCadencedAdmittedEchoAwaitProbe(
	prober *Prober,
	admit func(int, func() ([]byte, error)) error,
	dispatch func(func() error) error,
	outerIPUDPOverhead int,
	deadline time.Duration,
	after func(time.Duration) <-chan time.Time,
) *EchoAwaitProbe {
	return newEchoAwaitProbe(prober, admit, dispatch, outerIPUDPOverhead, deadline, after)
}

func newEchoAwaitProbe(
	prober *Prober,
	admit func(int, func() ([]byte, error)) error,
	dispatch func(func() error) error,
	outerIPUDPOverhead int,
	deadline time.Duration,
	after func(time.Duration) <-chan time.Time,
) *EchoAwaitProbe {
	if outerIPUDPOverhead <= 0 {
		panic("telemetry: outer IP/UDP overhead must be positive")
	}
	if deadline <= 0 {
		deadline = DefaultPMTUProbeDeadline
	}
	if after == nil {
		after = time.After
	}
	return &EchoAwaitProbe{
		prober:             prober,
		admit:              admit,
		dispatch:           dispatch,
		outerIPUDPOverhead: outerIPUDPOverhead,
		deadline:           deadline,
		after:              after,
		pending:            make(map[uint64]chan struct{}),
	}
}

func directProbeAdmission(send func([]byte) error) func(int, func() ([]byte, error)) error {
	return func(_ int, generate func() ([]byte, error)) error {
		raw, err := generate()
		if err != nil {
			return err
		}
		return send(raw)
	}
}

// ProbePMTU sends a padded probe of onWire outer bytes and reports whether a matching
// echo returned within the deadline. Sequence allocation, timestamping, registration,
// and the echo deadline begin inside dispatch, so time waiting for a transport cadence
// slot does not contaminate RTT or consume the echo timeout. It satisfies
// telemetry.PMTUProbe. A returned err leaves the search unconverged (the caller retries
// on a later tick); echoed=false with err=nil is the benign "this size did not
// traverse" the binary search narrows on.
func (e *EchoAwaitProbe) ProbePMTU(pathMTU int) (bool, error) {
	var seq uint64
	var ch chan struct{}
	err := e.dispatch(func() error {
		wireSize := pathMTU - e.outerIPUDPOverhead
		return e.admit(wireSize, func() ([]byte, error) {
			// Generate and register only after transport admission in the selected
			// cadence slot, so neither wait contaminates its timestamp.
			raw, generatedSeq, err := e.prober.SendPaddedProbe(wireSize)
			if err != nil {
				return nil, err
			}
			seq = generatedSeq
			ch = e.register(seq)
			return raw, nil
		})
	})
	if err != nil {
		if ch != nil {
			e.unregister(seq)
		}
		if errors.Is(err, ErrProbeTooLarge) {
			// The kernel refused the oversize datagram under DF: a definitive "too large"
			// verdict (echoed=false). Exclude the seq from loss — a deliberate, expected
			// drop the search must not read as path loss.
			if ch != nil {
				e.prober.ExcludePaddedProbeLoss(seq)
			}
			return false, nil
		}
		// A generation or unexpected transport failure leaves the search unconverged
		// so a later tick retries.
		return false, err
	}
	defer e.unregister(seq)

	select {
	case <-ch:
		return true, nil
	case <-e.after(e.deadline):
		// No echo by the deadline: the size did not round-trip. Exclude the seq from
		// loss (the search expected this drop) and narrow downward.
		e.prober.ExcludePaddedProbeLoss(seq)
		return false, nil
	}
}

// NotifyEcho completes the await for a padded probe whose echo just arrived, matched
// by ProbeSeq. It is DECOUPLED from Prober.HandleEcho's anti-replay verdict (R245):
// the bind receive path calls BOTH — HandleEcho folds liveness/RTT/loss, NotifyEcho
// releases the PMTU waiter — so a padded echo whose seq the guard already rejected as
// stale (a faster liveness echo advanced the high-water) still completes its search
// probe. A seq with no pending waiter (an ordinary liveness echo, or a padded echo
// that already timed out) is a no-op.
func (e *EchoAwaitProbe) NotifyEcho(seq uint64) {
	e.mu.Lock()
	ch, ok := e.pending[seq]
	e.mu.Unlock()
	if !ok {
		return
	}
	// Buffered (cap 1) + non-blocking send: the first echo releases the waiter; a
	// duplicate echo of the same seq hits the default and is discarded (no panic, no
	// block), so a reflected duplicate is harmless.
	select {
	case ch <- struct{}{}:
	default:
	}
}

// register installs a buffered (cap 1) waiter for seq before the probe is sent, so a
// racing NotifyEcho can never be lost between send and the select.
func (e *EchoAwaitProbe) register(seq uint64) chan struct{} {
	ch := make(chan struct{}, 1)
	e.mu.Lock()
	e.pending[seq] = ch
	e.mu.Unlock()
	return ch
}

func (e *EchoAwaitProbe) unregister(seq uint64) {
	e.mu.Lock()
	delete(e.pending, seq)
	e.mu.Unlock()
}

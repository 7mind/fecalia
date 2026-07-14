package device

import (
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/tun"

	"github.com/7mind/wanbond/internal/log"
)

// tunDiagnosticInterval is the minimum spacing between actionable EIO diagnostics for a
// single TUN. An interface left DOWN (or otherwise wedged) fails EVERY subsequent write, so
// without a rate limit a write storm would emit one diagnostic record per dropped packet.
// The raw amneziawg-go engine error ("Failed to write packets to TUN device: input/output
// error", logged unconditionally via engineLogger's Errorf) still surfaces on every failure
// underneath — this decorator adds ONE extra, actionable record per interval on top of that,
// it never suppresses the engine's own logging.
const tunDiagnosticInterval = 30 * time.Second

// diagnosingTUN wraps a tun.Device and, on a Write that fails with EIO, emits ONE rate-limited
// actionable ERROR naming the probable cause (the interface's link state and MTU) and the
// remedy, alongside the raw error for debugging (D39, I3). Left undiagnosed, a write to a DOWN
// wanbond0 surfaces only as the engine's generic "input/output error" — accurate but useless to
// an operator who has never hit the failure mode before. Every OTHER tun.Device method passes
// straight through via the embedded interface; only Write is intercepted.
type diagnosingTUN struct {
	tun.Device
	name string
	log  log.Logger
	// now and probeState are seams for tests: production wires time.Now and ifState (the real
	// SIOCGIFFLAGS/SIOCGIFMTU inspection); a test injects a fake clock and a scripted interface
	// state so it can assert the DOWN/UP wording without a real, privileged network interface.
	now        func() time.Time
	probeState func(name string) (up bool, mtu int, err error)

	mu       sync.Mutex
	lastDiag time.Time
}

// newDiagnosingTUN wraps dev for production use: real wall-clock rate-limiting and the real
// (platform-specific) interface-state inspection.
func newDiagnosingTUN(dev tun.Device, name string, lg log.Logger) *diagnosingTUN {
	return &diagnosingTUN{Device: dev, name: name, log: lg, now: time.Now, probeState: ifState}
}

// Write delegates to the wrapped Device and, on an EIO failure, reports a rate-limited
// actionable diagnostic. The original error is returned UNCHANGED: this decorator only adds a
// diagnostic side effect, it never masks or alters the engine's own error handling.
func (d *diagnosingTUN) Write(bufs [][]byte, offset int) (int, error) {
	n, err := d.Device.Write(bufs, offset)
	if err != nil && errors.Is(err, syscall.EIO) {
		d.diagnoseEIO(err)
	}
	return n, err
}

// diagnoseEIO inspects the interface state and logs ONE actionable ERROR record, then
// suppresses further diagnostics for tunDiagnosticInterval so a write storm (every dropped
// packet failing until the operator acts) produces one record, not a flood.
func (d *diagnosingTUN) diagnoseEIO(writeErr error) {
	d.mu.Lock()
	now := d.now()
	if !d.lastDiag.IsZero() && now.Sub(d.lastDiag) < tunDiagnosticInterval {
		d.mu.Unlock()
		return
	}
	d.lastDiag = now
	d.mu.Unlock()

	var errno syscall.Errno
	_ = errors.As(writeErr, &errno)

	up, mtu, stateErr := d.probeState(d.name)
	switch {
	case stateErr != nil:
		d.log.Error("TUN write failed with I/O error", "interface", d.name, "raw_error", writeErr.Error(),
			"errno", int(errno), "state", "unknown",
			"hint", fmt.Sprintf("could not inspect %s state (%v) — address & bring it up (install.md §4)", d.name, stateErr))
	case !up:
		d.log.Error("TUN write failed with I/O error", "interface", d.name, "raw_error", writeErr.Error(),
			"errno", int(errno), "state", "DOWN", "mtu", mtu,
			"hint", fmt.Sprintf("%s is DOWN — address & bring it up (install.md §4)", d.name))
	default:
		d.log.Error("TUN write failed with I/O error", "interface", d.name, "raw_error", writeErr.Error(),
			"errno", int(errno), "state", "UP", "mtu", mtu,
			"hint", fmt.Sprintf("%s is UP (mtu=%d) but writes are still failing — check the link/driver (install.md §4)", d.name, mtu))
	}
}

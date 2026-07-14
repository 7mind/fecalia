package device

import (
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/tun"

	"github.com/7mind/wanbond/internal/log"
)

// scriptedWriteTUN is a tun.Device whose Write result a test fully controls. Every OTHER
// Device method is unreachable in these tests (diagnosingTUN only intercepts Write and
// otherwise delegates through the embedded interface), so the nil embedded Device is never
// dereferenced.
type scriptedWriteTUN struct {
	tun.Device
	n   int
	err error
}

func (s *scriptedWriteTUN) Write([][]byte, int) (int, error) { return s.n, s.err }

// newTestDiagnosingTUN builds a diagnosingTUN over a scripted Write outcome, the shared
// manually-advanced fakeClock (see metrics_test.go), and a scripted interface-state probe — no
// real network interface or privilege required.
func newTestDiagnosingTUN(writeErr error, clk *fakeClock, lg log.Logger, up bool, mtu int, stateErr error) *diagnosingTUN {
	return &diagnosingTUN{
		Device: &scriptedWriteTUN{err: writeErr},
		name:   "wanbond0",
		log:    lg,
		now:    clk.Now,
		probeState: func(string) (bool, int, error) {
			return up, mtu, stateErr
		},
	}
}

func newCapturingLogger(t *testing.T) (log.Logger, *syncBuffer) {
	t.Helper()
	buf := &syncBuffer{}
	lg, err := log.New("error", buf)
	if err != nil {
		t.Fatalf("log.New: %v", err)
	}
	return lg, buf
}

// TestDiagnosingTUNWriteEIODown is the core acceptance check: a write that fails with EIO
// against a DOWN interface logs a record naming the DOWN state, pointing at install.md §4,
// and carrying the raw errno.
func TestDiagnosingTUNWriteEIODown(t *testing.T) {
	lg, buf := newCapturingLogger(t)
	clk := &fakeClock{now: time.Unix(1_000, 0)}
	writeErr := &syscallLikeErr{op: "write", errno: syscall.EIO}
	d := newTestDiagnosingTUN(writeErr, clk, lg, false /* down */, 1420, nil)

	if _, err := d.Write([][]byte{{1, 2, 3}}, 0); !errors.Is(err, syscall.EIO) {
		t.Fatalf("Write err = %v, want EIO passthrough", err)
	}

	got := buf.String()
	for _, want := range []string{"wanbond0", "DOWN", "install.md §4", "input/output error"} {
		if !strings.Contains(got, want) {
			t.Errorf("diagnostic record missing %q\ngot: %s", want, got)
		}
	}
	// The raw errno (5 = EIO on Linux) must be present, not just the human string, so an
	// operator/tool can key off the numeric code.
	if !strings.Contains(got, `"errno":5`) {
		t.Errorf("diagnostic record missing raw errno field, got: %s", got)
	}
}

// TestDiagnosingTUNWriteEIOUpButFailing asserts the decorator does NOT assume DOWN
// unconditionally: when the interface is actually UP, the record must say so (and still name
// the MTU + install.md §4), not repeat the DOWN wording verbatim. This is the mutation
// discriminator for the state branch — an implementation that always emits the DOWN message
// regardless of the probed state passes the first test but fails this one.
func TestDiagnosingTUNWriteEIOUpButFailing(t *testing.T) {
	lg, buf := newCapturingLogger(t)
	clk := &fakeClock{now: time.Unix(1_000, 0)}
	writeErr := &syscallLikeErr{op: "write", errno: syscall.EIO}
	d := newTestDiagnosingTUN(writeErr, clk, lg, true /* up */, 1280, nil)

	if _, err := d.Write([][]byte{{1}}, 0); !errors.Is(err, syscall.EIO) {
		t.Fatalf("Write err = %v, want EIO passthrough", err)
	}

	got := buf.String()
	if strings.Contains(got, `"state":"DOWN"`) {
		t.Errorf("record claims DOWN for an UP interface: %s", got)
	}
	for _, want := range []string{`"state":"UP"`, "1280", "install.md §4"} {
		if !strings.Contains(got, want) {
			t.Errorf("diagnostic record missing %q\ngot: %s", want, got)
		}
	}
}

// TestDiagnosingTUNWriteNonEIONotDiagnosed asserts a write failure that is NOT EIO produces no
// diagnostic — the decorator targets the specific EIO/DOWN-tun footgun (D39), not every write
// error. Also the mutation discriminator against "diagnose on any non-nil error".
func TestDiagnosingTUNWriteNonEIONotDiagnosed(t *testing.T) {
	lg, buf := newCapturingLogger(t)
	clk := &fakeClock{now: time.Unix(1_000, 0)}
	d := newTestDiagnosingTUN(syscall.EINVAL, clk, lg, false, 1420, nil)

	if _, err := d.Write([][]byte{{1}}, 0); !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("Write err = %v, want EINVAL passthrough", err)
	}
	if got := buf.String(); got != "" {
		t.Errorf("non-EIO write logged a diagnostic, want none: %s", got)
	}
}

// TestDiagnosingTUNWriteSuccessNotDiagnosed asserts a successful write logs nothing.
func TestDiagnosingTUNWriteSuccessNotDiagnosed(t *testing.T) {
	lg, buf := newCapturingLogger(t)
	clk := &fakeClock{now: time.Unix(1_000, 0)}
	d := newTestDiagnosingTUN(nil, clk, lg, true, 1420, nil)

	if _, err := d.Write([][]byte{{1}}, 0); err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}
	if got := buf.String(); got != "" {
		t.Errorf("successful write logged a diagnostic, want none: %s", got)
	}
}

// TestDiagnosingTUNRateLimitsBurst is the rate-limit acceptance check: a burst of EIO writes
// within the same diagnostic window yields exactly ONE diagnostic record, not one per write —
// the mutation discriminator against an unthrottled "log every EIO" implementation.
func TestDiagnosingTUNRateLimitsBurst(t *testing.T) {
	lg, buf := newCapturingLogger(t)
	clk := &fakeClock{now: time.Unix(1_000, 0)}
	writeErr := &syscallLikeErr{op: "write", errno: syscall.EIO}
	d := newTestDiagnosingTUN(writeErr, clk, lg, false, 1420, nil)

	const burst = 50
	for i := 0; i < burst; i++ {
		if _, err := d.Write([][]byte{{1}}, 0); !errors.Is(err, syscall.EIO) {
			t.Fatalf("write %d err = %v, want EIO passthrough", i, err)
		}
		// The storm happens well inside the same rate-limit window: no clock advance.
	}

	if got := strings.Count(buf.String(), "TUN write failed"); got != 1 {
		t.Fatalf("diagnostics logged during burst = %d, want exactly 1\n%s", got, buf.String())
	}

	// Once the rate-limit window elapses, a FRESH EIO is diagnosed again: the limiter throttles
	// a storm, it does not permanently latch off after the first incident. This is the discriminator
	// against an implementation that (incorrectly) diagnoses at most once ever.
	clk.advance(tunDiagnosticInterval)
	if _, err := d.Write([][]byte{{1}}, 0); !errors.Is(err, syscall.EIO) {
		t.Fatalf("post-window write err = %v, want EIO passthrough", err)
	}
	if got := strings.Count(buf.String(), "TUN write failed"); got != 2 {
		t.Fatalf("diagnostics logged after the rate-limit window elapsed = %d, want 2\n%s", got, buf.String())
	}

	// A write JUST inside the next window (one nanosecond short) must NOT re-diagnose: proves the
	// comparison is a strict "< interval", not "<= interval" or an off-by-one.
	clk.advance(tunDiagnosticInterval - time.Nanosecond)
	if _, err := d.Write([][]byte{{1}}, 0); !errors.Is(err, syscall.EIO) {
		t.Fatalf("near-window write err = %v, want EIO passthrough", err)
	}
	if got := strings.Count(buf.String(), "TUN write failed"); got != 2 {
		t.Fatalf("diagnostics logged just inside the window = %d, want still 2\n%s", got, buf.String())
	}
}

// TestDiagnosingTUNStateProbeFailure asserts a failed interface-state probe (e.g. the interface
// disappeared entirely) still logs an actionable record pointing at install.md §4 and carrying
// the raw errno, rather than silently dropping the diagnostic.
func TestDiagnosingTUNStateProbeFailure(t *testing.T) {
	lg, buf := newCapturingLogger(t)
	clk := &fakeClock{now: time.Unix(1_000, 0)}
	writeErr := &syscallLikeErr{op: "write", errno: syscall.EIO}
	d := newTestDiagnosingTUN(writeErr, clk, lg, false, 0, errors.New("no such device"))

	if _, err := d.Write([][]byte{{1}}, 0); !errors.Is(err, syscall.EIO) {
		t.Fatalf("Write err = %v, want EIO passthrough", err)
	}
	got := buf.String()
	for _, want := range []string{"wanbond0", "install.md §4", `"errno":5`} {
		if !strings.Contains(got, want) {
			t.Errorf("diagnostic record missing %q\ngot: %s", want, got)
		}
	}
}

// syscallLikeErr mimics the *os.PathError/*fs.PathError wrapping a real TUN write EIO carries
// (e.g. "write /dev/net/tun: input/output error"), so errors.Is(err, syscall.EIO) and
// errors.As(err, &syscall.Errno{}) both succeed exactly as they would against the real error.
type syscallLikeErr struct {
	op    string
	errno syscall.Errno
}

func (e *syscallLikeErr) Error() string { return e.op + ": " + e.errno.Error() }
func (e *syscallLikeErr) Unwrap() error { return e.errno }

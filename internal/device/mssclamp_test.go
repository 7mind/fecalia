package device

import (
	"testing"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/config"
)

// TestClampMSS pins the MSS-derivation invariant of the daemon-owned edge-originated TCP
// clamp (T208, D85): the effective MSS is the wanbond0 inner MTU minus the inner IPv4+TCP
// header overhead (20+20 = 40). This is the NON-privileged half of the task's test split —
// it verifies the arithmetic in the local gate; the rule-install lifecycle is asserted by
// the privileged, hardware-tier TestE2EDaemonMSSClampLifecycle (test/e2e, //go:build e2e).
//
// The clamp is programmed with --clamp-mss-to-pmtu, so the KERNEL derives this same value
// from the live interface MTU; clampMSS reproduces it for the boot log line, and this test
// keeps the two definitions of "the MSS we enforce" from drifting apart.
func TestClampMSS(t *testing.T) {
	const tcpV4HeaderOverhead = 40 // 20 (IPv4 header) + 20 (TCP header)

	// A representative two-path edge config: min-across-paths TUN sizing (T205) makes the
	// 1400 path the binding one, so tunMTU = bind.InnerMTU(1400, false) and the clamp MSS is
	// that minus the 40-byte inner header overhead.
	for _, fecEnabled := range []bool{false, true} {
		cfg := &config.Config{
			Paths: []config.Path{
				{Name: "starlink", MTU: 1500},
				{Name: "lte", MTU: 1400},
			},
			FEC: config.FEC{Enabled: fecEnabled},
		}
		innerMTU := tunMTU(cfg)
		want := innerMTU - tcpV4HeaderOverhead
		if got := clampMSS(innerMTU); got != want {
			t.Fatalf("fec=%v: clampMSS(tunMTU=%d) = %d, want %d (inner MTU - 40)", fecEnabled, innerMTU, got, want)
		}
		// Cross-check the binding path is the 1400 one (guards against the derivation being
		// accidentally correct only because both paths coincided).
		if innerMTU != bind.InnerMTU(1400, fecEnabled) {
			t.Fatalf("fec=%v: tunMTU = %d, want min-across-paths bind.InnerMTU(1400) = %d", fecEnabled, innerMTU, bind.InnerMTU(1400, fecEnabled))
		}
	}
}

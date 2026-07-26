package config

import (
	"testing"
	"time"
)

func TestRecoveryBoundStartsAtReceiverObservableCut(t *testing.T) {
	body := byteShaperFixture(
		"192.0.2.10",
		"8Mbit",
		"45ms",
		0,
		"pacing_enabled = true\n",
	) + `
[fec]
enabled = true
adaptive = true
data_shards = 8
parity_shards = 4
target_residual = 0.001
deadline = "5ms"
`
	cfg, err := loadByteShaperFixture(t, body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	shaper := onlyPathShaper(t, cfg)
	if shaper.DataBurstBytes != 45_000 {
		t.Fatalf("DATA budget B = %d, want unchanged BDP budget 45000", shaper.DataBurstBytes)
	}
	if shaper.RecoveryWriteSlack != 10*time.Millisecond {
		t.Fatalf("recovery cut I = %s, want 10ms", shaper.RecoveryWriteSlack)
	}
	if shaper.RecoveryBound != shaper.RecoveryWriteSlack {
		t.Fatalf(
			"advertised receiver-observable A = %s, want enforced cut I = %s; sender-side queue prefix elapses before a receiver gap can arm",
			shaper.RecoveryBound,
			shaper.RecoveryWriteSlack,
		)
	}
}

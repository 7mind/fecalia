//go:build adaptivepolicy

package bond_test

import (
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

func TestVoiceAndBulkShareSingleSlowLane(t *testing.T) {
	const (
		laneBytesPerSecond = 50000
		voiceHz            = 100
		bulkBytes          = 1200
		feedbackHz         = 40
		feedbackWireBytes  = 193
	)
	delivered, sent, p99, _, bulk := voiceThroughOutage(t,
		[]float64{laneBytesPerSecond}, []time.Duration{20 * time.Millisecond}, []time.Duration{0}, 0, -1, 0, 0)
	available := float64(laneBytesPerSecond-voiceHz*(voiceWireGuardBytes+bond.Overhead+28)-feedbackHz*feedbackWireBytes) * bulkBytes / (bulkBytes + bond.Overhead + 28)
	t.Logf("voice %d/%d, one-way p99 %s; bulk %v B/s; conservative payload reference %.0f B/s", delivered, sent, p99, bulk, available)
	if delivered < sent*99/100 || p99 > 75*time.Millisecond {
		t.Errorf("voice displaced on sole survivor: %d/%d delivered, one-way p99 %s", delivered, sent, p99)
	}
	for second, bytes := range bulk {
		if float64(bytes) < 0.75*available {
			t.Errorf("measured second %d: bulk %d B/s, want at least %.0f after voice and feedback", second, bytes, 0.75*available)
		}
	}
}

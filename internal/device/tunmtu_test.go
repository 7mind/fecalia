package device

import (
	"testing"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/config"
)

// TestTunMTUMinAcrossPaths pins tunMTU to the min-across-paths rule (T205, D85):
// each path's configured MTU (0 -> bind.DefaultPathMTU) is mapped through
// bind.InnerMTU, and the TUN is sized to the SMALLEST resulting inner MTU, not a
// hardcoded bind.DefaultPathMTU. A mixed {1500, 1400} config must yield
// bind.InnerMTU(1400, fec) — the current single-1500 implementation ignores
// per-path MTU entirely and must FAIL this case.
func TestTunMTUMinAcrossPaths(t *testing.T) {
	for _, fecEnabled := range []bool{false, true} {
		cfg := &config.Config{
			Paths: []config.Path{
				{Name: "a", MTU: 1500},
				{Name: "b", MTU: 1400},
			},
			FEC: config.FEC{Enabled: fecEnabled},
		}
		want := bind.InnerMTU(1400, fecEnabled)
		if got := tunMTU(cfg); got != want {
			t.Fatalf("fec=%v: tunMTU(mixed 1500/1400) = %d, want %d (min-across-paths on the 1400 path)", fecEnabled, got, want)
		}
	}
}

// TestTunMTUZeroMeansDefault confirms a path with MTU=0 ("unset") falls back to
// bind.DefaultPathMTU when computing the min, unchanged from pre-T205 behavior for
// an all-default config.
func TestTunMTUZeroMeansDefault(t *testing.T) {
	cfg := &config.Config{
		Paths: []config.Path{{Name: "a", MTU: 0}},
		FEC:   config.FEC{Enabled: false},
	}
	want := bind.InnerMTU(bind.DefaultPathMTU, false)
	if got := tunMTU(cfg); got != want {
		t.Fatalf("tunMTU(single unset path) = %d, want %d", got, want)
	}
}

// TestTunMTUNoPaths confirms an empty path set (defensive; validate() normally
// requires at least one path) falls back to bind.DefaultPathMTU rather than
// panicking or producing a degenerate MTU.
func TestTunMTUNoPaths(t *testing.T) {
	cfg := &config.Config{FEC: config.FEC{Enabled: false}}
	want := bind.InnerMTU(bind.DefaultPathMTU, false)
	if got := tunMTU(cfg); got != want {
		t.Fatalf("tunMTU(no paths) = %d, want %d", got, want)
	}
}

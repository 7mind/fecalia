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

// TestObfuscationMTUHeadroom pins D85 fix-direction 4 (T225): when AmneziaWG obfuscation
// is configured, tunMTU reserves the maximum junk-prefix length
// (config.Amnezia.MaxJunkPrefix) on top of the fixed outer overhead, so wanbond0 is sized
// for the true obfuscated data-frame envelope — L bytes smaller than with obfuscation
// off. With obfuscation OFF the derived MTU is byte-identical to the pre-T225 value
// (subtract 0). The pre-T225 tunMTU ignores obfuscation entirely and must FAIL the
// obfuscation-on case. Verified across FEC on/off (the junk reserve composes additively
// with the FEC parity penalty) and across the min-across-paths and single-path forms.
func TestObfuscationMTUHeadroom(t *testing.T) {
	// A complete obfuscation profile with S1=15, S2=92 -> max junk prefix 92.
	am := config.Amnezia{Jc: 4, Jmin: 8, Jmax: 80, S1: 15, S2: 92, H1: 1_111_111, H2: 2_222_222, H3: 3_333_333, H4: 4_444_444}
	l := am.MaxJunkPrefix()
	if l != 92 {
		t.Fatalf("MaxJunkPrefix() = %d, want 92 (max of S1=15,S2=92)", l)
	}
	if off := (config.Amnezia{}).MaxJunkPrefix(); off != 0 {
		t.Fatalf("MaxJunkPrefix() of unconfigured block = %d, want 0", off)
	}

	pathSets := map[string][]config.Path{
		"min-across-paths": {{Name: "a", MTU: 1500}, {Name: "b", MTU: 1400}},
		"single-unset":     {{Name: "a", MTU: 0}},
	}
	// The obfuscation-off effective MTU per path set: min-across-paths sizes to the 1400
	// path; single-unset falls back to DefaultPathMTU.
	wantOffPathMTU := map[string]int{"min-across-paths": 1400, "single-unset": bind.DefaultPathMTU}

	for name, paths := range pathSets {
		for _, fecEnabled := range []bool{false, true} {
			off := &config.Config{Paths: paths, FEC: config.FEC{Enabled: fecEnabled}}
			on := &config.Config{Paths: paths, FEC: config.FEC{Enabled: fecEnabled}, Amnezia: am}

			wantOff := bind.InnerMTU(wantOffPathMTU[name], fecEnabled)
			if got := tunMTU(off); got != wantOff {
				t.Fatalf("%s fec=%v: tunMTU(obf off) = %d, want %d (byte-identical to pre-T225)", name, fecEnabled, got, wantOff)
			}
			if got := tunMTU(on); got != wantOff-l {
				t.Fatalf("%s fec=%v: tunMTU(obf on, L=%d) = %d, want %d (L smaller than obf-off)", name, fecEnabled, l, got, wantOff-l)
			}
		}
	}
}

package device

import (
	"strings"
	"testing"

	"github.com/7mind/wanbond/internal/config"
)

// profileA and profileB are two DISTINCT, complete amnezia obfuscation profiles.
func profileA() config.Amnezia {
	return config.Amnezia{Jc: 4, Jmin: 8, Jmax: 80, S1: 15, S2: 92, H1: 1_111_111, H2: 2_222_222, H3: 3_333_333, H4: 4_444_444}
}
func profileB() config.Amnezia {
	return config.Amnezia{Jc: 6, Jmin: 10, Jmax: 90, S1: 20, S2: 100, H1: 5_555_555, H2: 6_666_666, H3: 7_777_777, H4: 8_888_888}
}

// TestAmneziaGuardSingleEngine pins the D2 invariant: at most one amnezia-configured
// engine may be live per process, because amneziawg-go keeps the magic-header
// message types in package-global state.
func TestAmneziaGuardSingleEngine(t *testing.T) {
	var g amneziaGuard

	// Plain WireGuard (unconfigured) never engages the guard, any number of times.
	for i := 0; i < 3; i++ {
		if err := g.acquire(config.Amnezia{}); err != nil {
			t.Fatalf("unconfigured acquire #%d = %v, want nil", i, err)
		}
	}

	// First configured engine is admitted.
	if err := g.acquire(profileA()); err != nil {
		t.Fatalf("first configured acquire = %v, want nil", err)
	}

	// A second engine with a DISTINCT profile is refused while the first is live.
	err := g.acquire(profileB())
	if err == nil {
		t.Fatal("second acquire with a distinct profile = nil, want refusal")
	}
	if !strings.Contains(err.Error(), "distinct obfuscation profile") {
		t.Fatalf("refusal error = %q, want the D2 single-engine message", err.Error())
	}

	// The SAME profile is admitted (idempotent reconfigure / refcount), and its
	// release must not free the still-held slot prematurely.
	if err := g.acquire(profileA()); err != nil {
		t.Fatalf("same-profile re-acquire = %v, want nil", err)
	}
	g.release(profileA())
	if err := g.acquire(profileB()); err == nil {
		t.Fatal("distinct profile admitted while original still held; refcount underflow")
	}

	// Once the last hold is released, a distinct profile may take over.
	g.release(profileA())
	if err := g.acquire(profileB()); err != nil {
		t.Fatalf("distinct profile after full release = %v, want nil", err)
	}
	g.release(profileB())
}

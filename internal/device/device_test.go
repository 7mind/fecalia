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

const (
	wantExclusivity = "process exclusivity"               // configured refused because another engine is live
	wantConfigLive  = "amnezia-configured engine is live" // plain refused because a configured engine is live
)

// TestAmneziaGuardConfiguredExclusivity pins the tightened D2 rule: an
// amnezia-configured engine requires PROCESS EXCLUSIVITY. A second configured
// engine is refused while the first is live — even with the SAME profile, because
// closing the first engine runs amneziawg-go's unconditional resetProtocol, which
// reverts the process-global message types to defaults under the second engine.
func TestAmneziaGuardConfiguredExclusivity(t *testing.T) {
	var g amneziaGuard

	if err := g.acquire(profileA()); err != nil {
		t.Fatalf("first configured acquire = %v, want nil", err)
	}

	// A second configured engine with a DISTINCT profile is refused.
	if err := g.acquire(profileB()); err == nil {
		t.Fatal("second configured (distinct) acquire = nil, want refusal")
	} else if !strings.Contains(err.Error(), wantExclusivity) {
		t.Fatalf("refusal error = %q, want substring %q", err.Error(), wantExclusivity)
	}

	// A second configured engine with the SAME profile is ALSO refused: the
	// refcount admission is unsound because the first engine's Close resets the
	// globals under the second, still-live same-profile engine.
	if err := g.acquire(profileA()); err == nil {
		t.Fatal("second configured (same profile) acquire = nil, want refusal")
	} else if !strings.Contains(err.Error(), wantExclusivity) {
		t.Fatalf("same-profile refusal error = %q, want substring %q", err.Error(), wantExclusivity)
	}

	// Once the sole configured engine is released, another may take over.
	g.release(profileA())
	if err := g.acquire(profileB()); err != nil {
		t.Fatalf("configured acquire after full release = %v, want nil", err)
	}
	g.release(profileB())
}

// TestAmneziaGuardPlainConfiguredOrderings covers BOTH bring-up orderings between
// a plain (unconfigured) engine and a configured one: whichever is live first, the
// configured engine can never coexist with any other engine.
func TestAmneziaGuardPlainConfiguredOrderings(t *testing.T) {
	// plain-then-configured: the configured engine is refused while a plain engine
	// is live (bringing it up would need exclusivity it cannot get).
	t.Run("plain then configured refused", func(t *testing.T) {
		var g amneziaGuard
		if err := g.acquire(config.Amnezia{}); err != nil {
			t.Fatalf("plain acquire = %v, want nil", err)
		}
		if err := g.acquire(profileA()); err == nil {
			t.Fatal("configured acquire while a plain engine is live = nil, want refusal")
		} else if !strings.Contains(err.Error(), wantExclusivity) {
			t.Fatalf("refusal error = %q, want substring %q", err.Error(), wantExclusivity)
		}
		// After the plain engine releases, the configured engine is admitted.
		g.release(config.Amnezia{})
		if err := g.acquire(profileA()); err != nil {
			t.Fatalf("configured acquire after plain release = %v, want nil", err)
		}
		g.release(profileA())
	})

	// configured-then-plain: a plain engine is refused while a configured engine is
	// live, because closing the plain engine would resetProtocol the globals under
	// the live amnezia tunnel.
	t.Run("configured then plain refused", func(t *testing.T) {
		var g amneziaGuard
		if err := g.acquire(profileA()); err != nil {
			t.Fatalf("configured acquire = %v, want nil", err)
		}
		if err := g.acquire(config.Amnezia{}); err == nil {
			t.Fatal("plain acquire while a configured engine is live = nil, want refusal")
		} else if !strings.Contains(err.Error(), wantConfigLive) {
			t.Fatalf("refusal error = %q, want substring %q", err.Error(), wantConfigLive)
		}
		// After the configured engine releases, plain engines are admitted again.
		g.release(profileA())
		if err := g.acquire(config.Amnezia{}); err != nil {
			t.Fatalf("plain acquire after configured release = %v, want nil", err)
		}
		g.release(config.Amnezia{})
	})
}

// TestAmneziaGuardPlainCoexist confirms plain (unconfigured) engines may coexist:
// they never set the process-global message types, and resetProtocol on their
// Close only restores the defaults they already use, so it is idempotent among
// them.
func TestAmneziaGuardPlainCoexist(t *testing.T) {
	var g amneziaGuard
	const n = 3
	for i := 0; i < n; i++ {
		if err := g.acquire(config.Amnezia{}); err != nil {
			t.Fatalf("plain acquire #%d = %v, want nil (plain engines must coexist)", i, err)
		}
	}
	// Releasing all of them frees the process for a configured engine.
	for i := 0; i < n; i++ {
		g.release(config.Amnezia{})
	}
	if err := g.acquire(profileA()); err != nil {
		t.Fatalf("configured acquire after all plain engines released = %v, want nil", err)
	}
	g.release(profileA())
}

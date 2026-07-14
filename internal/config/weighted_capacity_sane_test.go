package config

import (
	"strings"
	"testing"
)

// weightedCapacitySaneConfig is a two-path edge config (T144): %BW1%/%BW2% are
// spliced into path 1/2's body (empty leaves link_bandwidth/link_rtt undeclared for
// that path), %SCHED% is the [scheduler] block under test.
const weightedCapacitySaneConfig = `
role = "edge"
psk = "%PSK%"

[[paths]]
name = "starlink"
source_addr = "192.0.2.10"
%BW1%

[[paths]]
name = "cellular"
source_addr = "192.0.2.20"
%BW2%

[wireguard]
private_key = "%PRIV%"
[[wireguard.peers]]
public_key = "%PUB%"
endpoint = "203.0.113.5:51820"
allowed_ips = ["0.0.0.0/0"]

[log]
level = "info"
%SCHED%`

// bw8mbitGuardConsistent declares an 8mbit/20ms link — the SAME sizing
// weighted_engage_guard_test.go uses — whose ~666.7 fps implied capacity sits ABOVE
// the T142 guard threshold at per_path_capacity_fps=700 (schedWeightedGuardConsistent
// below), so a path carrying it keeps the T142 hard-fail guard passing.
const bw8mbitGuardConsistent = "link_bandwidth = \"8mbit\"\nlink_rtt = \"20ms\"\n"

// schedWeightedGuardConsistent pairs with bw8mbitGuardConsistent: lowering
// per_path_capacity_fps to 700 keeps engage_fraction(0.9)*700=630 fps at/below the
// 8mbit-implied ~666.7 fps ceiling, so a config declaring bw8mbitGuardConsistent on
// every declared path loads successfully under this scheduler block.
const schedWeightedGuardConsistent = "\n[scheduler]\npolicy = \"weighted\"\nper_path_capacity_fps = 700\n"

func withCapacitySane(bw1, bw2, sched string) string {
	r := strings.NewReplacer("%BW1%", bw1, "%BW2%", bw2, "%SCHED%", sched)
	return r.Replace(fill(weightedCapacitySaneConfig))
}

// TestWeightedCapacitySaneAllDeclared is the T144 SANE-VERIFIED case: every path
// declares a guard-consistent link_bandwidth under the weighted policy, so the
// verdict is true (gauge=1, no WARN).
func TestWeightedCapacitySaneAllDeclared(t *testing.T) {
	path := writeConfig(t, 0o600, withCapacitySane(bw8mbitGuardConsistent, bw8mbitGuardConsistent, schedWeightedGuardConsistent))
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.WeightedCapacitySane == nil || !*c.WeightedCapacitySane {
		t.Fatalf("WeightedCapacitySane = %v, want SANE-VERIFIED (true) when every path declares link_bandwidth", derefBool(c.WeightedCapacitySane))
	}
}

// TestWeightedCapacitySaneNoneDeclared is the T144 UNVERIFIABLE case: no path
// declares link_bandwidth, so the verdict is false (gauge=0, one startup WARN).
func TestWeightedCapacitySaneNoneDeclared(t *testing.T) {
	path := writeConfig(t, 0o600, withCapacitySane("", "", "\n[scheduler]\npolicy = \"weighted\"\n"))
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.WeightedCapacitySane == nil || *c.WeightedCapacitySane {
		t.Fatalf("WeightedCapacitySane = %v, want UNVERIFIABLE (false) when no path declares link_bandwidth", derefBool(c.WeightedCapacitySane))
	}
}

// TestWeightedCapacitySanePartiallyDeclared is the R155-pinned case: a PARTIAL
// declaration (one path declares link_bandwidth, the other does not) with pacing
// DISABLED (the shipped default) is a reachable, LOAD-SUCCEEDING state —
// deriveWeightedPacingFromBDP no-ops under disabled pacing, and the T142 guard only
// checks the DECLARED path (guard-consistent here) — yet the T144 verdict must still
// be UNVERIFIABLE, not SANE-VERIFIED: the undeclared path's capacity cannot be
// checked, so the aggregate accounting is not provably sane.
func TestWeightedCapacitySanePartiallyDeclared(t *testing.T) {
	path := writeConfig(t, 0o600, withCapacitySane(bw8mbitGuardConsistent, "", schedWeightedGuardConsistent))
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.WeightedCapacitySane == nil || *c.WeightedCapacitySane {
		t.Fatalf("WeightedCapacitySane = %v, want UNVERIFIABLE (false) for a partial declaration", derefBool(c.WeightedCapacitySane))
	}
}

// TestWeightedCapacitySaneNilUnderActiveBackup asserts the verdict is nil (not
// applicable — the metrics family is absent entirely) under the default
// active-backup policy, regardless of link_bandwidth declarations.
func TestWeightedCapacitySaneNilUnderActiveBackup(t *testing.T) {
	path := writeConfig(t, 0o600, withCapacitySane("", "", ""))
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.WeightedCapacitySane != nil {
		t.Fatalf("WeightedCapacitySane = %v, want nil (not applicable) under active-backup", *c.WeightedCapacitySane)
	}
}

// TestWeightedCapacitySaneDeclaredPathStillHardFails asserts T142's hard-fail guard
// still independently checks a DECLARED path that contradicts it even when another
// path is undeclared — the T144 WARN concerns only the UNdeclared paths; a declared
// path that contradicts the guard hard-fails Load exactly as it did before T144.
func TestWeightedCapacitySaneDeclaredPathStillHardFails(t *testing.T) {
	// 8mbit implies ~666.7 fps; the DEFAULT per_path_capacity_fps (10000) *
	// engage_fraction (0.9) = 9000 fps exceeds it — the T142 guard must still fire,
	// independent of the second path being undeclared.
	path := writeConfig(t, 0o600, withCapacitySane(bw8mbitGuardConsistent, "", "\n[scheduler]\npolicy = \"weighted\"\n"))
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load succeeded with a declared path whose bandwidth cannot sustain the engage threshold; want the T142 hard-fail guard to fire")
	}
	if !strings.Contains(err.Error(), "starlink") {
		t.Fatalf("error = %q, want it to name the contradicting declared path", err.Error())
	}
}

// derefBool renders a *bool for a failure message without panicking on nil.
func derefBool(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}

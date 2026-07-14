package config

import (
	"strings"
	"testing"
)

// weightedEngageGuardConfig is a single-path edge config whose one path declares an
// operator-declared link_bandwidth under the weighted policy with pacing DISABLED
// (the shipped default) and per_path_capacity_fps left at its synthetic default
// (10000) — the "chiefly bites" case T142's description calls out: the derive is a
// no-op (pacing off), so the synthetic default per-path capacity stands unchanged
// against a much slower declared link, and the aggregation engage threshold
// (engage_fraction * per_path_capacity_fps) can mathematically never be reached by a
// path capped at the declared bandwidth. %SCHED% is replaced with the [scheduler]
// block under test.
const weightedEngageGuardConfig = `
role = "edge"
psk = "%PSK%"

[[paths]]
name = "starlink"
source_addr = "192.0.2.10"
link_bandwidth = "8mbit"
link_rtt = "20ms"

[wireguard]
private_key = "%PRIV%"
[[wireguard.peers]]
public_key = "%PUB%"
endpoint = "203.0.113.5:51820"
allowed_ips = ["0.0.0.0/0"]

[metrics]
listen = "127.0.0.1:9096"

[log]
level = "info"
%SCHED%`

func withEngageGuardSched(sched string) string {
	return strings.NewReplacer("%SCHED%", sched).Replace(fill(weightedEngageGuardConfig))
}

// TestWeightedEngageAgainstBandwidthRefuses is the T142 acceptance (i) at the
// config-load level: policy = "weighted", a declared link_bandwidth = "8mbit", pacing
// DISABLED, and the default per_path_capacity_fps (10000) together imply aggregation
// can never engage at line rate (engage_fraction(0.9) * 10000 = 9000 frames/s exceeds
// the 8Mbit-implied ~666.7 frames/s ceiling) — Load must refuse, naming the path and
// all three numbers: the declared bandwidth, the implied capacity fps, and the
// engage-threshold fps.
func TestWeightedEngageAgainstBandwidthRefuses(t *testing.T) {
	path := writeConfig(t, 0o600, withEngageGuardSched("\n[scheduler]\npolicy = \"weighted\"\n"))
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load succeeded with an engage threshold that can never be reached at the declared link_bandwidth; want a hard-fail")
	}
	for _, want := range []string{"starlink", "8mbit", "666.7", "9000.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to contain %q (path name / declared bandwidth / implied capacity fps / engage-threshold fps)", err.Error(), want)
		}
	}
	t.Logf("Load refused as expected: %v", err)
}

// TestWeightedEngageAgainstBandwidthPassesWhenLowered is the negative case: lowering
// per_path_capacity_fps so engage_fraction * per_path_capacity_fps <= the
// bandwidth-implied capacity lets the same declared-bandwidth config load
// successfully — the guard is a threshold check, not a blanket rejection of
// link_bandwidth without pacing.
func TestWeightedEngageAgainstBandwidthPassesWhenLowered(t *testing.T) {
	// 8Mbit implies ~666.7 fps; engage_fraction defaults to 0.9, so
	// per_path_capacity_fps = 700 keeps the threshold (630) at/below the ceiling.
	path := writeConfig(t, 0o600, withEngageGuardSched("\n[scheduler]\npolicy = \"weighted\"\nper_path_capacity_fps = 700\n"))
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Scheduler.PerPathCapacityFPS != 700 {
		t.Fatalf("PerPathCapacityFPS = %g, want 700 (explicit, unaffected by the inert derive)", c.Scheduler.PerPathCapacityFPS)
	}
}

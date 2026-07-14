package config

import (
	"math"
	"strings"
	"testing"
	"time"
)

// edgePacingConfig is an edge config whose two paths carry an operator-declared
// per-link bandwidth + baseline RTT, so the weighted scheduler's pace can be sized
// from the bandwidth-delay product (T53). %SCHED% is replaced with the [scheduler]
// block under test. The 5g path is the slower (bottleneck) link.
const edgePacingConfig = `
role = "edge"
psk = "%PSK%"

[[paths]]
name = "starlink"
source_addr = "192.0.2.10"
link_bandwidth = "50Mbit"
link_rtt = "45ms"

[[paths]]
name = "5g"
source_addr = "192.0.2.20"
link_bandwidth = "10Mbit"
link_rtt = "30ms"

[wireguard]
private_key = "%PRIV%"
[[wireguard.peers]]
public_key = "%PUB%"
endpoint = "203.0.113.5:51820"
allowed_ips = ["0.0.0.0/0"]

[metrics]
listen = "127.0.0.1:9095"

[log]
level = "info"
%SCHED%`

// withSched fills the key placeholders and appends the [scheduler] block.
func withSched(sched string) string {
	return strings.NewReplacer("%SCHED%", sched).Replace(fill(edgePacingConfig))
}

// TestPacingDerivedFromDeclaredBandwidth (acceptance a): a declared per-link
// bandwidth under the weighted policy with pacing ENABLED sizes PerPathCapacityFPS +
// PacingBurstFrames from SizePacingFromBDP (correct-by-construction), to the SLOWEST
// declared link (the shared per-path pace must not exceed the bottleneck), and well
// below the synthetic default.
func TestPacingDerivedFromDeclaredBandwidth(t *testing.T) {
	path := writeConfig(t, 0o600, withSched("\n[scheduler]\npolicy = \"weighted\"\npacing_enabled = true\n"))
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// The bottleneck is the 10 Mbit / 30 ms path; the shared reference capacity and
	// burst must equal SizePacingFromBDP for that link at the full-MTU wire size.
	want, err := SizePacingFromBDP(10e6, 30*time.Millisecond, defaultAvgWireFrameBytes)
	if err != nil {
		t.Fatalf("SizePacingFromBDP: %v", err)
	}
	if math.Abs(c.Scheduler.PerPathCapacityFPS-want.CapacityFPS) > 1e-6 {
		t.Fatalf("PerPathCapacityFPS = %g, want %g (bottleneck-derived)", c.Scheduler.PerPathCapacityFPS, want.CapacityFPS)
	}
	if math.Abs(c.Scheduler.PacingBurstFrames-want.BurstFrames) > 1e-6 {
		t.Fatalf("PacingBurstFrames = %g, want %g (bottleneck BDP in frames)", c.Scheduler.PacingBurstFrames, want.BurstFrames)
	}
	// The whole point of D22/T53: the derived capacity is far below the synthetic
	// default, so the aggregation gate and pace actually bind on a realistic uplink.
	if c.Scheduler.PerPathCapacityFPS >= defaultPerPathCapacityFPS {
		t.Fatalf("derived capacity %g not below synthetic default %g", c.Scheduler.PerPathCapacityFPS, defaultPerPathCapacityFPS)
	}
}

// TestPacingDisabledLeavesDerivationInert (acceptance c): a declared bandwidth with
// pacing DISABLED (the shipped default) is inert — the synthetic default per-path
// capacity/burst are preserved, so pacing continues to ship disabled and un-sized.
func TestPacingDisabledLeavesDerivationInert(t *testing.T) {
	// weighted policy, pacing left at its default (false), bandwidth still declared.
	// Bandwidths here (150/120 Mbit) are deliberately far above edgePacingConfig's
	// 50/10 Mbit: with pacing off the synthetic default per_path_capacity_fps (10000)
	// stands unchanged, and T142's engage-vs-bandwidth guard requires every declared
	// path to sustain engage_fraction(0.9)*10000 = 9000 frames/s — 50/10 Mbit cannot
	// (implied ~4166.7/833.3 fps), but 150/120 Mbit comfortably can (implied
	// ~12500/10000 fps), so this fixture stays a config the guard accepts while still
	// exercising "declared bandwidth + pacing off = inert derivation".
	path := writeConfig(t, 0o600, fill(twoPathConfig("150Mbit", "45ms", "120Mbit", "30ms"))+"\n[scheduler]\npolicy = \"weighted\"\n")
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Scheduler.PacingEnabled {
		t.Fatal("pacing must default to disabled")
	}
	if c.Scheduler.PerPathCapacityFPS != defaultPerPathCapacityFPS {
		t.Fatalf("PerPathCapacityFPS = %g, want synthetic default %g (derivation must be inert with pacing off)", c.Scheduler.PerPathCapacityFPS, defaultPerPathCapacityFPS)
	}
	if c.Scheduler.PacingBurstFrames != defaultPacingBurstFrames {
		t.Fatalf("PacingBurstFrames = %g, want synthetic default %g", c.Scheduler.PacingBurstFrames, defaultPacingBurstFrames)
	}
}

// TestPacingNoDeclaredBandwidthFallsBackToDefault (acceptance d): weighted + pacing
// enabled but NO declared bandwidth keeps the synthetic default capacity/burst.
func TestPacingNoDeclaredBandwidthFallsBackToDefault(t *testing.T) {
	// The base edgeConfig (no link_bandwidth) + weighted + pacing on.
	body := fill(edgeConfig) + "\n[scheduler]\npolicy = \"weighted\"\npacing_enabled = true\n"
	path := writeConfig(t, 0o600, body)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Scheduler.PerPathCapacityFPS != defaultPerPathCapacityFPS {
		t.Fatalf("PerPathCapacityFPS = %g, want synthetic default %g", c.Scheduler.PerPathCapacityFPS, defaultPerPathCapacityFPS)
	}
	if c.Scheduler.PacingBurstFrames != defaultPacingBurstFrames {
		t.Fatalf("PacingBurstFrames = %g, want synthetic default %g", c.Scheduler.PacingBurstFrames, defaultPacingBurstFrames)
	}
}

// TestPacingDeclaredBandwidthRejects (acceptance b): the new field is fail-fast
// validated at config load — an unparseable/non-positive bandwidth or RTT, a mixed
// (all-or-nothing) declaration, a missing RTT under pacing, and a redundant raw
// frame-slot knob are all rejected.
func TestPacingDeclaredBandwidthRejects(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "unparseable bandwidth",
			body: fill(twoPathConfig("not-a-rate", "45ms", "10Mbit", "30ms") + weightedPacing),
			want: "invalid link_bandwidth",
		},
		{
			name: "zero bandwidth",
			body: fill(twoPathConfig("0Mbit", "45ms", "10Mbit", "30ms") + weightedPacing),
			want: "link_bandwidth must be > 0",
		},
		{
			name: "negative bandwidth",
			body: fill(twoPathConfig("-5Mbit", "45ms", "10Mbit", "30ms") + weightedPacing),
			want: "link_bandwidth must be > 0",
		},
		{
			name: "unparseable rtt",
			body: fill(twoPathConfig("50Mbit", "45quux", "10Mbit", "30ms") + weightedPacing),
			want: "invalid link_rtt",
		},
		{
			name: "non-positive rtt",
			body: fill(twoPathConfig("50Mbit", "0ms", "10Mbit", "30ms") + weightedPacing),
			want: "link_rtt must be > 0",
		},
		{
			name: "mixed declaration (all-or-nothing)",
			body: fill(twoPathConfig("50Mbit", "45ms", "", "") + weightedPacing),
			want: "must be declared on ALL paths or none",
		},
		{
			name: "missing rtt under pacing",
			body: fill(twoPathConfig("50Mbit", "", "10Mbit", "") + weightedPacing),
			want: "link_rtt is required",
		},
		{
			name: "mutually exclusive with raw capacity knob",
			body: withSched("\n[scheduler]\npolicy = \"weighted\"\npacing_enabled = true\nper_path_capacity_fps = 5000\n"),
			want: "mutually exclusive",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeConfig(t, 0o600, c.body)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), c.want)
			}
		})
	}
}

// weightedPacing is the [scheduler] block that turns the weighted policy on with
// pacing enabled — the mode under which declared bandwidth is derived.
const weightedPacing = "\n[scheduler]\npolicy = \"weighted\"\npacing_enabled = true\n"

// twoPathConfig builds a two-path edge config (still requiring fill) whose starlink
// and 5g paths carry the given per-link bandwidth/RTT declarations. An empty string
// omits that key entirely, so a caller can exercise a mixed/absent declaration.
func twoPathConfig(bw0, rtt0, bw1, rtt1 string) string {
	pathBlock := func(name, addr, bw, rtt string) string {
		s := "\n[[paths]]\nname = \"" + name + "\"\nsource_addr = \"" + addr + "\"\n"
		if bw != "" {
			s += "link_bandwidth = \"" + bw + "\"\n"
		}
		if rtt != "" {
			s += "link_rtt = \"" + rtt + "\"\n"
		}
		return s
	}
	return "\nrole = \"edge\"\npsk = \"%PSK%\"\n" +
		pathBlock("starlink", "192.0.2.10", bw0, rtt0) +
		pathBlock("5g", "192.0.2.20", bw1, rtt1) +
		"\n[wireguard]\nprivate_key = \"%PRIV%\"\n" +
		"[[wireguard.peers]]\npublic_key = \"%PUB%\"\n" +
		"endpoint = \"203.0.113.5:51820\"\nallowed_ips = [\"0.0.0.0/0\"]\n\n" +
		"[metrics]\nlisten = \"127.0.0.1:9095\"\n\n[log]\nlevel = \"info\"\n"
}

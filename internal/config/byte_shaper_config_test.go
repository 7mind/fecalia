package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

func byteShaperFixture(sourceAddr, bandwidth, rtt string, mtu int, scheduler string) string {
	pathSizing := ""
	if bandwidth != "" {
		pathSizing += fmt.Sprintf("link_bandwidth = %q\n", bandwidth)
	}
	if rtt != "" {
		pathSizing += fmt.Sprintf("link_rtt = %q\n", rtt)
	}
	if mtu != 0 {
		pathSizing += fmt.Sprintf("mtu = %d\n", mtu)
	}
	return fill(fmt.Sprintf(`
role = "concentrator"
psk = "%%PSK%%"

[[paths]]
name = "wan"
source_addr = %q
%s
[wireguard]
private_key = "%%PRIV%%"
listen_port = 51820

[[wireguard.peers]]
public_key = "%%PUB%%"
allowed_ips = ["10.0.0.2/32"]

[scheduler]
policy = "active-backup"
%s
`, sourceAddr, pathSizing, scheduler))
}

func loadByteShaperFixture(t *testing.T, body string) (*Config, error) {
	t.Helper()
	return Load(writeConfig(t, 0o600, body))
}

func onlyPathShaper(t *testing.T, cfg *Config) PathShaperConfig {
	t.Helper()
	if len(cfg.Scheduler.PerPathShapers) != 1 {
		t.Fatalf("PerPathShapers = %+v, want exactly one entry", cfg.Scheduler.PerPathShapers)
	}
	return cfg.Scheduler.PerPathShapers[0]
}

func TestPathShaperDerivedBytesAndLegacyFPS(t *testing.T) {
	cfg, err := loadByteShaperFixture(t, byteShaperFixture(
		"192.0.2.10", "8Mbit", "45ms", 0,
		"pacing_enabled = true\n",
	))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	shaper := onlyPathShaper(t, cfg)
	if shaper.RateBytesPerSecond != 1_000_000 {
		t.Errorf("RateBytesPerSecond = %g, want 1000000", shaper.RateBytesPerSecond)
	}
	if shaper.DataBurstBytes != 45_000 {
		t.Errorf("DataBurstBytes = %d, want ceil(1000000*45ms) = 45000", shaper.DataBurstBytes)
	}
	if shaper.MaxEncodedDatagramBytes != 1472 {
		t.Errorf("MaxEncodedDatagramBytes = %d, want 1500-IPv4/UDP(28) = 1472", shaper.MaxEncodedDatagramBytes)
	}
	if shaper.ControlReserveBytes != shaper.MaxEncodedDatagramBytes {
		t.Errorf("ControlReserveBytes = %d, want Lmax = %d", shaper.ControlReserveBytes, shaper.MaxEncodedDatagramBytes)
	}
	if shaper.ProbeBurstBytes != 2*shaper.MaxEncodedDatagramBytes {
		t.Errorf("ProbeBurstBytes = %d, want probe+echo = %d", shaper.ProbeBurstBytes, 2*shaper.MaxEncodedDatagramBytes)
	}
	wantProbeRate := float64(shaper.ProbeBurstBytes) / livenessProbeInterval.Seconds()
	if shaper.ProbeRateBytesPerSecond != wantProbeRate {
		t.Errorf("ProbeRateBytesPerSecond = %g, want %g", shaper.ProbeRateBytesPerSecond, wantProbeRate)
	}

	wantLegacy, err := SizePacingFromBDP(8e6, 45*time.Millisecond, defaultAvgWireFrameBytes)
	if err != nil {
		t.Fatalf("SizePacingFromBDP: %v", err)
	}
	if len(cfg.Scheduler.PerPathCapacities) != 1 || math.Abs(cfg.Scheduler.PerPathCapacities[0]-wantLegacy.CapacityFPS) > 1e-9 {
		t.Errorf("legacy PerPathCapacities = %v, want offered-wire-frame rate %g", cfg.Scheduler.PerPathCapacities, wantLegacy.CapacityFPS)
	}
	if len(cfg.Scheduler.PacingBursts) != 1 || math.Abs(cfg.Scheduler.PacingBursts[0]-wantLegacy.BurstFrames) > 1e-9 {
		t.Errorf("legacy PacingBursts = %v, want %g", cfg.Scheduler.PacingBursts, wantLegacy.BurstFrames)
	}
}

func TestPathShaperKeepsWeightedAggregationFPS(t *testing.T) {
	body := fill(twoPathConfig("8Mbit", "45ms", "8Mbit", "45ms")) + weightedPacing
	cfg, err := loadByteShaperFixture(t, body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	wantLegacy, err := SizePacingFromBDP(8e6, 45*time.Millisecond, defaultAvgWireFrameBytes)
	if err != nil {
		t.Fatalf("SizePacingFromBDP: %v", err)
	}
	if math.Abs(cfg.Scheduler.PerPathCapacityFPS-wantLegacy.CapacityFPS) > 1e-9 {
		t.Fatalf("weighted aggregation PerPathCapacityFPS = %g, want unchanged offered-wire-frame rate %g",
			cfg.Scheduler.PerPathCapacityFPS, wantLegacy.CapacityFPS)
	}
	if math.Abs(cfg.Scheduler.PacingBurstFrames-wantLegacy.BurstFrames) > 1e-9 {
		t.Fatalf("legacy weighted PacingBurstFrames = %g, want unchanged %g",
			cfg.Scheduler.PacingBurstFrames, wantLegacy.BurstFrames)
	}
	if len(cfg.Scheduler.PerPathShapers) != 2 {
		t.Fatalf("PerPathShapers = %+v, want one independently-derived byte config per path", cfg.Scheduler.PerPathShapers)
	}
	for i, shaper := range cfg.Scheduler.PerPathShapers {
		if shaper.RateBytesPerSecond != 1_000_000 || shaper.DataBurstBytes != 45_000 {
			t.Errorf("PerPathShapers[%d] = %+v, want R=1000000 B=45000", i, shaper)
		}
	}
}

func TestPathShaperMaximumDatagramUsesOuterAddressFamily(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sourceAddr string
		mtu        int
		wantLmax   int
	}{
		{name: "IPv4 default MTU", sourceAddr: "192.0.2.10", wantLmax: 1472},
		{name: "IPv4 declared MTU", sourceAddr: "192.0.2.10", mtu: 1400, wantLmax: 1372},
		{name: "IPv6 default MTU", sourceAddr: "2001:db8::10", wantLmax: 1452},
		{name: "IPv6 declared MTU", sourceAddr: "2001:db8::10", mtu: 1400, wantLmax: 1352},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadByteShaperFixture(t, byteShaperFixture(
				tc.sourceAddr, "8Mbit", "45ms", tc.mtu,
				"pacing_enabled = true\n",
			))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := onlyPathShaper(t, cfg).MaxEncodedDatagramBytes; got != tc.wantLmax {
				t.Fatalf("MaxEncodedDatagramBytes = %d, want %d", got, tc.wantLmax)
			}
		})
	}
}

func TestPathShaperLinkDerivedBurstBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rtt     string
		wantB   int
		wantErr string
	}{
		{name: "B equals Lmax", rtt: "1472us", wantB: 1472},
		{name: "B below Lmax", rtt: "1471us", wantErr: "DATA burst 1471 bytes is below maximum encoded datagram 1472 bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadByteShaperFixture(t, byteShaperFixture(
				"192.0.2.10", "8Mbit", tc.rtt, 0,
				"pacing_enabled = true\n",
			))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Load error = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := onlyPathShaper(t, cfg).DataBurstBytes; got != tc.wantB {
				t.Fatalf("DataBurstBytes = %d, want %d", got, tc.wantB)
			}
		})
	}
}

func TestPathShaperLegacyRawBurstBoundary(t *testing.T) {
	equalBurstFrames := strconv.FormatFloat(float64(1472)/defaultAvgWireFrameBytes, 'g', -1, 64)
	for _, tc := range []struct {
		name        string
		burstFrames string
		wantB       int
		wantErr     string
	}{
		{name: "converted B equals Lmax", burstFrames: equalBurstFrames, wantB: 1472},
		{name: "converted B below Lmax", burstFrames: "0.98", wantErr: "scheduler.pacing_burst_frames=0.98 converts to a DATA burst 1470 bytes below path \"wan\" maximum encoded datagram 1472 bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheduler := fmt.Sprintf("pacing_enabled = true\nper_path_capacity_fps = 10\npacing_burst_frames = %s\n", tc.burstFrames)
			cfg, err := loadByteShaperFixture(t, byteShaperFixture("192.0.2.10", "", "", 0, scheduler))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Load error = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := onlyPathShaper(t, cfg).DataBurstBytes; got != tc.wantB {
				t.Fatalf("DataBurstBytes = %d, want %d", got, tc.wantB)
			}
		})
	}
}

func TestPathShaperProbeRateBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		bandwidth string
		rtt       string
		wantRate  float64
		wantErr   string
	}{
		{name: "Rp below R", bandwidth: "117768bit", rtt: "100ms", wantRate: 14_721},
		{name: "Rp equals R", bandwidth: "117760bit", rtt: "100ms", wantErr: "generated probe+echo rate 14720 bytes/s must be < shaper rate 14720 bytes/s"},
		{name: "Rp above R", bandwidth: "108000bit", rtt: "110ms", wantErr: "generated probe+echo rate 14720 bytes/s must be < shaper rate 13500 bytes/s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadByteShaperFixture(t, byteShaperFixture(
				"192.0.2.10", tc.bandwidth, tc.rtt, 0, "pacing_enabled = true\n",
			))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Load error = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := onlyPathShaper(t, cfg).RateBytesPerSecond; got != tc.wantRate {
				t.Fatalf("RateBytesPerSecond = %g, want %g", got, tc.wantRate)
			}
		})
	}
}

func TestPathShaperPacingDisabledIsInert(t *testing.T) {
	cfg, err := loadByteShaperFixture(t, byteShaperFixture("192.0.2.10", "8Mbit", "45ms", 0, ""))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Scheduler.PerPathShapers != nil {
		t.Fatalf("PerPathShapers = %+v, want nil while pacing is disabled", cfg.Scheduler.PerPathShapers)
	}
}

func TestPathShaperRejectsNonFiniteInputs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		bandwidth string
		scheduler string
		want      string
	}{
		{
			name:      "link bandwidth NaN",
			bandwidth: "NaNMbit",
			scheduler: "pacing_enabled = true\n",
			want:      "link_bandwidth must be finite and > 0",
		},
		{
			name:      "link bandwidth Inf",
			bandwidth: "+InfMbit",
			scheduler: "pacing_enabled = true\n",
			want:      "link_bandwidth must be finite and > 0",
		},
		{
			name:      "link bandwidth unit multiplication overflows",
			bandwidth: "1e308Gbit",
			scheduler: "pacing_enabled = true\n",
			want:      "link_bandwidth must be finite and > 0",
		},
		{
			name:      "legacy capacity NaN",
			scheduler: "pacing_enabled = true\nper_path_capacity_fps = nan\npacing_burst_frames = 1\n",
			want:      "scheduler.per_path_capacity_fps must be finite and > 0",
		},
		{
			name:      "legacy capacity Inf",
			scheduler: "pacing_enabled = true\nper_path_capacity_fps = inf\npacing_burst_frames = 1\n",
			want:      "scheduler.per_path_capacity_fps must be finite and > 0",
		},
		{
			name:      "legacy burst NaN",
			scheduler: "pacing_enabled = true\nper_path_capacity_fps = 10\npacing_burst_frames = nan\n",
			want:      "scheduler.pacing_burst_frames must be finite and > 0",
		},
		{
			name:      "legacy burst Inf",
			scheduler: "pacing_enabled = true\nper_path_capacity_fps = 10\npacing_burst_frames = inf\n",
			want:      "scheduler.pacing_burst_frames must be finite and > 0",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgBody := byteShaperFixture("192.0.2.10", tc.bandwidth, "45ms", 0, tc.scheduler)
			_, err := loadByteShaperFixture(t, cfgBody)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestPathShaperRejectsNonRepresentableDerivedValues(t *testing.T) {
	for _, tc := range []struct {
		name      string
		bandwidth string
		rtt       string
		scheduler string
		want      string
	}{
		{
			name:      "link burst becomes non-finite",
			bandwidth: "1e308bit",
			rtt:       "1h",
			scheduler: "pacing_enabled = true\n",
			want:      "derived DATA burst must be finite and > 0",
		},
		{
			name:      "link burst exceeds int",
			bandwidth: "1e20bit",
			rtt:       "1s",
			scheduler: "pacing_enabled = true\n",
			want:      "derived DATA burst exceeds maximum supported byte count",
		},
		{
			name:      "legacy rate multiplication becomes non-finite",
			scheduler: "pacing_enabled = true\nper_path_capacity_fps = 1e308\npacing_burst_frames = 1\n",
			want:      "derived shaper rate must be finite and > 0",
		},
		{
			name:      "legacy burst multiplication becomes non-finite",
			scheduler: "pacing_enabled = true\nper_path_capacity_fps = 10\npacing_burst_frames = 1e308\n",
			want:      "derived DATA burst must be finite and > 0",
		},
		{
			name:      "legacy burst exceeds int",
			scheduler: "pacing_enabled = true\nper_path_capacity_fps = 10\npacing_burst_frames = 1e100\n",
			want:      "derived DATA burst exceeds maximum supported byte count",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgBody := byteShaperFixture("192.0.2.10", tc.bandwidth, tc.rtt, 0, tc.scheduler)
			_, err := loadByteShaperFixture(t, cfgBody)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestPathShaperLoadRejectsQueueBudgetOverflow(t *testing.T) {
	const lmax = 1472
	maxIntExclusive := math.Ldexp(1, strconv.IntSize-1)
	burstBytes := float64(math.MaxInt - lmax + 1)
	roundedBurstBytes := math.Ceil(burstBytes)
	if roundedBurstBytes >= maxIntExclusive ||
		int(roundedBurstBytes) <= math.MaxInt-lmax {
		t.Fatalf("test DATA burst %g does not produce a representable overflowing B+C", roundedBurstBytes)
	}

	bandwidth := strconv.FormatFloat(burstBytes*bitsPerByte, 'g', -1, 64) + "bit"
	_, err := loadByteShaperFixture(t, byteShaperFixture(
		"192.0.2.10", bandwidth, "1s", 0, "pacing_enabled = true\n",
	))
	const want = "queue budget B+C must fit in int"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Load error = %v, want substring %q", err, want)
	}
}

func TestPathShaperLoadRejectsUnrepresentablePriorityBound(t *testing.T) {
	const lmax = 1472
	probeBurstBytes := probeFramesPerBurstPair * lmax
	probeRateBytesPerSecond := float64(probeBurstBytes) / livenessProbeInterval.Seconds()
	rateBytesPerSecond := math.Nextafter(probeRateBytesPerSecond, math.Inf(1))
	bandwidth := strconv.FormatFloat(rateBytesPerSecond*bitsPerByte, 'g', -1, 64) + "bit"

	_, err := loadByteShaperFixture(t, byteShaperFixture(
		"192.0.2.10", bandwidth, "100ms", 0, "pacing_enabled = true\n",
	))
	const want = "modeled priority delay bound exceeds time.Duration"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Load error = %v, want substring %q", err, want)
	}
}

func TestReceiverInvisibleQueueDoesNotInflateRecoveryBound(t *testing.T) {
	const lmax = 1472
	probeBurstBytes := probeFramesPerBurstPair * lmax
	probeRateBytesPerSecond := float64(probeBurstBytes) / livenessProbeInterval.Seconds()
	rateBytesPerSecond := probeRateBytesPerSecond + 0.001
	bandwidth := strconv.FormatFloat(rateBytesPerSecond*bitsPerByte, 'g', -1, 64) + "bit"
	body := byteShaperFixture(
		"192.0.2.10",
		bandwidth,
		"100000h",
		0,
		"pacing_enabled = true\n",
	) + "\n[fec]\nenabled = true\ndata_shards = 3\nparity_shards = 1\n"

	cfg, err := loadByteShaperFixture(t, body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	shaper := onlyPathShaper(t, cfg)
	if shaper.RecoveryBound != RecoveryWriteSlack {
		t.Fatalf("A = %s, want receiver-observable cut I = %s", shaper.RecoveryBound, RecoveryWriteSlack)
	}
}

func TestRecoveryServiceDurationRepresentationBoundaries(t *testing.T) {
	valid, err := deriveServiceDuration(
		"recovery bound A",
		1,
		float64(time.Second),
		RecoveryWriteSlack,
	)
	if err != nil {
		t.Fatal(err)
	}
	if valid != RecoveryWriteSlack+time.Nanosecond {
		t.Fatalf("valid service duration = %s, want %s", valid, RecoveryWriteSlack+time.Nanosecond)
	}

	for _, label := range []string{"recovery bound A", "completion overrun Ecompletion"} {
		_, err := deriveServiceDuration(label, math.MaxInt, math.SmallestNonzeroFloat64, 0)
		if err == nil || !strings.Contains(err.Error(), label+" cannot be represented as time.Duration") {
			t.Fatalf("%s overflow error = %v", label, err)
		}
	}

	_, err = deriveServiceDuration(
		"completion overrun Ecompletion",
		math.MaxInt,
		float64(math.MaxInt)*float64(time.Second)/float64(math.MaxInt64-time.Nanosecond),
		time.Nanosecond,
	)
	if err == nil {
		t.Fatal("Ecompletion mutation crossing MaxInt64 with slack was accepted")
	}
}

func TestFECGroupAndMtotalExactArithmeticBoundaries(t *testing.T) {
	const (
		kdata = 3
		mmax  = 1
		lmax  = 1472
	)
	ownership, err := deriveFECGroupOwnership(kdata, mmax, lmax)
	if err != nil {
		t.Fatal(err)
	}
	maxInnerDatagram := lmax - outerDataFrameOverhead - fecParityMTUPenalty
	lc := 8 + maxInnerDatagram
	ls := fecShardLengthPrefix + lc
	if ownership.codedInputBytes != kdata*lc {
		t.Fatalf("coded input = %d, want %d", ownership.codedInputBytes, kdata*lc)
	}
	if ownership.workspaceBytes != (kdata+mmax)*ls {
		t.Fatalf("workspace = %d, want %d", ownership.workspaceBytes, (kdata+mmax)*ls)
	}
	wantEncodedWire := kdata*(outerDataFrameOverhead+maxInnerDatagram) + mmax*lmax
	if ownership.encodedWireBytes != wantEncodedWire {
		t.Fatalf("encoded wire = %d, want %d", ownership.encodedWireBytes, wantEncodedWire)
	}
	wantFgroup := kdata*lc + (kdata+mmax)*ls + wantEncodedWire
	if ownership.totalBytes != wantFgroup {
		t.Fatalf("Fgroup = %d, want exact term sum %d", ownership.totalBytes, wantFgroup)
	}

	memory, err := checkedIntSum("memory bound Mtotal", 100, 20, 30, wantFgroup, 40)
	if err != nil {
		t.Fatal(err)
	}
	if want := 100 + 20 + 30 + wantFgroup + 40; memory != want {
		t.Fatalf("Mtotal = %d, want %d", memory, want)
	}
	if _, err := checkedIntSum("memory bound Mtotal", math.MaxInt, 1); err == nil {
		t.Fatal("Mtotal overflow was accepted")
	}
}

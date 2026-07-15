package device

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/tun/tuntest"
	"go.uber.org/goleak"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/metrics"
)

// writeWeightedGaugeConfig writes a two-path WEIGHTED-policy edge config whose paths
// both declare a guard-consistent link_bandwidth (8mbit/20ms at per_path_capacity_fps=700,
// T142), so config.Load computes WeightedCapacitySane = true (gauge seeds at 1). The
// [metrics] endpoint binds an OS-assigned loopback port so the /metrics series is scrapable.
func writeWeightedGaugeConfig(t *testing.T) *config.Config {
	t.Helper()
	privRaw, _ := genX25519(t)
	_, pubRaw := genX25519(t)

	body := `role = "edge"
psk = "` + randB64Key(t) + `"

[[paths]]
name = "starlink"
source_addr = "127.0.0.1"
link_bandwidth = "8mbit"
link_rtt = "20ms"

[[paths]]
name = "cellular"
source_addr = "127.0.0.2"
link_bandwidth = "8mbit"
link_rtt = "20ms"

[wireguard]
private_key = "` + base64.StdEncoding.EncodeToString(privRaw) + `"
[[wireguard.peers]]
public_key = "` + base64.StdEncoding.EncodeToString(pubRaw) + `"
endpoint = "127.0.0.1:51821"
allowed_ips = ["10.0.0.0/24"]

[metrics]
listen = "127.0.0.1:0"

[scheduler]
policy = "weighted"
per_path_capacity_fps = 700
`
	path := filepath.Join(t.TempDir(), "weighted.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v\n%s", err, body)
	}
	return cfg
}

// TestReloadRecomputesWeightedCapacityGauge is the D74 reproduction/acceptance: the
// running /metrics endpoint's wanbond_weighted_capacity_sane gauge is NOT frozen at its
// boot value — a Reload whose config-derived verdict changed (here, flipped to
// UNVERIFIABLE) re-sets the live gauge via Server.SetWeightedCapacitySane. On the unfixed
// code Reload never touched the gauge, so the scrape after reload still read the boot 1.
func TestReloadRecomputesWeightedCapacityGauge(t *testing.T) {
	defer goleak.VerifyNone(t)

	cfg := writeWeightedGaugeConfig(t)
	if cfg.WeightedCapacitySane == nil || !*cfg.WeightedCapacitySane {
		t.Fatalf("setup: boot config WeightedCapacitySane = %v, want true", cfg.WeightedCapacitySane)
	}
	chtun := tuntest.NewChannelTUN()

	tun, err := up(cfg, discardLogger(t), chtun.TUN(), "wanbondtest0", inertFactory)
	if err != nil {
		t.Fatalf("up (weighted) failed: %v", err)
	}
	defer tun.Close()

	if tun.metricsSrv == nil {
		t.Fatal("up did not start the /metrics endpoint")
	}

	scrape := func() float64 {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		exp, err := metrics.Fetch(ctx, http.DefaultClient, tun.metricsSrv.URL())
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		v, ok := exp.Value(metrics.MetricWeightedCapacitySane)
		if !ok {
			t.Fatalf("%s series absent, want present under the weighted policy", metrics.MetricWeightedCapacitySane)
		}
		return v
	}

	if got := scrape(); got != 1 {
		t.Fatalf("%s at boot = %v, want 1 (both paths declare link_bandwidth)", metrics.MetricWeightedCapacitySane, got)
	}

	// Reload with the SAME membership but a verdict flipped to UNVERIFIABLE — the running
	// path set is unchanged (no add/remove), so only the capacity-sanity recompute path is
	// exercised. The verdict pointer is set directly: this test pins Reload's gauge WIRING,
	// not config.Load's derivation (covered by the config package's own T144 tests).
	next := *tun.cfg
	unverifiable := false
	next.WeightedCapacitySane = &unverifiable
	if err := tun.Reload(&next); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if got := scrape(); got != 0 {
		t.Errorf("%s after a reload flipping the verdict = %v, want 0 (D74: Reload must recompute/re-set the gauge, not leave it frozen)", metrics.MetricWeightedCapacitySane, got)
	}
}

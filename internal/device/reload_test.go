package device

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
)

func path(name string) config.Path {
	return config.Path{Name: name, SourceAddr: netip.MustParseAddr("127.0.0.1")}
}

// TestDiffPaths pins the reload diff (T30): paths in the desired set but not live are
// ADDED, live paths absent from the desired set are REMOVED, and paths present in
// both are left untouched — diffed by name, independent of ordering.
func TestDiffPaths(t *testing.T) {
	cases := []struct {
		name       string
		live       []string
		desired    []config.Path
		wantAdd    []string
		wantRemove []string
	}{
		{
			name:    "no change",
			live:    []string{"a", "b"},
			desired: []config.Path{path("a"), path("b")},
		},
		{
			name:    "add one",
			live:    []string{"a"},
			desired: []config.Path{path("a"), path("b")},
			wantAdd: []string{"b"},
		},
		{
			name:       "remove one",
			live:       []string{"a", "b"},
			desired:    []config.Path{path("a")},
			wantRemove: []string{"b"},
		},
		{
			name:       "add and remove",
			live:       []string{"a", "b"},
			desired:    []config.Path{path("a"), path("c")},
			wantAdd:    []string{"c"},
			wantRemove: []string{"b"},
		},
		{
			name:    "reorder is not a change",
			live:    []string{"a", "b"},
			desired: []config.Path{path("b"), path("a")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			add, remove := diffPaths(tc.live, tc.desired)
			if got := names(add); !equalStrings(got, tc.wantAdd) {
				t.Errorf("add = %v, want %v", got, tc.wantAdd)
			}
			if !equalStrings(remove, tc.wantRemove) {
				t.Errorf("remove = %v, want %v", remove, tc.wantRemove)
			}
		})
	}
}

// TestReloadWarnings pins the T30 operability contract (review criticism 4): a
// membership-only reload logs an EXPLICIT warning for every change it cannot apply —
// a modified same-name path, a path reorder, and any non-path field — while a pure
// add/remove is applied silently (no warning).
func TestReloadWarnings(t *testing.T) {
	base := func() *config.Config {
		return &config.Config{Role: config.RoleEdge, Paths: []config.Path{path("a"), path("b")}}
	}
	modPath := func(name, addr string) config.Path {
		return config.Path{Name: name, SourceAddr: netip.MustParseAddr(addr)}
	}

	cases := []struct {
		name    string
		desired func() *config.Config
		want    string // substring the single expected warning must contain; "" = expect none
	}{
		{"identical", base, ""},
		{"membership add/remove only", func() *config.Config {
			c := base()
			c.Paths = []config.Path{path("a"), path("c")} // remove b, add c
			return c
		}, ""},
		{"modified path source", func() *config.Config {
			c := base()
			c.Paths = []config.Path{path("a"), modPath("b", "10.0.0.9")}
			return c
		}, `path "b"`},
		{"same-name link_bandwidth changed", func() *config.Config {
			// D70: a same-name path whose declared link_bandwidth changed must warn —
			// it is not applied (survivors keep original pacing) and the catch-all
			// zeroes Paths, so without the same-name link comparison it is silent.
			c := base()
			b := path("b")
			b.LinkBandwidthBitsPerSec = 1_000_000
			c.Paths = []config.Path{path("a"), b}
			return c
		}, `path "b"`},
		{"same-name link_rtt changed", func() *config.Config {
			// D70: a same-name path whose declared link_rtt changed must warn, same rationale.
			c := base()
			b := path("b")
			b.LinkRTT = 45 * time.Millisecond
			c.Paths = []config.Path{path("a"), b}
			return c
		}, `path "b"`},
		{"reordered paths", func() *config.Config {
			c := base()
			c.Paths = []config.Path{path("b"), path("a")}
			return c
		}, "reorder"},
		{"role changed", func() *config.Config {
			c := base()
			c.Role = config.RoleConcentrator
			return c
		}, "role"},
		{"log changed", func() *config.Config {
			c := base()
			c.Log = config.Log{Level: "debug"}
			return c
		}, "log"},
		{"tun_persist flipped", func() *config.Config {
			c := base()
			c.TUNPersist = true // base leaves it false; a reload cannot re-issue TUNSETPERSIST
			return c
		}, "tun_persist"},
		{"scheduler changed", func() *config.Config {
			c := base()
			c.Scheduler = config.SchedulerConfig{Policy: config.PolicyWeighted}
			return c
		}, "scheduler"},
		{"fec changed", func() *config.Config {
			c := base()
			c.FEC = config.FEC{Enabled: true, DataShards: 4, ParityShards: 2}
			return c
		}, "fec"},
		{"dns changed", func() *config.Config {
			c := base()
			c.DNS = config.DNS{Resolver: config.DNSResolverDoH, DoHURL: "https://example.com/dns-query"}
			return c
		}, "dns"},
		{"metrics changed", func() *config.Config {
			c := base()
			c.Metrics = config.Metrics{Listen: "127.0.0.1:9100"}
			return c
		}, ""}, // Metrics IS applied by Reload (rebinds /metrics); must not warn.
		{"monitor changed", func() *config.Config {
			c := base()
			c.Monitor = config.Monitor{Listen: "127.0.0.1:9200"}
			return c
		}, ""}, // Monitor IS applied by Reload (reconciles the endpoint, T169); must not warn.
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := reloadWarnings(base(), tc.desired())
			if tc.want == "" {
				if len(w) != 0 {
					t.Fatalf("want no warnings, got %v", w)
				}
				return
			}
			if !containsSubstr(w, tc.want) {
				t.Fatalf("want a warning containing %q, got %v", tc.want, w)
			}
			if len(w) != 1 {
				t.Fatalf("want exactly one warning, got %v", w)
			}
		})
	}
}

// TestReloadWarningsBind pins the Bind mode reload contract (D52): Bind lives at BOTH
// the top-level Config.Bind default AND the per-path Path.Bind (falling back to that
// default in normalize). A change to either must produce EXACTLY ONE actionable
// warning — the top-level-default warning, NOT the generic catch-all — and a
// per-path Bind change (independent of source/dest) gets its own per-path warning.
func TestReloadWarningsBind(t *testing.T) {
	bindPath := func(name string, mode config.BindMode) config.Path {
		p := path(name)
		p.Bind = mode
		return p
	}

	t.Run("top-level default bind changed", func(t *testing.T) {
		live := &config.Config{Role: config.RoleEdge, Bind: config.BindModeAuto, Paths: []config.Path{path("a")}}
		desired := &config.Config{Role: config.RoleEdge, Bind: config.BindModeDevice, Paths: []config.Path{path("a")}}
		w := reloadWarnings(live, desired)
		if len(w) != 1 {
			t.Fatalf("want exactly one warning, got %v", w)
		}
		if !strings.Contains(w[0], "default bind mode changed") {
			t.Fatalf("want the default-bind warning, got %v", w)
		}
	})

	t.Run("per-path bind changed", func(t *testing.T) {
		live := &config.Config{Role: config.RoleEdge, Bind: config.BindModeAuto, Paths: []config.Path{bindPath("a", config.BindModeAuto)}}
		desired := &config.Config{Role: config.RoleEdge, Bind: config.BindModeAuto, Paths: []config.Path{bindPath("a", config.BindModeSource)}}
		w := reloadWarnings(live, desired)
		if len(w) != 1 {
			t.Fatalf("want exactly one warning, got %v", w)
		}
		if !strings.Contains(w[0], `path "a" bind mode changed`) {
			t.Fatalf("want the per-path bind warning, got %v", w)
		}
	})
}

// TestReloadWarningsCatchAll pins the D52-option-B future-proofing catch-all: it
// compares live/desired with every individually-handled field zeroed, so a Config
// field this function does not yet know about still produces a generic warning
// rather than being silently accepted. Config's real field set is closed (Go has no
// runtime struct extension), so this is exercised two ways: (1) every case above
// that DOES have a dedicated warning must stay at exactly one warning — proving the
// catch-all does NOT double-fire once a field is individually handled; (2) this test
// enumerates config.Config's actual fields via reflection and asserts the set is
// EXACTLY the set reloadWarnings's catch-all zeroes — so adding a new field to
// Config without also updating reloadWarnings (with either a dedicated warning or an
// addition to the zeroed set) fails this test, forcing the invariant to be honoured.
func TestReloadWarningsCatchAll(t *testing.T) {
	known := map[string]bool{
		"Role": true, "Paths": true, "WireGuard": true, "Amnezia": true, "PSK": true,
		"Metrics": true, "Monitor": true, "Log": true, "Scheduler": true, "FEC": true, "DNS": true,
		"Bind": true, "TUNPersist": true, "WeightedCapacitySane": true,
	}
	typ := reflect.TypeOf(config.Config{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if !known[name] {
			t.Fatalf("config.Config has a new field %q that reloadWarnings's catch-all does not account for "+
				"(add a dedicated warning or zero it in the catch-all copy, then add it here)", name)
		}
		delete(known, name)
	}
	if len(known) != 0 {
		t.Fatalf("reloadWarnings/this test references fields no longer on config.Config: %v", known)
	}
}

// TestRunningConfigTracksMembership pins that the running config advances to the
// membership actually in service — survivors (original params/order) then added
// paths — so a subsequent reload diffs against the true running state (T30).
func TestRunningConfigTracksMembership(t *testing.T) {
	live := &config.Config{Role: config.RoleEdge, Paths: []config.Path{path("a"), path("b")}}
	got := runningConfig(live, []config.Path{path("c")}, []string{"b"})
	if !equalStrings(names(got.Paths), []string{"a", "c"}) {
		t.Fatalf("running paths = %v, want [a c]", names(got.Paths))
	}
	if got.Role != config.RoleEdge {
		t.Fatalf("running role = %q, want edge (non-path fields carried over)", got.Role)
	}
	// The original config's path slice is not mutated.
	if !equalStrings(names(live.Paths), []string{"a", "b"}) {
		t.Fatalf("runningConfig mutated the input paths: %v", names(live.Paths))
	}
}

func containsSubstr(ws []string, sub string) bool {
	for _, w := range ws {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

func names(ps []config.Path) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

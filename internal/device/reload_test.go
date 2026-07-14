package device

import (
	"net/netip"
	"strings"
	"testing"

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
		})
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

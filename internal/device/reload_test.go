package device

import (
	"net/netip"
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

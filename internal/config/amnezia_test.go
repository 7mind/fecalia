package config

import (
	"strings"
	"testing"
)

// fullAmnezia is a complete, valid obfuscation profile: the whole junk/size set
// plus a distinct custom magic-header set (all > 4, so the engine actually
// obfuscates the message-type headers).
func fullAmnezia() Amnezia {
	return Amnezia{
		Jc: 4, Jmin: 8, Jmax: 80, S1: 15, S2: 92,
		H1: 1_111_111, H2: 2_222_222, H3: 3_333_333, H4: 4_444_444,
	}
}

// validateAmnezia mirrors what Load does to the block: applyDefaults (part of
// normalize) then validate. Tests exercise the pair, since defaulting and
// validation are two halves of one invariant.
func validateAmnezia(a Amnezia) (Amnezia, error) {
	a.applyDefaults()
	return a, a.validate()
}

func TestAmneziaValidate(t *testing.T) {
	cases := []struct {
		name    string
		in      Amnezia
		wantErr string // "" = must accept
	}{
		{
			name: "unconfigured is plain wireguard",
			in:   Amnezia{},
		},
		{
			name: "full custom profile accepted",
			in:   fullAmnezia(),
		},
		{
			name: "junk set with defaulted headers accepted",
			in:   Amnezia{Jc: 4, Jmin: 8, Jmax: 80, S1: 15, S2: 92},
		},
		{
			name:    "partial: jc/jmin/jmax without s1/s2 rejected",
			in:      Amnezia{Jc: 4, Jmin: 8, Jmax: 80},
			wantErr: "incomplete obfuscation set",
		},
		{
			name:    "partial: s1 without s2 rejected",
			in:      Amnezia{Jc: 4, Jmin: 8, Jmax: 80, S1: 15},
			wantErr: "incomplete obfuscation set",
		},
		{
			name:    "partial: only magic headers, no junk set rejected",
			in:      Amnezia{H1: 1_111_111, H2: 2_222_222, H3: 3_333_333, H4: 4_444_444},
			wantErr: "incomplete obfuscation set",
		},
		{
			name:    "jmin greater than jmax rejected",
			in:      Amnezia{Jc: 4, Jmin: 80, Jmax: 8, S1: 15, S2: 92},
			wantErr: "jmin <= jmax",
		},
		{
			name:    "partial magic-header set rejected as non-distinct",
			in:      Amnezia{Jc: 4, Jmin: 8, Jmax: 80, S1: 15, S2: 92, H1: 1_111_111},
			wantErr: "magic headers must be a complete, distinct set",
		},
		{
			name:    "duplicate magic headers rejected",
			in:      Amnezia{Jc: 4, Jmin: 8, Jmax: 80, S1: 15, S2: 92, H1: 9, H2: 9, H3: 3, H4: 4},
			wantErr: "distinct",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateAmnezia(tc.in)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validate(%+v) = %v, want accept", tc.in, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validate(%+v) = nil, want error containing %q", tc.in, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validate error = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestAmneziaMagicHeaderDefaulting pins the D1 defaulting rule: a configured block
// that omits the magic headers gets the standard 1..4 set (never left at 0), while
// an explicit header set is preserved and an unconfigured block is untouched.
func TestAmneziaMagicHeaderDefaulting(t *testing.T) {
	junkOnly := Amnezia{Jc: 4, Jmin: 8, Jmax: 80, S1: 15, S2: 92}
	junkOnly.applyDefaults()
	if got := [4]uint32{junkOnly.H1, junkOnly.H2, junkOnly.H3, junkOnly.H4}; got != [4]uint32{1, 2, 3, 4} {
		t.Errorf("defaulted headers = %v, want [1 2 3 4]", got)
	}

	custom := fullAmnezia()
	before := [4]uint32{custom.H1, custom.H2, custom.H3, custom.H4}
	custom.applyDefaults()
	if got := [4]uint32{custom.H1, custom.H2, custom.H3, custom.H4}; got != before {
		t.Errorf("applyDefaults clobbered explicit headers: %v -> %v", before, got)
	}

	var off Amnezia
	off.applyDefaults()
	if off != (Amnezia{}) {
		t.Errorf("applyDefaults mutated an unconfigured block: %+v", off)
	}
}

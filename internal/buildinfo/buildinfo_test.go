package buildinfo_test

import (
	"runtime/debug"
	"testing"

	"github.com/7mind/wanbond/internal/buildinfo"
)

func TestSourceIdentity(t *testing.T) {
	settings := []debug.BuildSetting{{Key: "vcs.revision", Value: "abcdef"}, {Key: "vcs.time", Value: "2026-10-04T21:00:00+01:00"}}
	for _, tc := range []struct {
		name, commit, stamp string
		settings            []debug.BuildSetting
		want                buildinfo.Info
		invalid             bool
	}{
		{name: "Go VCS", settings: settings, want: buildinfo.Info{Version: "test", Commit: "abcdef", CommitTime: "2026-10-04T20:00:00Z"}},
		{name: "dirty Go VCS", settings: append(append([]debug.BuildSetting{}, settings...), debug.BuildSetting{Key: "vcs.modified", Value: "true"}), want: buildinfo.Info{Version: "test", Commit: "abcdef-dirty", CommitTime: "2026-10-04T20:00:00Z"}},
		{name: "Nix stamp", commit: "123456-dirty", stamp: "1791144000", settings: settings, want: buildinfo.Info{Version: "test", Commit: "123456-dirty", CommitTime: "2026-10-04T20:00:00Z"}},
		{name: "stamped commit does not borrow another commit time", commit: "123456", settings: settings, want: buildinfo.Info{Version: "test", Commit: "123456"}},
		{name: "no metadata", want: buildinfo.Info{Version: "test"}},
		{name: "invalid source time", commit: "123456", stamp: "invalid", invalid: true},
		{name: "source time outside RFC3339", commit: "123456", stamp: "9223372036854775807", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildinfo.FromSettings("test", tc.commit, tc.stamp, tc.settings)
			if (err != nil) != tc.invalid {
				t.Fatalf("metadata error = %v, want invalid %t", err, tc.invalid)
			}
			if !tc.invalid && got != tc.want {
				t.Fatalf("source identity = %+v, want %+v", got, tc.want)
			}
		})
	}
}

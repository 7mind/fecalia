package buildinfo

import (
	"fmt"
	"runtime/debug"
	"strconv"
	"time"
)

type Info struct {
	Version    string
	Commit     string
	CommitTime string // UTC source commit time, not compilation time; empty if unknown.
}

func Read(version, commit, commitTime string) (Info, error) {
	var settings []debug.BuildSetting
	if info, ok := debug.ReadBuildInfo(); ok {
		settings = info.Settings
	}
	return FromSettings(version, commit, commitTime, settings)
}

func FromSettings(version, commit, commitTime string, settings []debug.BuildSetting) (Info, error) {
	if commit == "" {
		modified := false
		for _, setting := range settings {
			switch setting.Key {
			case "vcs.revision":
				commit = setting.Value
			case "vcs.time":
				if commitTime == "" {
					commitTime = setting.Value
				}
			case "vcs.modified":
				modified = setting.Value == "true"
			}
		}
		if modified && commit != "" {
			commit += "-dirty"
		}
	}
	if commitTime != "" {
		stamp, err := time.Parse(time.RFC3339, commitTime)
		if err != nil {
			seconds, err := strconv.ParseInt(commitTime, 10, 64)
			if err != nil {
				return Info{}, fmt.Errorf("buildinfo: invalid source commit time %q", commitTime)
			}
			stamp = time.Unix(seconds, 0)
		}
		commitTime = stamp.UTC().Format(time.RFC3339)
	}
	return Info{Version: version, Commit: commit, CommitTime: commitTime}, nil
}

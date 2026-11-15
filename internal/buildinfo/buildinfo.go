package buildinfo

import (
	"fmt"
	"runtime/debug"
	"strings"
)

const (
	CLIName          = "satelle"
	DaemonName       = "satelled"
	DaemonVersionKey = "satelled.version"
)

type Info struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
}

func (i Info) String() string {
	c := i.Commit
	if len(c) > 12 {
		c = c[:12]
	}
	v := i.Version
	if base, marker, ok := strings.Cut(v, "+"); ok {
		v = base
		if strings.HasSuffix(marker, "-dirty") && !strings.HasSuffix(c, "-dirty") {
			c += "-dirty"
		}
	}
	return fmt.Sprintf("%s %s (commit %s, built %s)", i.Name, v, c, i.BuildTime)
}

var (
	Name      = "satelle"
	Version   = "dev"
	Commit    = "none"
	BuildTime = "unknown"
)

func IsReleaseVersion(v string) bool {
	return v != "" && v != "dev" && !strings.HasPrefix(v, "0.0.0-dev")
}

func Resolve() Info {
	var settings []debug.BuildSetting
	if bi, ok := debug.ReadBuildInfo(); ok {
		settings = bi.Settings
	}
	return resolveFrom(Info{Name: Name, Version: Version, Commit: Commit, BuildTime: BuildTime}, settings)
}

func resolveFrom(info Info, settings []debug.BuildSetting) Info {
	if info.Name == "" {
		info.Name = "satelle"
	}
	if IsReleaseVersion(info.Version) {
		return info
	}
	var rev, dirty, vcsTime string
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value
		case "vcs.time":
			vcsTime = s.Value
		}
	}
	if rev == "" {
		return info
	}
	short := rev
	if len(short) > 12 {
		short = short[:12]
	}
	v := "0.0.0-dev+" + short
	if dirty == "true" {
		v += "-dirty"
	}
	info.Version = v
	if info.Commit == "" || info.Commit == "none" {
		info.Commit = short
	}
	if (info.BuildTime == "" || info.BuildTime == "unknown") && vcsTime != "" {
		info.BuildTime = vcsTime
	}
	return info
}

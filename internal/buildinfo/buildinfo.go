// Package buildinfo holds satelle's build identity — the version, commit,
// and build time stamped into the binary via -ldflags. Info.String is the one
// line every surface shows (satelle version, satelled --version, satelled's
// header, service status), so it is plain which build is running.
package buildinfo

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// Artifact names. One source for the daemon identity so service, update,
// and the dedicated main do not each invent a string (sty_bd9de06d).
const (
	// CLIName is the command users type.
	CLIName = "satelle"
	// DaemonName is the local-tier daemon binary.
	DaemonName = "satelled"
	// DaemonVersionKey is the .version field for the daemon.
	DaemonVersionKey = "satelled.version"
)

// Info is the resolved build identity.
type Info struct {
	// Name is the artifact identity (e.g. "satelle", "satelled") so
	// footers and version lines brand the running binary, not a hard-coded
	// product string.
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
}

// String is the build identity line: "<name> <version> (commit <c>, built
// <t>)". The commit is shortened to 12 characters (CI stamps the full SHA); a
// dev build's version keeps its +<commit>[-dirty] marker. A bare `go build`
// reports the VCS commit time as its build time.
func (i Info) String() string {
	c := i.Commit
	if len(c) > 12 {
		c = c[:12]
	}
	return fmt.Sprintf("%s %s (commit %s, built %s)", i.Name, i.Version, c, i.BuildTime)
}

// Build info — overridden via -ldflags at release time. These are the
// single source of truth for satelle's build identity. Stamp with:
//
//	-ldflags "-X github.com/bobmcallan/satelle/internal/buildinfo.Version=0.0.1 ..."
//	-ldflags "-X github.com/bobmcallan/satelle/internal/buildinfo.Name=satelled ..."
var (
	// Name defaults to the CLI product; the daemon main stamps Name=satelled.
	Name      = "satelle"
	Version   = "dev"
	Commit    = "none"
	BuildTime = "unknown"
)

// IsReleaseVersion reports whether v is a stamped release version — a real
// tag, not the unstamped "dev" sentinel or the VCS-derived dev string.
func IsReleaseVersion(v string) bool {
	return v != "" && v != "dev" && !strings.HasPrefix(v, "0.0.0-dev")
}

// Resolve returns the effective build identity. A release binary is
// ldflag-stamped, so its values are returned verbatim. Any other build — a
// bare `go build`, an IDE run — leaves Version=="dev"; for those we fall back
// to the VCS stamp Go embeds via debug.ReadBuildInfo so `version` reports a
// real, git-derived string instead of the bare "dev".
func Resolve() Info {
	var settings []debug.BuildSetting
	if bi, ok := debug.ReadBuildInfo(); ok {
		settings = bi.Settings
	}
	return resolveFrom(Info{Name: Name, Version: Version, Commit: Commit, BuildTime: BuildTime}, settings)
}

// resolveFrom is the testable core of Resolve.
func resolveFrom(info Info, settings []debug.BuildSetting) Info {
	if info.Name == "" {
		info.Name = "satelle"
	}
	if IsReleaseVersion(info.Version) {
		return info // ldflag-stamped release build — trust it verbatim.
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
		return info // no embedded VCS info (e.g. built from a tarball).
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

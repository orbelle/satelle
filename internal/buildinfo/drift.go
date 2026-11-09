package buildinfo

import "fmt"

// Mismatch is one disagreement between the installed satelle CLI, the running
// satelled and the installed satelled, with the command that puts it right.
// Lines names the builds that disagree: "cli", "running", "installed".
type Mismatch struct {
	Text   string
	Remedy string
	Lines  []string
}

// Drift is the one rule satelle service status and satelled's footer share.
// A running satelled that differs from the installed one is left behind by an
// install: restart it. An installed CLI whose version differs from the
// installed satelled's is a half-done update: update. Versions are compared, not
// whole lines, because the CLI and satelled are separate builds. A CLI line that
// names no build is never compared. Restart comes first.
func Drift(cli, running, installed string) []Mismatch {
	var out []Mismatch
	if running != "" && installed != "" && running != installed {
		out = append(out, Mismatch{
			Text:   fmt.Sprintf("the running satelled (%s) differs from the installed one (%s)", running, installed),
			Remedy: "satelle service restart",
			Lines:  []string{"running", "installed"},
		})
	}
	cv, cok := VersionOf(cli)
	iv, iok := VersionOf(installed)
	if cok && iok && cv != iv {
		out = append(out, Mismatch{
			Text:   fmt.Sprintf("satelle %s differs from satelled %s", cv, iv),
			Remedy: "satelle update",
			Lines:  []string{"cli", "installed"},
		})
	}
	return out
}

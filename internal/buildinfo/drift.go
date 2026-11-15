package buildinfo

import "fmt"

type Mismatch struct {
	Text   string
	Remedy string
	Lines  []string
}

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

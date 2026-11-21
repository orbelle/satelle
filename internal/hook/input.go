package hook

import (
	"strings"

	"github.com/bobmcallan/satelle/internal/integration"
	"github.com/bobmcallan/satelle/internal/integration/common"
)

const (
	Claude = integration.Claude
	Pi     = integration.Pi
	Grok   = integration.Grok
	Cursor = integration.Cursor
)

var Harnesses = integration.Names()

func ForCLI(cli string) (string, bool) {
	for _, a := range integration.Hooks() {
		if a.CLI() == cli {
			return a.Name(), true
		}
	}
	return "", false
}

type Input common.HookInput

func (in Input) Harness(flag string) string {
	if in.DetectedHarness != "" {
		return in.DetectedHarness
	}
	return strings.TrimSpace(flag)
}

const TurnEndReason = common.TurnEndReason

func (in Input) TurnEnd() bool {
	return in.StopReason == "" || in.StopReason == TurnEndReason
}

func (in Input) IsShell() bool {
	return integration.IsShellTool(in.Tool) || (in.Tool == "" && in.Command != "")
}

func (in Input) IsEdit() bool { return !in.IsShell() && in.Path != "" }

func ParseInput(raw []byte) Input {
	for _, a := range integration.Hooks() {
		if in, claimed := a.ParseInput(raw); claimed {
			return Input(in)
		}
	}
	in, _ := integration.Fallback().ParseInput(raw)
	return Input(in)
}

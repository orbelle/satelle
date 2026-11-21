package hook

import (
	"strings"

	"github.com/bobmcallan/satelle/internal/integration"
	"github.com/bobmcallan/satelle/internal/integration/common"
)

const (
	EventSessionStart = common.EventSessionStart
	EventPrompt       = common.EventPrompt
)

func adapter(harness string) common.HookAdapter {
	if a, ok := integration.Hook(harness); ok {
		return a
	}
	return integration.Fallback()
}

func Deny(harness, reason string) []byte {
	if strings.TrimSpace(reason) == "" {
		reason = "satelle: denied"
	}
	return adapter(harness).Deny(reason)
}

func Context(harness, event, text, notice string) []byte {
	return adapter(harness).Context(event, text, notice)
}

func StopAnswer(harness string) []byte { return adapter(harness).StopAnswer() }

func StopBlock(harness, reason string) []byte { return adapter(harness).StopBlock(reason) }

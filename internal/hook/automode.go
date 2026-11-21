package hook

import (
	"fmt"
	"strings"

	"github.com/bobmcallan/satelle/internal/integration"
	"github.com/bobmcallan/satelle/internal/integration/common"
)

var ErrAutoModeUnavailable = common.ErrAutoModeUnavailable

func AutoModeLines(guidance []byte) []string { return common.AutoModeLines(guidance) }

func autoModeAdapter(harness string) (common.AutoModeAdapter, error) {
	a, ok := integration.Hook(harness)
	if !ok {
		return nil, fmt.Errorf("unknown harness %q (one of %s)", harness, strings.Join(Harnesses, ", "))
	}
	am, ok := a.(common.AutoModeAdapter)
	if !ok {
		return nil, common.AutoModeUnavailable(harness)
	}
	return am, nil
}

func AutoModeDisplay(harness string) (string, error) {
	am, err := autoModeAdapter(harness)
	if err != nil {
		return "", err
	}
	return am.AutoModeDisplay(), nil
}

func FormatAutoMode(harness string, guidance []byte) ([]byte, error) {
	am, err := autoModeAdapter(harness)
	if err != nil {
		return nil, err
	}
	return am.FormatAutoMode(guidance)
}

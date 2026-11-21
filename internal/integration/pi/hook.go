package pi

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/bobmcallan/satelle/internal/integration/common"
)

const (
	OwnedMarker  = "// satelle-owned:"
	hookFile     = ".pi/extensions/satelle.ts"
	callerMarker = "PI_CODING_AGENT"
	callerPrefix = "PI_SESSION_"
	shellTool    = "bash"
)

//go:embed pi_extension.ts.tmpl
var template string

func Extension() []byte {
	verbs, _ := json.Marshal(map[string]string{
		"gate": common.VerbGate, "context": common.VerbContext, "prompt": common.VerbPrompt, "stopcheck": common.VerbStopcheck,
	})
	return []byte(strings.NewReplacer("__SATELLE_VERBS__", string(verbs), "__SATELLE_HARNESS__", strconv.Quote(Name)).Replace(template))
}

func owned(raw []byte) bool { return strings.Contains(string(raw), OwnedMarker) }

type Hook struct{}

func (Hook) Name() string             { return Name }
func (Hook) CLI() string              { return CLI }
func (Hook) File() string             { return hookFile }
func (Hook) Fallback() bool           { return false }
func (Hook) ProjectDirVars() []string { return nil }
func (Hook) ShellTools() []string     { return []string{shellTool} }

func (Hook) CallerMarkers() (names, prefixes []string) {
	return []string{callerMarker}, []string{callerPrefix}
}

func (Hook) ParseInput([]byte) (common.HookInput, bool) { return common.HookInput{}, false }

func (Hook) Deny(reason string) []byte { return common.PreToolUseDeny(reason) }

func (Hook) Context(event, text, notice string) []byte {
	return common.EventContext(event, text, notice)
}

func (Hook) StopAnswer() []byte             { return nil }
func (Hook) StopBlock(reason string) []byte { return common.DecisionBlock(reason) }

func (Hook) Plan(prev []byte, absent bool, _ func(string) bool, ownedOnly bool) ([]byte, bool, error) {
	if ownedOnly && !absent && !owned(prev) {
		return nil, false, common.ErrNotOwned
	}
	body := Extension()
	return body, absent || !bytes.Equal(prev, body), nil
}

func (Hook) Remove(prev []byte) ([]byte, bool, error) { return nil, owned(prev), nil }

func (Hook) Commands(string) ([]string, error) { return nil, nil }

func (Hook) Installed(raw []byte) bool { return owned(raw) }

func (Hook) AutoModeDisplay() (string, error) { return "", common.AutoModeUnavailable(Name) }

func (Hook) FormatAutoMode([]byte) ([]byte, error) { return nil, common.AutoModeUnavailable(Name) }

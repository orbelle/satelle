package hook

import (
	"encoding/json"
	"strings"
)

// Deny returns a PreToolUse deny for harness carrying reason. Grok reads a
// top-level decision/reason; claude and pi (through its extension) read
// claude's hookSpecificOutput.
func Deny(harness, reason string) []byte {
	if strings.TrimSpace(reason) == "" {
		reason = "satelle: denied"
	}
	if harness == Grok {
		return marshal(struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}{"deny", reason})
	}
	var doc struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	doc.HookSpecificOutput.HookEventName = "PreToolUse"
	doc.HookSpecificOutput.PermissionDecision = "deny"
	doc.HookSpecificOutput.PermissionDecisionReason = reason
	return marshal(doc)
}

// Context returns the answer that adds text to the model's context for event
// (SessionStart or UserPromptSubmit). Every harness reads claude's shape; the
// pi extension forwards additionalContext into its system prompt.
func Context(event, text string) []byte {
	var doc struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	doc.HookSpecificOutput.HookEventName = event
	doc.HookSpecificOutput.AdditionalContext = text
	return marshal(doc)
}

// StopBlock returns a Stop answer that refuses the stop with reason.
func StopBlock(reason string) []byte {
	return marshal(struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}{"block", reason})
}

func marshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("hook: marshal: " + err.Error())
	}
	return b
}

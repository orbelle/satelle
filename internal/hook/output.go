package hook

import (
	"encoding/json"
	"strings"
)

// Deny returns a PreToolUse deny for harness carrying reason. Grok reads a
// top-level decision/reason; cursor's preToolUse reads permission and shows
// the agent user_message (probed on cursor-agent 2026.11.05: it ignores
// decision, and the rejection the agent sees is user_message), so the reason
// goes in both messages; claude and pi (through its extension) read claude's
// hookSpecificOutput.
func Deny(harness, reason string) []byte {
	if strings.TrimSpace(reason) == "" {
		reason = "satelle: denied"
	}
	if harness == Cursor {
		return marshal(struct {
			Permission   string `json:"permission"`
			UserMessage  string `json:"user_message"`
			AgentMessage string `json:"agent_message"`
		}{"deny", reason, reason})
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

// Allow returns harness's PreToolUse answer that lets a tool call run without
// the harness's own approval: cursor's {"permission":"allow"}. Claude, pi and
// grok have none here (their allow channel is permission rules), so it is nil.
func Allow(harness string) []byte {
	if harness == Cursor {
		return marshal(struct {
			Permission string `json:"permission"`
		}{"allow"})
	}
	return nil
}

// SessionStart and UserPromptSubmit are the context events in claude's names.
const (
	EventSessionStart = "SessionStart"
	EventPrompt       = "UserPromptSubmit"
)

// Context returns harness's answer that adds text to the model's context for
// event (EventSessionStart or EventPrompt). Claude, pi and grok read claude's
// shape (the pi extension forwards additionalContext into its system prompt).
// Cursor's sessionStart reads additional_context; its beforeSubmitPrompt has
// no context channel (it answers only continue and user_message), so a
// cursor prompt answer is PromptContinue instead.
func Context(harness, event, text string) []byte {
	if harness == Cursor {
		if event == EventSessionStart {
			return marshal(struct {
				AdditionalContext string `json:"additional_context"`
			}{text})
		}
		return PromptContinue()
	}
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

// PromptContinue is cursor's beforeSubmitPrompt answer that lets the prompt
// through: {"continue":true}.
func PromptContinue() []byte {
	return marshal(struct {
		Continue bool `json:"continue"`
	}{true})
}

// StopAnswer is harness's answer to the stop (turn end) event when satelle
// has nothing to add: cursor reads a JSON object and would submit a
// followup_message as a new prompt, so it gets an empty object; claude, pi and
// grok take no output.
func StopAnswer(harness string) []byte {
	if harness == Cursor {
		return []byte("{}")
	}
	return nil
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

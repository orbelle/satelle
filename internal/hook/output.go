package hook

import (
	"encoding/json"
	"strings"
)

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

const (
	EventSessionStart = "SessionStart"
	EventPrompt       = "UserPromptSubmit"
)

func Context(harness, event, text, notice string) []byte {
	if harness == Cursor {
		if event == EventSessionStart {
			return marshal(struct {
				AdditionalContext string `json:"additional_context"`
			}{text})
		}
		return PromptContinue()
	}
	if harness == Grok {
		if event != EventSessionStart {
			return nil
		}
		return marshal(struct {
			SystemMessage string `json:"systemMessage"`
		}{text})
	}
	var doc struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
		SystemMessage string `json:"systemMessage,omitempty"`
	}
	doc.HookSpecificOutput.HookEventName = event
	doc.HookSpecificOutput.AdditionalContext = text
	doc.SystemMessage = notice
	return marshal(doc)
}

func PromptContinue() []byte {
	return marshal(struct {
		Continue bool `json:"continue"`
	}{true})
}

func StopAnswer(harness string) []byte {
	if harness == Cursor {
		return []byte("{}")
	}
	return nil
}

func StopBlock(harness, reason string) []byte {
	if harness == Cursor {
		return marshal(struct {
			FollowupMessage string `json:"followup_message"`
		}{reason})
	}
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

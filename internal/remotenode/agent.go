package remotenode

import "encoding/json"

const AgentJob = "agent"

type AgentRequest struct {
	Text         string `json:"text"`
	Binding      string `json:"binding,omitempty"`
	SystemPrompt string `json:"system_prompt,omitempty"`
}

type AgentReply struct {
	Text      string `json:"text"`
	Model     string `json:"model,omitempty"`
	Command   string `json:"command,omitempty"`
	TokensIn  int    `json:"tokens_in,omitempty"`
	TokensOut int    `json:"tokens_out,omitempty"`
}

func DecodeAgentReply(body json.RawMessage) (AgentReply, error) {
	var r AgentReply
	err := json.Unmarshal(body, &r)
	return r, err
}

package remotenode

import "encoding/json"

// AgentJob is the job type that talks to the node's agent: the request carries
// the developer's message and the reply the agent's answer.
const AgentJob = "agent"

// AgentRequest is the body of an AgentJob request.
//
// Binding names the node's own agents.toml binding that performs the step
// (empty: the run's binding), SystemPrompt is the step's skill and Text the
// work.
type AgentRequest struct {
	Text         string `json:"text"`
	Binding      string `json:"binding,omitempty"`
	SystemPrompt string `json:"system_prompt,omitempty"`
}

// AgentReply is the body of an AgentJob reply: the performer's answer and
// what it cost.
type AgentReply struct {
	Text      string `json:"text"`
	Model     string `json:"model,omitempty"`
	TokensIn  int    `json:"tokens_in,omitempty"`
	TokensOut int    `json:"tokens_out,omitempty"`
}

// DecodeAgentReply reads an AgentJob reply body.
func DecodeAgentReply(body json.RawMessage) (AgentReply, error) {
	var r AgentReply
	err := json.Unmarshal(body, &r)
	return r, err
}

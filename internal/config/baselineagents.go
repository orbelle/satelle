// Baseline agent seats: the binary ships none (the embedded default process
// left the binary). A repo's agents file names every seat its route uses.
package config

// BaselineAgents returns the baseline seats: none.
func BaselineAgents() (AgentsConfig, error) {
	return AgentsConfig{Agents: map[string]AgentBinding{}}, nil
}

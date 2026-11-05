package config

import "fmt"

// Scope is a position on the local -> personal -> team sync ladder.
type Scope int

const (
	// LocalScope never leaves this machine.
	LocalScope Scope = iota
	// PersonalScope syncs to the same operator's other machines.
	PersonalScope
	// TeamScope syncs to a team.
	TeamScope
)

// String renders the scope as its recorded word.
func (s Scope) String() string {
	switch s {
	case PersonalScope:
		return "personal"
	case TeamScope:
		return "team"
	default:
		return "local"
	}
}

// ParseScope parses a recorded scope word.
func ParseScope(s string) (Scope, error) {
	switch s {
	case "local":
		return LocalScope, nil
	case "personal":
		return PersonalScope, nil
	case "team":
		return TeamScope, nil
	}
	return LocalScope, fmt.Errorf("config: unknown sync scope %q", s)
}

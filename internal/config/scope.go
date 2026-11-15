package config

import "fmt"

type Scope int

const (
	LocalScope Scope = iota
	PersonalScope
	TeamScope
)

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
